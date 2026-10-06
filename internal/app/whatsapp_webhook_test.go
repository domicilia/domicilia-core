package app_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/domicilia/domicilia-core/internal/platform/config"
)

// Webhook de Meta. Lo que más se prueba es lo que debe RECHAZARSE o ignorarse: un evento falso que crea
// mensajes en la bandeja de un negocio, o un evento de un negocio que aterriza en otro, no da un error visible.

func verifyURL(mode, token, challenge string) string {
	q := url.Values{}
	if mode != "" {
		q.Set("hub.mode", mode)
	}
	if token != "" {
		q.Set("hub.verify_token", token)
	}
	q.Set("hub.challenge", challenge)
	return "/webhooks/whatsapp?" + q.Encode()
}

func TestSuscripcionDelWebhook(t *testing.T) {
	h := newHarness(t)
	t.Run("responde el desafío con el token correcto", func(t *testing.T) {
		rec := h.do(http.MethodGet, verifyURL("subscribe", testVerifyToken, "1158201444"), nil, nil)
		want(t, rec, http.StatusOK)
		if rec.Body.String() != "1158201444" {
			t.Fatalf("cuerpo = %q", rec.Body.String())
		}
	})
	t.Run("rechaza un token o un modo incorrectos", func(t *testing.T) {
		for name, u := range map[string]string{
			"token equivocado": verifyURL("subscribe", "otro-token", "x"),
			"sin token":        verifyURL("subscribe", "", "x"),
			"modo equivocado":  verifyURL("unsubscribe", testVerifyToken, "x"),
			"sin modo":         verifyURL("", testVerifyToken, "x"),
			"token a medias":   verifyURL("subscribe", testVerifyToken[:5], "x"),
		} {
			t.Run(name, func(t *testing.T) {
				rec := h.do(http.MethodGet, u, nil, nil)
				want(t, rec, http.StatusForbidden)
				if strings.Contains(rec.Body.String(), "x") && rec.Body.String() == "x" {
					t.Fatal("no debía devolver el desafío")
				}
			})
		}
	})
	t.Run("sin token de verificación configurado responde 503", func(t *testing.T) {
		h2 := newHarness(t, func(c *config.Config) { c.WhatsAppVerifyToken = "" })
		want(t, h2.do(http.MethodGet, verifyURL("subscribe", "", "x"), nil, nil), http.StatusServiceUnavailable)
		want(t, h2.do(http.MethodGet, verifyURL("subscribe", "cualquiera", "x"), nil, nil), http.StatusServiceUnavailable)
	})
}

