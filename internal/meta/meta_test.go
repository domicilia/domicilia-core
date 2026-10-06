package meta_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/domicilia/domicilia-core/internal/meta"
)

const secretToken = "EAAG-token-secreto-1234567890"

type recorded struct {
	Method, Path, Query, Auth, ContentType string
	Body                                   map[string]any
}

// fakeGraph es una Graph API de mentira: guarda lo que recibe y responde lo que se le pide.
func fakeGraph(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec recorded)) (*meta.Client, *[]recorded) {
	t.Helper()
	var calls []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), ContentType: r.Header.Get("Content-Type")}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &rec.Body)
		}
		calls = append(calls, rec)
		handler(w, r, rec)
	}))
	t.Cleanup(srv.Close)
	return meta.New(meta.Config{BaseURL: srv.URL, Version: "v99.0"}), &calls
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestSendTextArmaLaPeticionYDevuelveElWamid(t *testing.T) {
	c, calls := fakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) {
		writeJSON(w, 200, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.ABC"}]}`)
	})
	id, err := c.SendText(context.Background(), secretToken, "PN123", meta.TextMessage{To: "573001234567", Body: "Hola ñandú", ReplyToID: "wamid.PREV"})
	if err != nil || id != "wamid.ABC" {
		t.Fatalf("SendText = %q, %v", id, err)
	}
	got := (*calls)[0]
	if got.Method != http.MethodPost || got.Path != "/v99.0/PN123/messages" {
		t.Errorf("petición = %s %s", got.Method, got.Path)
	}
	if got.Auth != "Bearer "+secretToken || got.ContentType != "application/json" {
		t.Errorf("cabeceras = %q / %q", got.Auth, got.ContentType)
	}
	if strings.Contains(got.Query, "EAAG") || strings.Contains(got.Path, "EAAG") {
		t.Error("el token no debe ir en la URL")
	}
	if got.Body["to"] != "573001234567" || got.Body["type"] != "text" || got.Body["messaging_product"] != "whatsapp" {
		t.Errorf("cuerpo = %v", got.Body)
	}
	if text := got.Body["text"].(map[string]any); text["body"] != "Hola ñandú" {
		t.Errorf("texto = %v", text)
	}
	if ctxObj := got.Body["context"].(map[string]any); ctxObj["message_id"] != "wamid.PREV" {
		t.Errorf("context = %v", ctxObj)
	}
}

func TestSendTextSinRespuestaEnContexto(t *testing.T) {
	c, calls := fakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) {
		writeJSON(w, 200, `{"messages":[{"id":"w1"}]}`)
	})
	if _, err := c.SendText(context.Background(), "t", "PN", meta.TextMessage{To: "57", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, has := (*calls)[0].Body["context"]; has {
		t.Error("sin ReplyToID no debe enviarse context")
	}
}

func TestSendTextRechazaUnaRespuestaSinId(t *testing.T) {
	c, _ := fakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) { writeJSON(w, 200, `{"messages":[]}`) })
	if _, err := c.SendText(context.Background(), "t", "PN", meta.TextMessage{To: "57", Body: "x"}); err == nil {
		t.Fatal("una respuesta sin wamid no es un envío confirmado")
	}
}

