package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/payments", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}
