// Package payments cobra un pedido: crea el intento de cobro (Payment), arma la URL de pago y
// procesa el webhook de la pasarela con idempotencia. Diseño en docs/ecommerce.md §3. Tercera
// pieza del módulo de e-commerce (catalog → orders → payments → promotions).
//
// Depende de internal/orders (un pago siempre es de un pedido), nunca al revés — orders no sabe
// que payments existe. La dependencia es angosta a propósito: OrdersGateway declara solo los tres
// métodos que este paquete necesita, y *orders.Service los satisface sin saberlo.
//
// ePayco (paquete epayco) es la primera y única pasarela — ver Gateway.
package payments

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Errores del repositorio y de Gateway.ParseWebhook que el servicio y el handler traducen a
// errores de negocio.
var (
	ErrNotFound     = errors.New("payments: no encontrado")
	ErrBadSignature = errors.New("payments: firma inválida")
	ErrBadPayload   = errors.New("payments: carga inválida")
)

// Status es el estado de un intento de cobro.
type Status string

// Estados. Solo avanza desde pending — ver db/queries/payments.sql (SettlePayment).
const (
	StatusPending   Status = "pending"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Payment es un intento de cobro de un pedido. Un pedido puede tener más de uno (el primero
// falla, el cliente reintenta con orders.OpRetryPayment y luego pide un cobro nuevo).
type Payment struct {
	ID             uuid.UUID `json:"id"`
	OrderID        uuid.UUID `json:"order_id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Status         Status    `json:"status"`
	AmountCents    int32     `json:"amount_cents"`
	Currency       string    `json:"currency"`
	Gateway        string    `json:"gateway"`
	CheckoutURL    *string   `json:"checkout_url"`
	FailureReason  *string   `json:"failure_reason"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ChargeRequest son los datos para armar el cobro de un pedido.
type ChargeRequest struct {
	// PaymentID es NUESTRA referencia — se le manda a la pasarela y ella la devuelve tal cual en
	// su webhook (p_id_invoice/x_id_invoice en ePayco). Así el webhook no depende de que la
	// pasarela nos haya asignado una referencia propia a tiempo.
	PaymentID       uuid.UUID
	AmountCents     int32
	Currency        string
	Description     string
	CustomerEmail   string
	ResponseURL     string // adonde vuelve el navegador del cliente tras pagar
	ConfirmationURL string // nuestro webhook, servidor a servidor
	TestMode        bool
}

// WebhookEvent es un evento YA VALIDADO (firma/autenticidad comprobada con las credenciales de la
// pasarela) que reporta el resultado de un cobro. Un pago rechazado es un WebhookEvent válido con
// Succeeded = false — eso no es un error de análisis, es información real.
type WebhookEvent struct {
	// EventKey identifica este evento para no aplicarlo dos veces (webhook_events, mismo patrón
	// que WhatsApp): normalmente gateway + referencia de la transacción de la pasarela.
	EventKey      string
	PaymentID     uuid.UUID
	Succeeded     bool
	FailureReason string
	RawPayload    []byte
}

// Gateway es la pasarela de pago. ePayco (paquete epayco) es la primera implementación.
type Gateway interface {
	// Name identifica la pasarela ("epayco") — se guarda en payments.gateway.
	Name() string
	// BuildCheckout arma la URL a la que se manda al cliente a pagar. Para pasarelas por
	// redirección (ePayco y la mayoría de las latinoamericanas) es solo construir una URL firmada
	// con la llave pública: no hace ninguna llamada de red, nada que falle por una caída de la
	// pasarela — por eso Initiate no necesita cola de reintento (a diferencia del envío de
	// WhatsApp, que sí llama a Meta).
	BuildCheckout(req ChargeRequest) (checkoutURL string, err error)
	// ParseWebhook valida la autenticidad del webhook con las credenciales de la pasarela y
	// devuelve el evento. Un error significa "no confío en este webhook", nunca "el pago falló".
	ParseWebhook(r *http.Request) (WebhookEvent, error)
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	Insert(ctx context.Context, orderID, orgID uuid.UUID, amountCents int32, currency, gateway string) (Payment, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Payment, error)
	// GetByID lo usa el webhook: solo conoce el id que le mandamos como referencia, no la
	// organización — mismo motivo que orders.Repository.GetOrderByID.
	GetByID(ctx context.Context, id uuid.UUID) (Payment, error)
	ListByOrder(ctx context.Context, orgID, orderID uuid.UUID) ([]Payment, error)
	SetCheckoutURL(ctx context.Context, id uuid.UUID, url string) (Payment, error)
	// Settle aplica el resultado final. Devuelve ErrNotFound si el pago no existe O si ya no
	// estaba pending (un webhook repetido no debe poder pisar un estado final ya aplicado) — el
	// servicio distingue ambos casos leyendo el pago aparte antes de decidir cómo responder.
	Settle(ctx context.Context, id uuid.UUID, status Status, failureReason *string) (Payment, error)
	// RecordEvent inserta el evento del webhook. applied = false si (source, eventKey) ya
	// existía: el servicio no debe volver a aplicar sus efectos, pero sí responder 200 — la
	// pasarela no tiene la culpa de reintentar una entrega que ya llegó antes.
	RecordEvent(ctx context.Context, source, eventKey string, payload []byte) (applied bool, err error)
}