func TestClasificaLosErroresDeMeta(t *testing.T) {
	tests := []struct {
		name                        string
		status                      int
		body                        string
		retryable, auth, windowShut bool
	}{
		{"token inválido", 401, `{"error":{"message":"Invalid OAuth access token","code":190,"fbtrace_id":"T1"}}`, false, true, false},
		{"ventana cerrada", 400, `{"error":{"message":"Re-engagement message","code":131047}}`, false, false, true},
		{"límite de ritmo del número", 429, `{"error":{"message":"Throughput","code":130429}}`, true, false, false},
		{"fallo temporal de Meta", 500, `{"error":{"message":"x","code":131000}}`, true, false, false},
		{"caída sin cuerpo JSON", 502, `<html>Bad Gateway</html>`, true, false, false},
		{"destinatario sin WhatsApp", 400, `{"error":{"message":"not in allowed list","code":131026}}`, false, false, false},
		{"parámetro inválido", 400, `{"error":{"message":"bad param","code":100}}`, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) { writeJSON(w, tc.status, tc.body) })
			_, err := c.SendText(context.Background(), secretToken, "PN", meta.TextMessage{To: "57", Body: "x"})
			var ae *meta.APIError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, quería *APIError", err)
			}
			if ae.Retryable() != tc.retryable || ae.AuthFailed() != tc.auth || ae.WindowClosed() != tc.windowShut {
				t.Errorf("retryable=%v auth=%v window=%v, quería %v/%v/%v", ae.Retryable(), ae.AuthFailed(), ae.WindowClosed(), tc.retryable, tc.auth, tc.windowShut)
			}
			if strings.Contains(err.Error(), secretToken) {
				t.Fatal("el error contiene el token")
			}
		})
	}
}

func TestUnErrorDeRedEsReintentableYNoFiltraElToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, 200, `{}`)
	}))
	defer srv.Close()
	c := meta.New(meta.Config{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 50 * time.Millisecond}})
	_, err := c.SendText(context.Background(), secretToken, "PN", meta.TextMessage{To: "57", Body: "x"})
	var te *meta.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, quería *TransportError", err)
	}
	if strings.Contains(err.Error(), secretToken) || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("el error de red arrastra la URL o el token: %v", err)
	}
}

func TestListPhoneNumbersPaginaYValida(t *testing.T) {
	c, calls := fakeGraph(t, func(w http.ResponseWriter, r *http.Request, _ recorded) {
		if r.URL.Query().Get("after") == "" {
			writeJSON(w, 200, `{"data":[{"id":"1","display_phone_number":"+57 300 111 1111","verified_name":"Uno","quality_rating":"GREEN","messaging_limit_tier":"TIER_1K"}],"paging":{"cursors":{"after":"CUR"},"next":"https://x"}}`)
			return
		}
		writeJSON(w, 200, `{"data":[{"id":"2","display_phone_number":"+57 300 222 2222","verified_name":"Dos"}]}`)
	})
	nums, err := c.ListPhoneNumbers(context.Background(), secretToken, "WABA9")
	if err != nil || len(nums) != 2 || nums[0].QualityRating != "GREEN" || nums[1].ID != "2" {
		t.Fatalf("números = %+v, %v", nums, err)
	}
	if len(*calls) != 2 || (*calls)[0].Path != "/v99.0/WABA9/phone_numbers" || !strings.Contains((*calls)[1].Query, "after=CUR") {
		t.Errorf("llamadas = %+v", *calls)
	}
}

func TestListPhoneNumbersConTokenInvalidoDevuelveAPIError(t *testing.T) {
	c, _ := fakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) {
		writeJSON(w, 400, `{"error":{"message":"Invalid OAuth access token.","code":190}}`)
	})
	_, err := c.ListPhoneNumbers(context.Background(), "malo", "WABA")
	var ae *meta.APIError
	if !errors.As(err, &ae) || !ae.AuthFailed() {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentificadoresConCaracteresEspecialesNoRompenLaRuta(t *testing.T) {
	c, calls := fakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) { writeJSON(w, 200, `{"data":[]}`) })
	if _, err := c.ListPhoneNumbers(context.Background(), "t", "../evil?x=1"); err != nil {
		t.Fatal(err)
	}
	// En el cable el id va escapado dentro de UN solo segmento de la ruta.
	if got := (*calls)[0].Path; got != "/v99.0/..%2Fevil%3Fx=1/phone_numbers" {
		t.Errorf("el id no quedó escapado: %q", got)
	}
}

