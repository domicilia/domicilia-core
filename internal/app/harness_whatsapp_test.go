package app_test

// Piezas de prueba del módulo WhatsApp: una Graph API de Meta simulada y constructores de webhooks
// firmados. Lo único simulado es Meta; la aplicación, la base de datos y el cifrado son los reales.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/meta"
)

const (
	testAppSecret   = "secreto-de-la-app-de-la-plataforma-0001"
	testVerifyToken = "token-de-verificacion-0001"
	testMetaVersion = "v99.0"
)

// newSecretsKey genera una llave de cifrado válida (32 bytes en base64).
func newSecretsKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// sentMessage es un envío que llegó a la Graph API simulada.
type sentMessage struct {
	Token         string
	PhoneNumberID string
	To            string
	Body          string
	ReplyTo       string
}

// fakeMeta simula la Graph API: qué cuentas existen (con su token y sus números), y qué responde al enviar.
type fakeMeta struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	accounts map[string]*fakeAccount // por waba_id
	sent     []sentMessage
	script   []scriptedReply // respuestas para los próximos envíos, en orden
	seq      int
	calls    int
}

type fakeAccount struct {
	token   string
	numbers []map[string]any
}

// scriptedReply es la respuesta de un envío. Cero valores = éxito con un wamid nuevo.
type scriptedReply struct {
	status int
	body   string
	delay  time.Duration
	drop   bool // cierra la conexión sin responder
}

func newFakeMeta(t *testing.T) *fakeMeta {
	t.Helper()
	f := &fakeMeta{t: t, accounts: map[string]*fakeAccount{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// addAccount registra una cuenta con su token y un número (id de número y teléfono a mostrar).
func (f *fakeMeta) addAccount(waba, token string, numbers ...[2]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	acc := &fakeAccount{token: token}
	for _, n := range numbers {
		acc.numbers = append(acc.numbers, map[string]any{
			"id": n[0], "display_phone_number": n[1], "verified_name": "Negocio " + n[0],
			"quality_rating": "GREEN", "messaging_limit_tier": "TIER_1K", "code_verification_status": "VERIFIED",
		})
	}
	f.accounts[waba] = acc
}

func (f *fakeMeta) revokeToken(waba string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[waba].token = "revocado-" + uuid.NewString()
}

func (f *fakeMeta) setToken(waba, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[waba].token = token
}

func (f *fakeMeta) reply(r ...scriptedReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, r...)
}

func (f *fakeMeta) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeMeta) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func metaError(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "code": code, "fbtrace_id": "T"}})
}

func (f *fakeMeta) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // v99.0/{id}/{recurso}
	if len(parts) != 3 || parts[0] != testMetaVersion {
		http.NotFound(w, r)
		return
	}
	id, resource := parts[1], parts[2]

	switch {
	case r.Method == http.MethodGet && resource == "phone_numbers":
		f.mu.Lock()
		acc := f.accounts[id]
		f.mu.Unlock()
		if acc == nil {
			metaError(w, http.StatusBadRequest, 100, "Unsupported get request. Object with ID '"+id+"' does not exist")
			return
		}
		if acc.token != token {
			metaError(w, http.StatusBadRequest, 190, "Invalid OAuth access token.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": acc.numbers})

	case r.Method == http.MethodPost && resource == "messages":
		f.serveSend(w, r, id, token)

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeMeta) serveSend(w http.ResponseWriter, r *http.Request, phoneNumberID, token string) {
	var req struct {
		To      string                `json:"to"`
		Type    string                `json:"type"`
		Text    struct{ Body string } `json:"text"`
		Context struct {
			MessageID string `json:"message_id"`
		} `json:"context"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	var next scriptedReply
	if len(f.script) > 0 {
		next, f.script = f.script[0], f.script[1:]
	}
	f.seq++
	wamid := fmt.Sprintf("wamid.OUT%04d", f.seq)
	f.sent = append(f.sent, sentMessage{Token: token, PhoneNumberID: phoneNumberID, To: req.To, Body: req.Text.Body, ReplyTo: req.Context.MessageID})
	f.mu.Unlock()

	if next.delay > 0 {
		time.Sleep(next.delay)
	}
	if next.drop {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
		return
	}
	if next.status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.status)
		_, _ = w.Write([]byte(next.body))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"messaging_product":"whatsapp","contacts":[{"wa_id":%q}],"messages":[{"id":%q}]}`, req.To, wamid)
}

// ---------------------------------------------------------------------------
// Webhooks firmados
// ---------------------------------------------------------------------------

// waMsg es un mensaje entrante para construir un webhook.
type waMsg struct {
	From      string // wa_id sin "+"
	Name      string
	ID        string // wamid
	Type      string // text por omisión
	Body      string
	At        time.Time
	ReplyToID string
	Extra     map[string]any // campos propios del tipo (image, location...)
}

// waStatus es un estado de entrega para construir un webhook.
type waStatus struct {
	ID       string
	Status   string
	At       time.Time
	Code     int
	Detail   string
	Category string
}