func TestElWebhookExigeFirma(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	c := h.connect(admin, o, "P")
	body := webhookBody(t, "WABA", c.PhoneNumberID, []waMsg{{From: "573001112222", Name: "Ana", ID: "wamid.IN1", Body: "hola"}}, nil)

	store := func() (int, int, int) {
		return count(t, `SELECT count(*) FROM webhook_events`), count(t, `SELECT count(*) FROM messages`), count(t, `SELECT count(*) FROM contacts`)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"sin firma":                h.postWebhook(body, ""),
		"firmado con otro secreto": h.postWebhook(body, "un-secreto-cualquiera"),
		"firmado con el secreto de la plataforma pero alterado": func() *httptest.ResponseRecorder {
			signed := body
			forged := []byte(strings.Replace(string(body), "hola", "chao", 1))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/whatsapp", strings.NewReader(string(forged)))
			req.Header.Set("X-Hub-Signature-256", sigOf(testAppSecret, signed))
			r := httptest.NewRecorder()
			h.handler.ServeHTTP(r, req)
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			want(t, rec, http.StatusUnauthorized)
		})
	}
	if ev, msgs, cts := store(); ev != 0 || msgs != 0 || cts != 0 {
		t.Fatalf("un evento sin firma válida dejó rastro: eventos=%d mensajes=%d contactos=%d", ev, msgs, cts)
	}

	t.Run("con la firma correcta se procesa", func(t *testing.T) {
		want(t, h.postWebhook(body, testAppSecret), http.StatusOK)
		if ev, msgs, cts := store(); ev != 1 || msgs != 1 || cts != 1 {
			t.Fatalf("eventos=%d mensajes=%d contactos=%d", ev, msgs, cts)
		}
	})

	t.Run("sin ningún secreto configurado nada se puede firmar", func(t *testing.T) {
		h2 := newHarness(t, func(c *config.Config) { c.WhatsAppAppSecret = "" })
		admin2, o2 := orgAdmin(h2, "Acme")
		c2 := h2.connect(admin2, o2, "P")
		b := webhookBody(t, "WABA", c2.PhoneNumberID, []waMsg{{From: "573001112222", ID: "w1", Body: "x"}}, nil)
		want(t, h2.postWebhook(b, ""), http.StatusUnauthorized)
		want(t, h2.postWebhook(b, "cualquiera"), http.StatusUnauthorized)
		// Un secreto vacío tampoco "valida": la firma de un secreto vacío no sirve.
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/whatsapp", strings.NewReader(string(b)))
		req.Header.Set("X-Hub-Signature-256", sigOf("", b))
		r := httptest.NewRecorder()
		h2.handler.ServeHTTP(r, req)
		want(t, r, http.StatusUnauthorized)
		if n := count(t, `SELECT count(*) FROM messages`); n != 0 {
			t.Fatalf("mensajes = %d", n)
		}
	})

	t.Run("una carga que no es de WhatsApp o está rota es un 422", func(t *testing.T) {
		for name, b := range map[string]string{
			"no es JSON": "hola", "otro producto": `{"object":"page","entry":[]}`, "vacío": ``,
		} {
			t.Run(name, func(t *testing.T) { want(t, h.postWebhook([]byte(b), testAppSecret), http.StatusUnprocessableEntity) })
		}
	})

	t.Run("un cuerpo enorme se corta", func(t *testing.T) {
		// Más grande que MaxBodyBytes del arnés (8 MiB — ver newHarness: lo necesita la subida de
		// fotos de producto, no este webhook).
		big := []byte(`{"object":"whatsapp_business_account","entry":[],"x":"` + strings.Repeat("a", 9<<20) + `"}`)
		want(t, h.postWebhook(big, testAppSecret), http.StatusRequestEntityTooLarge)
	})
}

