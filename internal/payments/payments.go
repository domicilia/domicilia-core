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
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/pricing"
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
	// Medio que declaró el cliente, base (lo que debe quedar tras la pasarela), costo de
	// transacción que aceptó, plan con que se calculó y el desglose completo (docs/pagos.md §8).
	Method              *string         `json:"method"`
	BaseCents           *int32          `json:"base_cents"`
	TransactionFeeCents int32           `json:"transaction_fee_cents"`
	GatewayPlanCode     *string         `json:"gateway_plan_code"`
	Breakdown           json.RawMessage `json:"breakdown"`
	// SessionID es la sesión del checkout onpage de ePayco (checkout-v2).
	SessionID *string `json:"session_id"`
	// MethodUsed es lo que la pasarela dice que se usó (x_franchise); MethodMismatch, si no
	// coincide con Method — para conciliación (docs/pagos.md §7.6).
	MethodUsed     *string   `json:"method_used"`
	MethodMismatch bool      `json:"method_mismatch"`
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
	// Method es el medio declarado por el cliente (va como extra para trazabilidad).
	Method pricing.Method
	// Split, si no es nil, reparte el cobro (ePayco split payments). nil = todo a la cuenta de
	// la plataforma.
	Split *SplitRequest
}

// SplitRequest es el reparto automático del cobro entre receptores (docs/pagos.md §8). La
// plataforma es el receptor principal: recibe lo que no se asigna a otros.
type SplitRequest struct {
	Receivers []SplitReceiver
}

// SplitReceiver es un receptor secundario (el restaurante).
type SplitReceiver struct {
	MerchantID  string // P_CUST_ID_CLIENTE del receptor en ePayco
	AmountCents int64
}

// SessionGateway es una pasarela que abre su checkout DENTRO de nuestra página (checkout onpage):
// el servidor crea una sesión y el frontend la abre con el script de la pasarela. ePayco
// checkout-v2 la exige (login + payment/session/create desde el servidor).
type SessionGateway interface {
	CreateSession(ctx context.Context, req ChargeRequest) (sessionID string, err error)
	// TestMode dice si las sesiones se crean en modo de pruebas (el frontend lo necesita).
	TestMode() bool
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
	// AmountCents es el monto que la pasarela dice que cobró (0 = no lo informó). Si no coincide
	// con el del pago, el pago NO se da por bueno.
	AmountCents int64
	// MethodUsed es el medio que la pasarela dice que se usó, ya traducido a pricing.Method ("" si
	// no se pudo saber).
	MethodUsed pricing.Method
	// RawMethod es lo que mandó la pasarela tal cual (p. ej. x_franchise), para auditoría.
	RawMethod string
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
// NewPayment son los datos de un intento de cobro nuevo.
type NewPayment struct {
	OrderID, OrganizationID uuid.UUID
	AmountCents             int32
	Currency, Gateway       string
	Method                  pricing.Method
	BaseCents               int32
	TransactionFeeCents     int32
	GatewayPlanCode         string
	Breakdown               []byte
}

type Repository interface {
	Insert(ctx context.Context, n NewPayment) (Payment, error)
	SetSession(ctx context.Context, id uuid.UUID, sessionID string) (Payment, error)
	SetMethodUsed(ctx context.Context, id uuid.UUID, methodUsed string, mismatch bool) (Payment, error)
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