// webhookBody arma el cuerpo de una entrega de Meta para un número.
func webhookBody(t *testing.T, waba, phoneNumberID string, msgs []waMsg, statuses []waStatus) []byte {
	t.Helper()
	value := map[string]any{
		"messaging_product": "whatsapp",
		"metadata":          map[string]any{"display_phone_number": "573000000000", "phone_number_id": phoneNumberID},
	}
	var contacts, messages, sts []map[string]any
	for _, m := range msgs {
		typ := m.Type
		if typ == "" {
			typ = "text"
		}
		at := m.At
		if at.IsZero() {
			at = time.Now()
		}
		msg := map[string]any{"from": m.From, "id": m.ID, "timestamp": fmt.Sprint(at.Unix()), "type": typ}
		if typ == "text" {
			msg["text"] = map[string]any{"body": m.Body}
		}
		for k, v := range m.Extra {
			msg[k] = v
		}
		if m.ReplyToID != "" {
			msg["context"] = map[string]any{"id": m.ReplyToID}
		}
		messages = append(messages, msg)
		contacts = append(contacts, map[string]any{"wa_id": m.From, "profile": map[string]any{"name": m.Name}})
	}
	for _, s := range statuses {
		at := s.At
		if at.IsZero() {
			at = time.Now()
		}
		st := map[string]any{"id": s.ID, "status": s.Status, "timestamp": fmt.Sprint(at.Unix()), "recipient_id": "573001112222"}
		if s.Code != 0 {
			st["errors"] = []map[string]any{{"code": s.Code, "title": "Error", "message": s.Detail}}
		}
		if s.Category != "" {
			st["pricing"] = map[string]any{"billable": true, "pricing_model": "PMP", "category": s.Category}
		}
		sts = append(sts, st)
	}
	if len(messages) > 0 {
		value["contacts"], value["messages"] = contacts, messages
	}
	if len(sts) > 0 {
		value["statuses"] = sts
	}
	b, err := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry":  []any{map[string]any{"id": waba, "changes": []any{map[string]any{"field": "messages", "value": value}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// postWebhook envía un cuerpo firmado con el secreto dado ("" = sin firma).
func (h *harness) postWebhook(body []byte, secret string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/whatsapp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Hub-Signature-256", meta.Sign(secret, body))
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// inbound entrega mensajes de clientes a un número, firmados con el secreto de la plataforma.
func (h *harness) inbound(phoneNumberID string, msgs ...waMsg) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.postWebhook(webhookBody(h.t, "WABA", phoneNumberID, msgs, nil), testAppSecret)
}

// status entrega estados de entrega a un número.
func (h *harness) status(phoneNumberID string, sts ...waStatus) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.postWebhook(webhookBody(h.t, "WABA", phoneNumberID, nil, sts), testAppSecret)
}

// dispatchAll ejecuta el trabajador de envío hasta vaciar lo que esté listo.
func (h *harness) dispatchAll() {
	h.t.Helper()
	for range 20 {
		n, err := h.workers.WhatsApp.RunOnce(context.Background())
		must(h.t, err)
		if n == 0 {
			return
		}
	}
}

// connected es una bandeja conectada en las pruebas.
type connected struct {
	Inbox         string // id de la bandeja
	PhoneNumberID string
	WABA          string
	Token         string
}

// connect conecta un número a una organización por la API (contra la Graph API simulada) y devuelve la bandeja.
func (h *harness) connect(admin person, o organization, name string) connected {
	h.t.Helper()
	suffix := fmt.Sprint(len(h.meta.accounts) + 1)
	c := connected{PhoneNumberID: "1000000" + suffix, WABA: "2000000" + suffix, Token: "EAAG-token-" + uuid.NewString()}
	h.meta.addAccount(c.WABA, c.Token, [2]string{c.PhoneNumberID, "+57 300 000 000" + suffix})
	rec := h.do(http.MethodPost, orgURL(o, "/inboxes/whatsapp"), map[string]any{
		"name": name, "waba_id": c.WABA, "phone_number_id": c.PhoneNumberID, "access_token": c.Token,
	}, &admin)
	want(h.t, rec, http.StatusCreated)
	c.Inbox = jsonMap(h.t, rec)["id"].(string)
	return c
}

// convOf devuelve la conversación (id) de un contacto por su teléfono E.164 en una organización.
func convOf(t *testing.T, o organization, phone string) string {
	t.Helper()
	return str(t, `SELECT c.id::text FROM conversations c JOIN contacts ct ON ct.id = c.contact_id
		WHERE c.organization_id = $1 AND ct.phone_e164 = $2 ORDER BY c.created_at DESC LIMIT 1`, o.ID, phone)
}

func str2(n int64) string { return fmt.Sprint(n) }

// sigOf firma un cuerpo como lo haría Meta.
func sigOf(secret string, body []byte) string { return meta.Sign(secret, body) }

// mixedBody arma UNA carga con cambios para varios números (cada par es número, wamid): lo que un atacante
// fabricaría para colar eventos ajenos junto a los propios.
func mixedBody(t *testing.T, pairs ...[2]string) []byte {
	t.Helper()
	var changes []any
	for _, p := range pairs {
		changes = append(changes, map[string]any{"field": "messages", "value": map[string]any{
			"messaging_product": "whatsapp",
			"metadata":          map[string]any{"phone_number_id": p[0]},
			"contacts":          []any{map[string]any{"wa_id": "573007776666", "profile": map[string]any{"name": "X"}}},
			"messages": []any{map[string]any{"from": "573007776666", "id": p[1], "timestamp": fmt.Sprint(time.Now().Unix()),
				"type": "text", "text": map[string]any{"body": "forjado"}}},
		}})
	}
	b, err := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "W", "changes": changes}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
