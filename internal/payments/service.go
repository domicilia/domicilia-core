package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/orders"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// OrdersGateway es lo mínimo que payments necesita de pedidos: iniciar un cobro exige leer UN
// pedido propio del cliente (ya con dueño y estado comprobados), y el webhook exige poder marcar
// el resultado. *orders.Service lo satisface sin saberlo — payments nunca depende de su interfaz
// completa. Ver el comentario del paquete.
type OrdersGateway interface {
	GetMyOrder(ctx context.Context, actor identity.Principal, id uuid.UUID) (orders.Order, error)
	GetForOrg(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (orders.Order, error)
	MarkPaidByGateway(ctx context.Context, orgID, orderID uuid.UUID) (orders.Order, error)
	MarkPaymentFailedByGateway(ctx context.Context, orgID, orderID uuid.UUID) (orders.Order, error)
}

// Service reúne las reglas de los pagos.
type Service struct {
	repo   Repository
	orders OrdersGateway
	gate   *tenant.Gate
	// gateway es nil si el servidor no tiene una pasarela configurada (CORE_EPAYCO_*) — iniciar
	// un cobro responde 503, igual que conectar WhatsApp sin CORE_SECRETS_KEY.
	gateway   Gateway
	publicURL string // CORE_PUBLIC_URL: adonde le decimos a la pasarela que nos confirme
	appURL    string // CORE_APP_URL: adonde vuelve el cliente tras pagar
}

// NewService crea el servicio. gateway nil es válido: significa "sin pasarela configurada".
func NewService(repo Repository, ordersGateway OrdersGateway, gate *tenant.Gate, gateway Gateway, publicURL, appURL string) *Service {
	return &Service{repo: repo, orders: ordersGateway, gate: gate, gateway: gateway, publicURL: publicURL, appURL: appURL}
}

// Initiate arma un cobro nuevo para un pedido propio del cliente. El pedido debe estar recién
// confirmado (placed) — o de vuelta ahí tras orders.RetryPayment sobre un intento fallido; nunca
// se inicia un cobro en cualquier otro estado.
func (s *Service) Initiate(ctx context.Context, actor identity.Principal, orderID uuid.UUID) (Payment, error) {
	order, err := s.orders.GetMyOrder(ctx, actor, orderID)
	if err != nil {
		return Payment{}, err
	}
	if order.Status != orders.StatusPlaced {
		return Payment{}, apperr.Conflict("el pedido no está listo para pagar")
	}
	if order.TotalCents <= 0 {
		return Payment{}, apperr.Invalid("el pedido no tiene nada que cobrar")
	}
	if s.gateway == nil {
		return Payment{}, apperr.Unavailable("los pagos todavía no están configurados en este servidor")
	}
	p, err := s.repo.Insert(ctx, order.ID, order.OrganizationID, order.TotalCents, "COP", s.gateway.Name())
	if err != nil {
		return Payment{}, err
	}
	checkoutURL, err := s.gateway.BuildCheckout(ChargeRequest{
		PaymentID: p.ID, AmountCents: order.TotalCents, Currency: "COP",
		Description:     "Pedido domicilia " + order.ID.String(),
		CustomerEmail:   actor.Email,
		ResponseURL:     s.appURL + "/cliente/pedidos/" + order.ID.String(),
		ConfirmationURL: s.publicURL + "/webhooks/payments",
	})
	if err != nil {
		return Payment{}, fmt.Errorf("payments: armar el cobro: %w", err)
	}
	return s.repo.SetCheckoutURL(ctx, p.ID, checkoutURL)
}

// ListForOrder devuelve los intentos de cobro de un pedido — puede haber más de uno si el primero
// falló y el cliente reintentó. Requiere org.payments.read: es información contable, no se
// delega al empleado del día a día.
func (s *Service) ListForOrder(ctx context.Context, actor identity.Principal, orgID, orderID uuid.UUID) ([]Payment, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgPaymentsRead, false); err != nil {
		return nil, err
	}
	// Confirma que el pedido es de esta organización ANTES de listar sus pagos — GetForOrg ya
	// hace ese aislamiento (y su propio 404 si no lo es).
	if _, err := s.orders.GetForOrg(ctx, actor, orgID, orderID); err != nil {
		return nil, err
	}
	return s.repo.ListByOrder(ctx, orgID, orderID)
}

// HandleWebhook procesa un evento YA VALIDADO de la pasarela (ver Gateway.ParseWebhook): lo
// registra para no aplicarlo dos veces y, si es la primera vez, marca el pago y el pedido.
// Nunca falla porque el pago haya sido rechazado — eso es un resultado normal, no un error.
func (s *Service) HandleWebhook(ctx context.Context, source string, event WebhookEvent) error {
	applied, err := s.repo.RecordEvent(ctx, source, event.EventKey, event.RawPayload)
	if err != nil {
		return err
	}
	if !applied {
		return nil // ya se había procesado — la pasarela reintregó una entrega que ya llegó antes
	}

	payment, err := s.repo.GetByID(ctx, event.PaymentID)
	if errors.Is(err, ErrNotFound) {
		return apperr.Invalid("el webhook referencia un pago que no existe")
	}
	if err != nil {
		return err
	}

	status := StatusFailed
	if event.Succeeded {
		status = StatusSucceeded
	}
	// Settle solo avanza desde pending (ver db/queries/payments.sql): si este pago ya se había
	// resuelto por otro evento, no hay nada más que hacer aparte de lo que RecordEvent ya evitó.
	if _, err := s.repo.Settle(ctx, payment.ID, status, optionalReason(event)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	if event.Succeeded {
		_, err = s.orders.MarkPaidByGateway(ctx, payment.OrganizationID, payment.OrderID)
	} else {
		_, err = s.orders.MarkPaymentFailedByGateway(ctx, payment.OrganizationID, payment.OrderID)
	}
	if err != nil && !errors.Is(err, orders.ErrInvalidTransition) {
		// El pedido ya no estaba en placed (p. ej. el cliente lo canceló mientras pagaba): el
		// pago igual quedó resuelto arriba. Cualquier otro error sí es real.
		return err
	}
	return nil
}

func optionalReason(event WebhookEvent) *string {
	if event.Succeeded || event.FailureReason == "" {
		return nil
	}
	return &event.FailureReason
}

// GetForOrg devuelve un pago de la organización (para su detalle, si algún día hace falta).
func (s *Service) GetForOrg(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Payment, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgPaymentsRead, false); err != nil {
		return Payment{}, err
	}
	p, err := s.repo.Get(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return Payment{}, apperr.NotFound("pago no encontrado")
	}
	return p, err
}
