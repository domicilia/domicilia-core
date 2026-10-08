package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

const (
	testPublicURL        = "http://localhost:8080"
	testEpaycoPublicKey  = "clave-publica-de-pruebas"
	testEpaycoPrivateKey = "clave-privada-de-pruebas"
	testEpaycoCustomerID = "0000000000"
)

// epaycoSignature calcula la firma como lo hace internal/epayco: sirve para armar webhooks de
// prueba válidos, y para armar uno inválido a propósito (con otra llave o campos distintos).
func epaycoSignature(refPayco, transactionID, amount, currency string) string {
	raw := testEpaycoCustomerID + "^" + testEpaycoPrivateKey + "^" + refPayco + "^" + transactionID + "^" + amount + "^" + currency
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// postPaymentWebhook simula la confirmación de ePayco (form-encoded, no JSON). signature nil
// firma de verdad con la llave de pruebas; una firma explícita sirve para probar el rechazo.
func (h *harness) postPaymentWebhook(paymentID, refPayco, transactionID, amount, state string, signature *string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.postPaymentWebhookWith(paymentID, refPayco, transactionID, amount, state, signature, nil)
}

// postPaymentWebhookWith es postPaymentWebhook con campos extra (p. ej. x_franchise).
func (h *harness) postPaymentWebhookWith(paymentID, refPayco, transactionID, amount, state string, signature *string, extra map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	sig := epaycoSignature(refPayco, transactionID, amount, "COP")
	if signature != nil {
		sig = *signature
	}
	form := url.Values{
		"x_ref_payco":         {refPayco},
		"x_transaction_id":    {transactionID},
		"x_id_invoice":        {paymentID},
		"x_amount":            {amount},
		"x_currency_code":     {"COP"},
		"x_transaction_state": {state},
		"x_signature":         {sig},
	}
	for k, v := range extra {
		form.Set(k, v)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/payments", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// fakeEpayco imita la API de sesiones de ePayco (apify): login con Basic auth y creación de la
// sesión del checkout onpage. Guarda cada sesión pedida para que las pruebas revisen qué se
// mandó (monto, split...). fail = true simula una caída de ePayco.
type fakeEpayco struct {
	srv      *httptest.Server
	mu       sync.Mutex
	sessions []map[string]any
	fail     bool
}

func newFakeEpayco(t *testing.T) *fakeEpayco {
	f := &fakeEpayco{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fail := f.fail
		f.mu.Unlock()
		if fail {
			http.Error(w, `{"error":"caído"}`, http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/login":
			user, pass, ok := r.BasicAuth()
			if !ok || user != testEpaycoPublicKey || pass != testEpaycoPrivateKey {
				http.Error(w, `{"error":"credenciales"}`, http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "token-de-pruebas"})
		case "/payment/session/create":
			if r.Header.Get("Authorization") != "Bearer token-de-pruebas" {
				http.Error(w, `{"error":"token"}`, http.StatusUnauthorized)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.sessions = append(f.sessions, body)
			n := len(f.sessions)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"sessionId": "sesion-" + string(rune('0'+n))}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEpayco) lastSession() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return nil
	}
	return f.sessions[len(f.sessions)-1]
}

func (f *fakeEpayco) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}