// El secreto de app propio de UN negocio solo autoriza los eventos de SUS números. Sin esta regla, quien tenga su
// propia app de Meta podría fabricar mensajes en la bandeja de cualquier otro negocio.
func TestElSecretoPropioDeUnNegocioNoAutorizaEventosAjenos(t *testing.T) {
	h := newHarness(t)
	adminA, a := orgAdmin(h, "Org A")
	adminB, b := orgAdmin(h, "Org B")
	const secretA = "secreto-de-la-app-propia-de-org-a-123"

	h.meta.addAccount("300001", "token-a", [2]string{"400001", "+57 300 111 1111"})
	want(t, h.do(http.MethodPost, inboxesURL(a, "/whatsapp"), map[string]any{
		"name": "A", "waba_id": "300001", "phone_number_id": "400001", "access_token": "token-a", "app_secret": secretA,
	}, &adminA), http.StatusCreated)
	cb := h.connect(adminB, b, "B") // usa la app de la plataforma

	msgA := waMsg{From: "573001112222", Name: "Ana", ID: "wamid.A1", Body: "para A"}
	msgB := waMsg{From: "573009998888", Name: "Beto", ID: "wamid.B1", Body: "para B"}

	post := func(body []byte, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/whatsapp", strings.NewReader(string(body)))
		req.Header.Set("X-Hub-Signature-256", sigOf(secret, body))
		r := httptest.NewRecorder()
		h.handler.ServeHTTP(r, req)
		return r
	}

	t.Run("el secreto propio de A autoriza un evento del número de A", func(t *testing.T) {
		want(t, post(webhookBody(t, "300001", "400001", []waMsg{msgA}, nil), secretA), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM messages WHERE organization_id = $1`, a.ID); n != 1 {
			t.Fatalf("mensajes de A = %d", n)
		}
	})

	t.Run("un evento del número de A firmado con OTRO secreto se rechaza (no basta con que A tenga un secreto propio)", func(t *testing.T) {
		for name, secret := range map[string]string{"un secreto cualquiera": "otro-secreto-0123456789", "el de la app de otro negocio": "secreto-de-la-app-propia-de-org-b"} {
			t.Run(name, func(t *testing.T) {
				want(t, post(webhookBody(t, "300001", "400001", []waMsg{{From: "573005554444", Name: "Falso", ID: "wamid.FORJADO", Body: "forjado"}}, nil), secret), http.StatusUnauthorized)
			})
		}
		if n := count(t, `SELECT count(*) FROM messages WHERE wamid = 'wamid.FORJADO'`); n != 0 {
			t.Fatalf("un evento con firma incorrecta creó %d mensajes en la bandeja de A", n)
		}
	})

	t.Run("el secreto de A NO autoriza un evento del número de B", func(t *testing.T) {
		want(t, post(webhookBody(t, "WABA", cb.PhoneNumberID, []waMsg{msgB}, nil), secretA), http.StatusUnauthorized)
		if n := count(t, `SELECT count(*) FROM messages WHERE organization_id = $1`, b.ID); n != 0 {
			t.Fatalf("A logró escribir en la bandeja de B: %d mensajes", n)
		}
	})

	t.Run("mezclar en una misma carga el número de A y el de B tampoco sirve: se rechaza todo", func(t *testing.T) {
		mixed := mixedBody(t, [2]string{"400001", "wamid.A2"}, [2]string{cb.PhoneNumberID, "wamid.B2"})
		want(t, post(mixed, secretA), http.StatusUnauthorized)
		if n := count(t, `SELECT count(*) FROM messages WHERE wamid IN ('wamid.A2', 'wamid.B2')`); n != 0 {
			t.Fatalf("se procesó algo de una carga rechazada: %d", n)
		}
	})

	t.Run("el secreto de la plataforma autoriza los números de cualquiera", func(t *testing.T) {
		want(t, post(webhookBody(t, "WABA", cb.PhoneNumberID, []waMsg{msgB}, nil), testAppSecret), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM messages WHERE organization_id = $1`, b.ID); n != 1 {
			t.Fatalf("mensajes de B = %d", n)
		}
	})

	t.Run("un secreto de app propio se guarda cifrado", func(t *testing.T) {
		if enc := str(t, `SELECT app_secret_enc FROM whatsapp_accounts WHERE waba_id = '300001'`); strings.Contains(enc, secretA) || !strings.HasPrefix(enc, "v1:") {
			t.Fatalf("secreto guardado = %q", enc)
		}
	})
}

func TestUnNumeroDesconocidoSeIgnora(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	h.connect(admin, o, "P")
	want(t, h.inbound("999999999", waMsg{From: "573001112222", ID: "w1", Body: "hola"}), http.StatusOK)
	if n := count(t, `SELECT count(*) FROM messages`) + count(t, `SELECT count(*) FROM contacts`) + count(t, `SELECT count(*) FROM conversations`); n != 0 {
		t.Fatalf("un evento para un número desconocido creó %d filas", n)
	}
}