func TestFirmaDelWebhook(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	const secret = "app-secret"
	good := meta.Sign(secret, body)
	if !meta.VerifySignature(secret, body, good) {
		t.Fatal("una firma correcta debía validar")
	}
	for name, tc := range map[string]struct {
		secret string
		body   []byte
		header string
	}{
		"secreto distinto":        {"otro", body, good},
		"cuerpo alterado":         {secret, []byte(`{"object":"x"}`), good},
		"un byte de más":          {secret, append(append([]byte{}, body...), ' '), good},
		"sin cabecera":            {secret, body, ""},
		"sin prefijo sha256=":     {secret, body, strings.TrimPrefix(good, "sha256=")},
		"prefijo de otro hash":    {secret, body, "sha1=" + strings.TrimPrefix(good, "sha256=")},
		"hex inválido":            {secret, body, "sha256=zzzz"},
		"firma truncada":          {secret, body, good[:len(good)-2]},
		"secreto vacío no valida": {"", body, meta.Sign("", body)},
		"firma vacía":             {secret, body, "sha256="},
		"mayúsculas del prefijo":  {secret, body, "SHA256=" + strings.TrimPrefix(good, "sha256=")},
	} {
		if meta.VerifySignature(tc.secret, tc.body, tc.header) {
			t.Errorf("%s: no debía validar", name)
		}
	}
}

func TestParseWebhook(t *testing.T) {
	const payload = `{"object":"whatsapp_business_account","entry":[{"id":"WABA1","changes":[{"field":"messages","value":{
		"messaging_product":"whatsapp","metadata":{"display_phone_number":"573001112222","phone_number_id":"PN1"},
		"contacts":[{"profile":{"name":"María"},"wa_id":"573009998888"}],
		"messages":[{"from":"573009998888","id":"wamid.IN1","timestamp":"1790000000","type":"text","text":{"body":"Hola"}},
		            {"from":"573009998888","id":"wamid.IN2","timestamp":"1790000001","type":"image","image":{"id":"MED1","mime_type":"image/jpeg","caption":"foto"}}],
		"statuses":[{"id":"wamid.OUT1","status":"failed","timestamp":"1790000002","recipient_id":"573009998888",
		             "errors":[{"code":131047,"title":"Re-engagement","message":"fuera de ventana","error_data":{"details":"más de 24 h"}}],
		             "pricing":{"billable":true,"pricing_model":"PMP","category":"service"}}]}}]}]}`
	w, err := meta.ParseWebhook([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	v := w.Entry[0].Changes[0].Value
	if v.Metadata.PhoneNumberID != "PN1" || v.Contacts[0].Profile.Name != "María" || len(v.Messages) != 2 {
		t.Fatalf("valor = %+v", v)
	}
	if v.Messages[0].Text.Body != "Hola" || v.Messages[1].Image.ID != "MED1" || v.Messages[1].Image.Caption != "foto" {
		t.Errorf("mensajes = %+v", v.Messages)
	}
	st := v.Statuses[0]
	if st.Status != "failed" || st.Errors[0].Code != 131047 || st.Errors[0].Detail() != "más de 24 h" || st.Pricing == nil || st.Pricing.Category != "service" {
		t.Errorf("estado = %+v", st)
	}
	if got := meta.Time(v.Messages[0].Timestamp, time.Time{}); got.Unix() != 1790000000 {
		t.Errorf("Time = %v", got)
	}
}

func TestParseWebhookRechazaLoQueNoEsWhatsApp(t *testing.T) {
	for name, body := range map[string]string{
		"otro producto": `{"object":"page","entry":[]}`, "no es JSON": `hola`, "vacío": ``, "sin objeto": `{}`,
	} {
		if _, err := meta.ParseWebhook([]byte(body)); err == nil {
			t.Errorf("%s: debía rechazarse", name)
		}
	}
}

func TestTimeYE164(t *testing.T) {
	fb := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, bad := range []string{"", "abc", "0", "-5"} {
		if got := meta.Time(bad, fb); !got.Equal(fb) {
			t.Errorf("Time(%q) = %v, quería el respaldo", bad, got)
		}
	}
	for in, want := range map[string]string{"573001234567": "+573001234567", "+573001234567": "+573001234567", " 57300 ": "+57300", "": ""} {
		if got := meta.E164(in); got != want {
			t.Errorf("E164(%q) = %q, quería %q", in, got, want)
		}
	}
}
