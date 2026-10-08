// Package epayco implementa payments.Gateway contra ePayco (https://epayco.co), la pasarela del
// modo B de facturación (docs/ecommerce.md §0/§3): el cliente paga, ePayco liquida a Domicilia.
//
// Usa el checkout por redirección de ePayco ("Onepage Checkout" clásico): arma una URL firmada
// con la llave pública del comercio, sin llamar a ningún endpoint de ePayco — nada que pueda
// fallar por una caída de la pasarela al confirmar un pedido. Por eso payments.Service.Initiate
// no necesita cola de reintento para esta parte (a diferencia de enviar un WhatsApp, que sí llama
// a Meta): si algo falla aquí es en nuestro propio código, no en la red.
//
// VERIFICAR ANTES DE PRODUCCIÓN — mismo tipo de advertencia que ya existe para Meta (confirmar si
// Domicilia puede ser pagador): esto se escribió contra la documentación pública de ePayco
// (checkout por redirección + confirmación firmada con SHA-256 sobre
// p_cust_id_cliente^p_key^x_ref_payco^x_transaction_id^x_amount^x_currency_code), NO contra una
// cuenta de pruebas real. Antes de mover dinero real: confirmar con una cuenta sandbox de ePayco
// la URL exacta del checkout, los nombres de los parámetros y el orden de la firma.
package epayco

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/payments"
	"github.com/domicilia/domicilia-core/internal/pricing"
)

// checkoutBaseURL es el punto de entrada del checkout por redirección. VERIFICAR (ver el
// comentario del paquete).
const checkoutBaseURL = "https://checkout.epayco.co/checkout.php"

// transactionAccepted es el valor de x_transaction_state que ePayco documenta para un pago
// aprobado. Cualquier otro (Rechazada, Pendiente, Fallida...) se trata como no exitoso.
const transactionAccepted = "Aceptada"

// Config son las credenciales del comercio en ePayco (CORE_EPAYCO_*, ver config.Config).
type Config struct {
	PublicKey  string
	PrivateKey string
	CustomerID string // p_cust_id_cliente
	TestMode   bool
	// ApifyURL es la API que crea las sesiones del checkout onpage (vacío = DefaultApifyURL). Las
	// pruebas la apuntan a un servidor falso.
	ApifyURL string
}

// Gateway implementa payments.Gateway y payments.SessionGateway contra ePayco.
type Gateway struct {
	cfg    Config
	client *http.Client // nil = cliente con timeout de 10 s
}

// New crea el gateway. Panic si faltan credenciales: es un error de programación llamar a esto
// sin haber comprobado antes que hay pasarela configurada (ver app.newPaymentsParts).
func New(cfg Config) *Gateway {
	if cfg.PublicKey == "" || cfg.PrivateKey == "" || cfg.CustomerID == "" {
		panic("epayco: faltan credenciales")
	}
	return &Gateway{cfg: cfg}
}

// franchiseMethod traduce x_franchise de ePayco al medio de pago declarado. VERIFICAR los códigos
// contra el sandbox: estos son los que su documentación y sus plugins usan (PSE, NEQUI/NQ,
// DAVIPLATA/DP); cualquier otro se toma como tarjeta. Una tarjeta internacional NO se distingue de
// una nacional por la franquicia (docs/pagos.md §7.6).
func franchiseMethod(f string) pricing.Method {
	switch strings.ToUpper(strings.TrimSpace(f)) {
	case "":
		return ""
	case "PSE":
		return pricing.MethodPSE
	case "NEQUI", "NQ", "DAVIPLATA", "DP":
		return pricing.MethodWallet
	default:
		return pricing.MethodCard
	}
}

// decimalToCents pasa "55980.00" o "55980" a centavos. 0 si no se puede leer.
func decimalToCents(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0
	}
	return int64(math.Round(f * 100))
}

// Name identifica la pasarela — se guarda en payments.gateway.
func (g *Gateway) Name() string { return "epayco" }