func TestMensajesEntrantes(t *testing.T) {
	newOrg := func(t *testing.T) (*harness, organization, connected) {
		h := newHarness(t)
		h.clock = time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC)
		admin, o := orgAdmin(h, "Acme")
		return h, o, h.connect(admin, o, "Pedidos")
	}

	t.Run("crea contacto y conversación, abre la ventana de 24 h y cuenta lo no leído", func(t *testing.T) {
		h, o, c := newOrg(t)
		at := h.clock.Add(-2 * time.Minute)
		want(t, h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: "Ana Gómez", ID: "wamid.IN1", Body: "  Hola,\n quiero un pedido  ", At: at}), http.StatusOK)

		if got := str(t, `SELECT name || '|' || source || '|' || phone_e164 FROM contacts WHERE organization_id = $1`, o.ID); got != "Ana Gómez|inbound|+573001112222" {
			t.Errorf("contacto = %q", got)
		}
		if got := str(t, `SELECT display_id || '|' || status || '|' || unread_count || '|' || handled_by FROM conversations`); got != "1|open|1|human" {
			t.Errorf("conversación = %q", got)
		}
		if got := str(t, `SELECT extract(epoch FROM last_customer_message_at)::bigint::text FROM conversations`); got != str2(at.Unix()) {
			t.Errorf("last_customer_message_at = %s, quería %d", got, at.Unix())
		}
		if got := str(t, `SELECT last_message_preview || '|' || last_message_direction FROM conversations`); got != "Hola, quiero un pedido|inbound" {
			t.Errorf("vista previa = %q", got)
		}
		if got := str(t, `SELECT direction || '|' || kind || '|' || status || '|' || wamid FROM messages`); got != "inbound|text|received|wamid.IN1" {
			t.Errorf("mensaje = %q", got)
		}
	})

	t.Run("los siguientes mensajes van a la MISMA conversación; otro contacto abre otra numerada", func(t *testing.T) {
		h, o, c := newOrg(t)
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: "Ana", ID: "w1", Body: "uno"})
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: "Ana", ID: "w2", Body: "dos"})
		h.inbound(c.PhoneNumberID, waMsg{From: "573009998888", Name: "Beto", ID: "w3", Body: "tres"})
		if n := count(t, `SELECT count(*) FROM conversations WHERE organization_id = $1`, o.ID); n != 2 {
			t.Fatalf("conversaciones = %d", n)
		}
		if got := str(t, `SELECT string_agg(display_id::text || ':' || unread_count, ',' ORDER BY display_id) FROM conversations`); got != "1:2,2:1" {
			t.Errorf("numeración y no leídos = %q", got)
		}
	})

	t.Run("la numeración es independiente por organización", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		ca, cb := h.connect(adminA, a, "A"), h.connect(adminB, b, "B")
		h.inbound(ca.PhoneNumberID, waMsg{From: "573001112222", ID: "a1", Body: "x"})
		h.inbound(ca.PhoneNumberID, waMsg{From: "573001113333", ID: "a2", Body: "x"})
		h.inbound(cb.PhoneNumberID, waMsg{From: "573001112222", ID: "b1", Body: "x"}) // el mismo cliente escribe a B
		got := str(t, `SELECT string_agg(o.name || '#' || c.display_id, ',' ORDER BY o.name, c.display_id) FROM conversations c JOIN organizations o ON o.id = c.organization_id`)
		if got != "Org A#1,Org A#2,Org B#1" {
			t.Fatalf("numeración = %q", got)
		}
		// El mismo teléfono es un contacto distinto en cada organización.
		if n := count(t, `SELECT count(*) FROM contacts WHERE phone_e164 = '+573001112222'`); n != 2 {
			t.Fatalf("contactos con ese teléfono = %d, quería 1 por organización", n)
		}
	})

	t.Run("una conversación resuelta no se reabre: el mensaje siguiente abre una nueva", func(t *testing.T) {
		h, o, c := newOrg(t)
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w1", Body: "uno"})
		_, err := pool.Exec(t.Context(), `UPDATE conversations SET status = 'resolved' WHERE organization_id = $1`, o.ID)
		must(t, err)
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w2", Body: "de nuevo"})
		if got := str(t, `SELECT string_agg(display_id || ':' || status, ',' ORDER BY display_id) FROM conversations`); got != "1:resolved,2:open" {
			t.Fatalf("conversaciones = %q", got)
		}
	})

	t.Run("un mensaje reabre lo pendiente o pospuesto", func(t *testing.T) {
		h, o, c := newOrg(t)
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w1", Body: "uno"})
		for i, st := range []string{"pending", "snoozed"} {
			_, err := pool.Exec(t.Context(), `UPDATE conversations SET status = $2,
				snoozed_until = CASE WHEN $2 = 'snoozed' THEN now() + interval '1 day' END WHERE organization_id = $1`, o.ID, st)
			must(t, err)
			h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "wx" + str2(int64(i)), Body: "hola"})
			if got := str(t, `SELECT status || '|' || coalesce(snoozed_until::text, '-') FROM conversations`); got != "open|-" {
				t.Errorf("tras el mensaje (%s) = %q", st, got)
			}
		}
	})

	t.Run("un contacto bloqueado no abre conversaciones", func(t *testing.T) {
		h, o, c := newOrg(t)
		_, err := pool.Exec(t.Context(), `INSERT INTO contacts (organization_id, phone_e164, source, blocked) VALUES ($1, '+573001112222', 'manual', true)`, o.ID)
		must(t, err)
		want(t, h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w1", Body: "spam"}), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM conversations`) + count(t, `SELECT count(*) FROM messages`); n != 0 {
			t.Fatalf("un contacto bloqueado creó %d filas", n)
		}
	})

	t.Run("archivada no recibe; suspendida sí (no se pierden los mensajes de los clientes)", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		ca, cb := h.connect(adminA, a, "A"), h.connect(adminB, b, "B")
		want(t, h.do(http.MethodPost, platformOrgURL(a, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, h.do(http.MethodPost, platformOrgURL(b, "/archive"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		h.inbound(ca.PhoneNumberID, waMsg{From: "573001112222", ID: "a1", Body: "x"})
		h.inbound(cb.PhoneNumberID, waMsg{From: "573001112222", ID: "b1", Body: "x"})
		if got := str(t, `SELECT string_agg(o.name, ',') FROM messages m JOIN organizations o ON o.id = m.organization_id`); got != "Org A" {
			t.Fatalf("mensajes guardados para: %q", got)
		}
	})

	t.Run("tipos de mensaje: archivos, ubicación, botones, contactos, reacciones y no soportados", func(t *testing.T) {
		h, _, c := newOrg(t)
		send := func(id, typ string, extra map[string]any) {
			want(t, h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: "Ana", ID: id, Type: typ, Extra: extra}), http.StatusOK)
		}
		send("m-img", "image", map[string]any{"image": map[string]any{"id": "MEDIA1", "mime_type": "image/jpeg", "sha256": "abc", "caption": "mi foto"}})
		send("m-doc", "document", map[string]any{"document": map[string]any{"id": "MEDIA2", "mime_type": "application/pdf", "filename": "menu.pdf"}})
		send("m-loc", "location", map[string]any{"location": map[string]any{"latitude": 4.44, "longitude": -75.24, "name": "Mi casa", "address": "Cra 1"}})
		send("m-btn", "interactive", map[string]any{"interactive": map[string]any{"type": "button_reply", "button_reply": map[string]any{"id": "b1", "title": "Confirmar"}}})
		send("m-but", "button", map[string]any{"button": map[string]any{"text": "Sí", "payload": "SI"}})
		send("m-con", "contacts", map[string]any{"contacts": []map[string]any{{"name": map[string]any{"formatted_name": "Pepe"}}}})
		send("m-rea", "reaction", map[string]any{"reaction": map[string]any{"message_id": "wamid.X", "emoji": "👍"}})
		send("m-ord", "order", nil)

		got := str(t, `SELECT string_agg(wamid || '=' || kind || ':' || coalesce(body, ''), ' | ' ORDER BY wamid) FROM messages`)
		want1 := "m-btn=interactive:Confirmar | m-but=button:Sí | m-con=contacts:Contacto compartido | m-doc=document: | m-img=image:mi foto | m-loc=location:Mi casa | m-ord=unsupported:Mensaje no soportado"
		if got != want1 {
			t.Fatalf("mensajes:\n got %s\nwant %s", got, want1)
		}
		if n := count(t, `SELECT count(*) FROM messages WHERE wamid = 'm-rea'`); n != 0 {
			t.Error("una reacción no es un mensaje de la conversación")
		}
		if got := str(t, `SELECT kind || '|' || mime_type || '|' || wa_media_id || '|' || coalesce(caption, '') FROM message_attachments a WHERE wa_media_id = 'MEDIA1'`); got != "image|image/jpeg|MEDIA1|mi foto" {
			t.Errorf("adjunto = %q", got)
		}
		if got := str(t, `SELECT payload->>'latitude' || ',' || (payload->>'name') FROM messages WHERE wamid = 'm-loc'`); got != "4.44,Mi casa" {
			t.Errorf("ubicación = %q", got)
		}
		if got := str(t, `SELECT last_message_preview FROM conversations`); got == "" {
			t.Error("la vista previa no puede quedar vacía")
		}
	})

	t.Run("un mensaje sin texto muestra el tipo como vista previa", func(t *testing.T) {
		h, _, c := newOrg(t)
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "m-doc", Type: "document", Extra: map[string]any{"document": map[string]any{"id": "M", "mime_type": "application/pdf"}}})
		if got := str(t, `SELECT last_message_preview FROM conversations`); got != "[document]" {
			t.Errorf("vista previa = %q", got)
		}
	})

	t.Run("responder a un mensaje nuestro queda enlazado", func(t *testing.T) {
		h, o, c := newOrg(t)
		admin := h.user()
		h.join(admin, o, "employee")
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w1", Body: "hola", At: h.clock.Add(-time.Minute)})
		conv := convOf(t, o, "+573001112222")
		want(t, h.do(http.MethodPost, orgURL(o, "/conversations/"+conv+"/messages"), map[string]any{"body": "¿en qué te ayudo?"}, &admin), http.StatusAccepted)
		h.dispatchAll()
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w2", Body: "un pedido", ReplyToID: "wamid.OUT0001", At: h.clock})
		if n := count(t, `SELECT count(*) FROM messages WHERE wamid = 'w2' AND reply_to_message_id = (SELECT id FROM messages WHERE wamid = 'wamid.OUT0001')`); n != 1 {
			t.Fatal("la respuesta no quedó enlazada al mensaje que la originó")
		}
	})

	t.Run("un reloj adelantado no abre una ventana en el futuro", func(t *testing.T) {
		h, _, c := newOrg(t)
		future := h.clock.Add(3 * time.Hour)
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w1", Body: "x", At: future})
		got := str(t, `SELECT extract(epoch FROM last_customer_message_at)::bigint::text FROM conversations`)
		if got != str2(h.clock.Unix()) {
			t.Fatalf("last_customer_message_at = %s, quería el instante actual %d", got, h.clock.Unix())
		}
	})

	t.Run("una entrega atrasada nunca hace retroceder la ventana ni la vista previa", func(t *testing.T) {
		h, _, c := newOrg(t)
		recent, old := h.clock.Add(-1*time.Minute), h.clock.Add(-3*time.Hour)
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w-new", Body: "reciente", At: recent})
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "w-old", Body: "atrasado", At: old})
		if got := str(t, `SELECT extract(epoch FROM last_customer_message_at)::bigint::text || '|' || last_message_preview || '|' || unread_count FROM conversations`); got != str2(recent.Unix())+"|reciente|2" {
			t.Fatalf("conversación = %q", got)
		}
	})

	t.Run("datos raros no rompen el evento: número inválido, nombre y texto enormes", func(t *testing.T) {
		h, _, c := newOrg(t)
		want(t, h.inbound(c.PhoneNumberID, waMsg{From: "no-es-un-numero", ID: "bad1", Body: "x"}), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM contacts`); n != 0 {
			t.Errorf("un remitente inválido creó %d contactos", n)
		}
		want(t, h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: strings.Repeat("ñ", 400), ID: "big1", Body: strings.Repeat("é", 6000)}), http.StatusOK)
		if got := str(t, `SELECT char_length(name)::text || '|' || (SELECT char_length(body)::text FROM messages WHERE wamid = 'big1') FROM contacts`); got != "255|4096" {
			t.Errorf("recortes = %q", got)
		}
	})
}

func TestEntregasRepetidasYSimultaneas(t *testing.T) {
	t.Run("la misma entrega dos veces se procesa una sola", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		body := webhookBody(t, "WABA", c.PhoneNumberID, []waMsg{{From: "573001112222", ID: "w1", Body: "hola"}}, nil)
		for range 3 {
			want(t, h.postWebhook(body, testAppSecret), http.StatusOK)
		}
		if got := str(t, `SELECT (SELECT count(*) FROM webhook_events) || '|' || (SELECT count(*) FROM messages) || '|' || (SELECT unread_count FROM conversations)`); got != "1|1|1" {
			t.Fatalf("eventos|mensajes|no leídos = %q", got)
		}
		if n := count(t, `SELECT count(*) FROM webhook_events WHERE processed_at IS NOT NULL AND error IS NULL`); n != 1 {
			t.Errorf("el evento debía quedar procesado")
		}
	})

	t.Run("el mismo mensaje en otra entrega (Meta reagrupa) no se duplica", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		m := waMsg{From: "573001112222", ID: "w1", Body: "hola"}
		want(t, h.inbound(c.PhoneNumberID, m), http.StatusOK)
		want(t, h.inbound(c.PhoneNumberID, m, waMsg{From: "573001112222", ID: "w2", Body: "segundo"}), http.StatusOK) // distinta carga, w1 repetido
		if got := str(t, `SELECT (SELECT count(*) FROM messages) || '|' || (SELECT unread_count FROM conversations)`); got != "2|2" {
			t.Fatalf("mensajes|no leídos = %q", got)
		}
	})

	t.Run("entregas idénticas simultáneas: un solo mensaje y todas responden 200", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		body := webhookBody(t, "WABA", c.PhoneNumberID, []waMsg{{From: "573001112222", ID: "w1", Body: "hola"}}, nil)
		var wg sync.WaitGroup
		codes := make([]int, 8)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = h.postWebhook(body, testAppSecret).Code
			}()
		}
		wg.Wait()
		for i, code := range codes {
			if code != http.StatusOK {
				t.Errorf("entrega %d respondió %d", i, code)
			}
		}
		if got := str(t, `SELECT (SELECT count(*) FROM messages) || '|' || (SELECT count(*) FROM conversations) || '|' || (SELECT unread_count FROM conversations)`); got != "1|1|1" {
			t.Fatalf("mensajes|conversaciones|no leídos = %q", got)
		}
	})

	t.Run("mensajes simultáneos de un contacto NUEVO: una sola conversación", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		var wg sync.WaitGroup
		for i := range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: "Ana", ID: "w" + str2(int64(i)), Body: "hola"})
			}()
		}
		wg.Wait()
		if got := str(t, `SELECT (SELECT count(*) FROM conversations) || '|' || (SELECT count(*) FROM contacts) || '|' || (SELECT count(*) FROM messages) || '|' || (SELECT unread_count FROM conversations)`); got != "1|1|10|10" {
			t.Fatalf("conversaciones|contactos|mensajes|no leídos = %q", got)
		}
		if got := str(t, `SELECT display_id::text FROM conversations`); got != "1" {
			t.Errorf("display_id = %s", got)
		}
	})
}

func TestEstadosDeEntrega(t *testing.T) {
	// enviar deja un mensaje saliente entregado a Meta con wamid.OUT0001.
	setup := func(t *testing.T) (*harness, organization, connected, string) {
		h := newHarness(t)
		h.clock = time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: "Ana", ID: "w-in", Body: "hola", At: h.clock.Add(-time.Minute)})
		conv := convOf(t, o, "+573001112222")
		want(t, h.do(http.MethodPost, orgURL(o, "/conversations/"+conv+"/messages"), map[string]any{"body": "hola Ana"}, &admin), http.StatusAccepted)
		h.dispatchAll()
		return h, o, c, conv
	}
	t.Run("avanza sent → delivered → read con sus horas y guarda lo que Meta cobró", func(t *testing.T) {
		h, _, c, _ := setup(t)
		t1, t2 := h.clock.Add(time.Minute), h.clock.Add(2*time.Minute)
		want(t, h.status(c.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "delivered", At: t1, Category: "service"}), http.StatusOK)
		want(t, h.status(c.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "read", At: t2}), http.StatusOK)
		got := str(t, `SELECT status || '|' || extract(epoch FROM delivered_at)::bigint || '|' || extract(epoch FROM read_at)::bigint || '|' || pricing_category || '|' || billable FROM messages WHERE wamid = 'wamid.OUT0001'`)
		if got != "read|"+str2(t1.Unix())+"|"+str2(t2.Unix())+"|service|true" {
			t.Fatalf("mensaje = %q", got)
		}
	})

	t.Run("un estado viejo nunca hace retroceder a uno más nuevo (Meta no garantiza el orden)", func(t *testing.T) {
		h, _, c, _ := setup(t)
		h.status(c.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "read"})
		h.status(c.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "delivered"})
		h.status(c.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "sent"})
		if got := str(t, `SELECT status FROM messages WHERE wamid = 'wamid.OUT0001'`); got != "read" {
			t.Fatalf("estado = %q", got)
		}
		// "read" sin "delivered" previo completa la hora de entrega.
		if n := count(t, `SELECT count(*) FROM messages WHERE wamid = 'wamid.OUT0001' AND delivered_at IS NOT NULL AND sent_at IS NOT NULL`); n != 1 {
			t.Error("read debía completar delivered_at y sent_at")
		}
	})

	t.Run("failed guarda el código y el motivo; no pisa a uno ya entregado", func(t *testing.T) {
		h, _, c, _ := setup(t)
		h.status(c.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "failed", Code: 131026, Detail: "Message undeliverable"})
		if got := str(t, `SELECT status || '|' || error_code || '|' || error_detail FROM messages WHERE wamid = 'wamid.OUT0001'`); got != "failed|131026|Message undeliverable" {
			t.Fatalf("mensaje = %q", got)
		}
		h2, _, c2, _ := setup(t)
		h2.status(c2.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "delivered"})
		h2.status(c2.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "failed", Code: 1, Detail: "tarde"})
		if got := str(t, `SELECT status FROM messages WHERE wamid = 'wamid.OUT0001'`); got != "delivered" {
			t.Fatalf("un failed tardío pisó a delivered: %q", got)
		}
	})

	t.Run("un estado de un mensaje de OTRA bandeja no lo toca (mismo wamid)", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		ca, cb := h.connect(adminA, a, "A"), h.connect(adminB, b, "B")
		h.inbound(ca.PhoneNumberID, waMsg{From: "573001112222", ID: "wi", Body: "x"})
		conv := convOf(t, a, "+573001112222")
		want(t, h.do(http.MethodPost, orgURL(a, "/conversations/"+conv+"/messages"), map[string]any{"body": "hola"}, &adminA), http.StatusAccepted)
		h.dispatchAll()
		// B recibe un estado con el wamid del mensaje de A.
		h.status(cb.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "read"})
		if got := str(t, `SELECT status FROM messages WHERE wamid = 'wamid.OUT0001'`); got != "sent" {
			t.Fatalf("un evento de la bandeja de B cambió un mensaje de A: %q", got)
		}
	})

	t.Run("un estado que llega ANTES del wamid se aplica cuando el envío lo registra", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "wi", Body: "x"})
		conv := convOf(t, o, "+573001112222")
		want(t, h.do(http.MethodPost, orgURL(o, "/conversations/"+conv+"/messages"), map[string]any{"body": "hola"}, &admin), http.StatusAccepted)
		// El webhook gana la carrera: el mensaje aún está en cola y no tiene wamid.
		want(t, h.status(c.PhoneNumberID, waStatus{ID: "wamid.OUT0001", Status: "delivered"}), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM unmatched_statuses WHERE wamid = 'wamid.OUT0001'`); n != 1 {
			t.Fatalf("estados sin mensaje = %d", n)
		}
		h.dispatchAll()
		if got := str(t, `SELECT status FROM messages WHERE wamid = 'wamid.OUT0001'`); got != "delivered" {
			t.Fatalf("estado = %q, quería delivered (se perdió el estado adelantado)", got)
		}
		if n := count(t, `SELECT count(*) FROM unmatched_statuses`); n != 0 {
			t.Errorf("el estado pendiente no se limpió: %d", n)
		}
	})

	t.Run("los estados sin mensaje se podan al día siguiente", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		h.status(c.PhoneNumberID, waStatus{ID: "wamid.NUNCA", Status: "delivered"})
		if n := count(t, `SELECT count(*) FROM unmatched_statuses`); n != 1 {
			t.Fatalf("estados sin mensaje = %d", n)
		}
		_, err := pool.Exec(t.Context(), `UPDATE unmatched_statuses SET created_at = now() - interval '2 days'; UPDATE webhook_events SET received_at = now() - interval '31 days'`)
		must(t, err)
		h.workers.WhatsApp.Prune(t.Context())
		if n := count(t, `SELECT count(*) FROM unmatched_statuses`) + count(t, `SELECT count(*) FROM webhook_events`); n != 0 {
			t.Fatalf("no se podó lo viejo: %d filas", n)
		}
	})

	t.Run("otros campos y estados desconocidos se guardan crudos sin procesar", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		body := []byte(`{"object":"whatsapp_business_account","entry":[{"id":"W","changes":[{"field":"account_update","value":{"event":"VERIFIED_ACCOUNT"}}]}]}`)
		want(t, h.postWebhook(body, testAppSecret), http.StatusOK)
		want(t, h.status(c.PhoneNumberID, waStatus{ID: "wamid.X", Status: "deleted"}), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM unmatched_statuses`); n != 0 {
			t.Errorf("un estado 'deleted' no debía guardarse: %d", n)
		}
		if n := count(t, `SELECT count(*) FROM webhook_events`); n != 2 {
			t.Errorf("eventos = %d", n)
		}
	})
}