// BuildCheckout arma la URL del checkout. Sin llamada de red: ver el comentario del paquete.
func (g *Gateway) BuildCheckout(req payments.ChargeRequest) (string, error) {
	if req.AmountCents <= 0 {
		return "", fmt.Errorf("epayco: amount_cents debe ser positivo")
	}
	q := url.Values{}
	q.Set("p_cust_id_cliente", g.cfg.CustomerID)
	q.Set("p_key", g.cfg.PublicKey)
	q.Set("p_id_invoice", req.PaymentID.String())
	q.Set("p_description", req.Description)
	q.Set("p_amount", centsToDecimal(req.AmountCents))
	q.Set("p_currency_code", req.Currency)
	q.Set("p_email", req.CustomerEmail)
	q.Set("p_url_response", req.ResponseURL)
	q.Set("p_url_confirmation", req.ConfirmationURL)
	if req.TestMode || g.cfg.TestMode {
		q.Set("p_test_request", "TRUE")
	}
	return checkoutBaseURL + "?" + q.Encode(), nil
}

// centsToDecimal pasa centavos a la notación decimal que espera ePayco ("15000" → "150.00").
func centsToDecimal(cents int32) string {
	return strconv.FormatFloat(float64(cents)/100, 'f', 2, 64)
}

// signature calcula la firma que ePayco espera para su webhook de confirmación. VERIFICAR el
// orden y los campos contra la documentación vigente antes de producción (ver el comentario del
// paquete) — esto es lo que su integración clásica documenta desde hace años, pero una pasarela
// de pago puede cambiarlo.
func (g *Gateway) signature(refPayco, transactionID, amount, currency string) string {
	raw := g.cfg.CustomerID + "^" + g.cfg.PrivateKey + "^" + refPayco + "^" + transactionID + "^" + amount + "^" + currency
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ParseWebhook valida la confirmación de ePayco (form-encoded, no JSON) y devuelve el evento.
func (g *Gateway) ParseWebhook(r *http.Request) (payments.WebhookEvent, error) {
	if err := r.ParseForm(); err != nil {
		return payments.WebhookEvent{}, fmt.Errorf("%w: %w", payments.ErrBadPayload, err)
	}
	form := r.PostForm
	refPayco := form.Get("x_ref_payco")
	transactionID := form.Get("x_transaction_id")
	invoice := form.Get("x_id_invoice")
	amount := form.Get("x_amount")
	currency := form.Get("x_currency_code")
	state := form.Get("x_transaction_state")
	signature := form.Get("x_signature")
	if refPayco == "" || transactionID == "" || invoice == "" || signature == "" {
		return payments.WebhookEvent{}, fmt.Errorf("%w: faltan campos obligatorios", payments.ErrBadPayload)
	}
	paymentID, err := uuid.Parse(invoice)
	if err != nil {
		return payments.WebhookEvent{}, fmt.Errorf("%w: x_id_invoice no es un id válido", payments.ErrBadPayload)
	}
	want := g.signature(refPayco, transactionID, amount, currency)
	if !hmac.Equal([]byte(signature), []byte(want)) {
		return payments.WebhookEvent{}, payments.ErrBadSignature
	}

	payload, err := json.Marshal(form)
	if err != nil {
		return payments.WebhookEvent{}, fmt.Errorf("%w: %w", payments.ErrBadPayload, err)
	}
	succeeded := state == transactionAccepted
	var failureReason string
	if !succeeded {
		failureReason = "ePayco: " + state
	}
	franchise := form.Get("x_franchise")
	return payments.WebhookEvent{
		// Dedup por transacción de ePayco, no por invoice: un reintento de pago sobre el mismo
		// pedido (otro x_ref_payco) es un evento distinto, no un duplicado.
		EventKey:      refPayco + ":" + transactionID,
		PaymentID:     paymentID,
		Succeeded:     succeeded,
		FailureReason: failureReason,
		RawPayload:    payload,
		AmountCents:   decimalToCents(amount),
		MethodUsed:    franchiseMethod(franchise),
		RawMethod:     franchise,
	}, nil
}
