package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/orders"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/pricing"
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

// Pricer resuelve las tarifas vigentes de una organización (internal/pricing).
type Pricer interface {
	RatesFor(ctx context.Context, orgID uuid.UUID) (pricing.Rates, error)
}

// Service reúne las reglas de los pagos.
type Service struct {
	repo   Repository
	orders OrdersGateway
	gate   *tenant.Gate
	pricer Pricer
	// gateway es nil si el servidor no tiene una pasarela configurada (CORE_EPAYCO_*) — iniciar
	// un cobro responde 503, igual que conectar WhatsApp sin CORE_SECRETS_KEY.
	gateway   Gateway
	publicURL string // CORE_PUBLIC_URL: adonde le decimos a la pasarela que nos confirme
	appURL    string // CORE_APP_URL: adonde vuelve el cliente tras pagar
}

// NewService crea el servicio. gateway nil es válido: significa "sin pasarela configurada".
func NewService(repo Repository, ordersGateway OrdersGateway, gate *tenant.Gate, pricer Pricer, gateway Gateway, publicURL, appURL string) *Service {
	return &Service{repo: repo, orders: ordersGateway, gate: gate, pricer: pricer, gateway: gateway, publicURL: publicURL, appURL: appURL}
}

// amountsOf son los montos del pedido ya congelados al confirmarlo (orders.place).
func amountsOf(o orders.Order) pricing.OrderAmounts {
	return pricing.OrderAmounts{
		SubtotalCents: int64(o.SubtotalCents), SubtotalLocalCents: int64(o.SubtotalLocalCents),
		DiscountCents: int64(o.DiscountCents), DeliveryFeeCents: int64(o.DeliveryFeeCents),
	}
}

// splitOf reparte con lo CONGELADO en el pedido (comisión y parte del domicilio), no con las
// tarifas de hoy: el pedido es un documento histórico.
func splitOf(o orders.Order) pricing.Split {
	org := int64(o.SubtotalLocalCents) - int64(o.DiscountCents)
	fee := int64(o.PlatformFeeCents)
	if org < 0 {
		fee += org
		if fee < 0 {
			fee = 0
		}
		org = 0
	}
	courierFee := int64(o.CourierFeeCents)
	return pricing.Split{
		OrganizationCents: org, PlatformFeeCents: fee, CourierFeeCents: courierFee,
		CourierCents: int64(o.DeliveryFeeCents) - courierFee, PlatformCents: fee + courierFee,
	}
}

// quoteFor calcula la cotización de un pedido con un medio de pago.
func (s *Service) quoteFor(ctx context.Context, o orders.Order, method pricing.Method) (pricing.Quote, pricing.Rates, error) {
	if !method.Valid() {
		return pricing.Quote{}, pricing.Rates{}, apperr.Invalid("method debe ser card, card_international, pse o wallet")
	}
	rates, err := s.pricer.RatesFor(ctx, o.OrganizationID)
	if err != nil {
		return pricing.Quote{}, pricing.Rates{}, err
	}
	q, err := rates.QuoteFor(amountsOf(o), method)
	if err != nil {
		return pricing.Quote{}, pricing.Rates{}, fmt.Errorf("payments: calcular cotización: %w", err)
	}
	q.Split = splitOf(o)
	return q, rates, nil
}

// payableOrder lee un pedido propio del cliente y comprueba que se pueda pagar.
func (s *Service) payableOrder(ctx context.Context, actor identity.Principal, orderID uuid.UUID) (orders.Order, error) {
	order, err := s.orders.GetMyOrder(ctx, actor, orderID)
	if err != nil {
		return orders.Order{}, err
	}
	if order.Status != orders.StatusPlaced {
		return orders.Order{}, apperr.Conflict("el pedido no está listo para pagar")
	}
	if order.TotalCents <= 0 {
		return orders.Order{}, apperr.Invalid("el pedido no tiene nada que cobrar")
	}
	return order, nil
}

// Quote es lo que el cliente ve ANTES de pagar: el desglose con el costo de transacción del medio
// que eligió. Cobrar un recargo por el medio de pago solo es legal si se informa antes y el
// cliente lo acepta (docs/pagos.md §8): por eso Initiate exige el total que el cliente aceptó.
func (s *Service) Quote(ctx context.Context, actor identity.Principal, orderID uuid.UUID, method pricing.Method) (pricing.Quote, error) {
	order, err := s.payableOrder(ctx, actor, orderID)
	if err != nil {
		return pricing.Quote{}, err
	}
	q, _, err := s.quoteFor(ctx, order, method)
	return q, err
}

// InitiateInput es lo que manda el cliente al pagar.
type InitiateInput struct {
	Method pricing.Method
	// ExpectedTotalCents es el total que el cliente VIO y ACEPTÓ (Quote). Si las tarifas
	// cambiaron entre la cotización y el pago, no se cobra un valor distinto sin avisar: 409.
	ExpectedTotalCents int64
}

// Checkout es lo que el frontend necesita para abrir el pago.
type Checkout struct {
	Payment Payment `json:"payment"`
	// Type "onpage": abrir el checkout de la pasarela DENTRO de nuestra página con SessionID.
	// "redirect": mandar al cliente a CheckoutURL (pasarela sin sesiones, o falla al crearla).
	Type      string        `json:"type"`
	SessionID string        `json:"session_id,omitempty"`
	TestMode  bool          `json:"test_mode"`
	Quote     pricing.Quote `json:"quote"`
}

// Initiate arma un cobro nuevo para un pedido propio del cliente, con el medio que declaró y el
// costo de transacción que aceptó. El pedido debe estar recién confirmado (placed) — o de vuelta
// ahí tras orders.RetryPayment sobre un intento fallido.
func (s *Service) Initiate(ctx context.Context, actor identity.Principal, orderID uuid.UUID, in InitiateInput) (Checkout, error) {
	order, err := s.payableOrder(ctx, actor, orderID)
	if err != nil {
		return Checkout{}, err
	}
	if s.gateway == nil {
		return Checkout{}, apperr.Unavailable("los pagos todavía no están configurados en este servidor")
	}
	quote, rates, err := s.quoteFor(ctx, order, in.Method)
	if err != nil {
		return Checkout{}, err
	}
	if in.ExpectedTotalCents != quote.TotalCents {
		return Checkout{}, apperr.Conflict("el total cambió: vuelve a revisar el costo de transacción antes de pagar")
	}
	if quote.TotalCents > int64(^uint32(0)>>1) {
		return Checkout{}, apperr.Invalid("el total del pedido es demasiado grande")
	}
	breakdown, err := json.Marshal(quote)
	if err != nil {
		return Checkout{}, fmt.Errorf("payments: codificar desglose: %w", err)
	}
	p, err := s.repo.Insert(ctx, NewPayment{
		OrderID: order.ID, OrganizationID: order.OrganizationID,
		AmountCents: int32(quote.TotalCents), Currency: "COP", Gateway: s.gateway.Name(), //nolint:gosec // acotado arriba
		Method: in.Method, BaseCents: int32(quote.BaseCents), //nolint:gosec
		TransactionFeeCents: int32(quote.TransactionFeeCents), //nolint:gosec
		GatewayPlanCode:     quote.PlanCode, Breakdown: breakdown,
	})
	if err != nil {
		return Checkout{}, err
	}
	req := ChargeRequest{
		PaymentID: p.ID, AmountCents: p.AmountCents, Currency: "COP",
		Description:     "Pedido Domicilia " + order.ID.String()[:8],
		CustomerEmail:   actor.Email,
		ResponseURL:     s.appURL + "/cliente/pedidos/" + order.ID.String(),
		ConfirmationURL: s.publicURL + WebhookPath,
		Method:          in.Method,
		Split:           splitRequest(rates, quote.Split),
	}
	// URL del checkout por redirección: siempre se arma (no llama a la red). Sirve de respaldo si
	// la sesión onpage falla y para el link de cobro de WhatsApp (docs/pagos.md §7.2).
	checkoutURL, err := s.gateway.BuildCheckout(req)
	if err != nil {
		return Checkout{}, fmt.Errorf("payments: armar el cobro: %w", err)
	}
	if p, err = s.repo.SetCheckoutURL(ctx, p.ID, checkoutURL); err != nil {
		return Checkout{}, err
	}
	out := Checkout{Payment: p, Type: "redirect", Quote: quote}
	sg, ok := s.gateway.(SessionGateway)
	if !ok {
		return out, nil
	}
	sessionID, err := sg.CreateSession(ctx, req)
	if err != nil {
		// No se pierde el cobro: el pago queda pending con su URL de redirección. El cliente sale
		// a la página de la pasarela en vez de abrirla encima de la nuestra.
		return out, nil //nolint:nilerr // degradación a propósito, ver arriba
	}
	if p, err = s.repo.SetSession(ctx, p.ID, sessionID); err != nil {
		return Checkout{}, err
	}
	return Checkout{Payment: p, Type: "onpage", SessionID: sessionID, TestMode: sg.TestMode(), Quote: quote}, nil
}

// splitRequest arma el reparto para la pasarela. Solo si el split está activado Y la
// organización tiene su id de receptor: si no, todo llega a la cuenta de la plataforma (y se
// liquida aparte — docs/pagos.md §3, riesgo abierto). El domicilio NO se reparte al domiciliario
// en el cobro: cuando se paga todavía no hay domiciliario asignado (queda en la parte de la
// plataforma, docs/pagos.md §16).
func splitRequest(r pricing.Rates, s pricing.Split) *SplitRequest {
	if !r.SplitEnabled || r.EpaycoMerchantID == "" || s.OrganizationCents <= 0 {
		return nil
	}
	return &SplitRequest{Receivers: []SplitReceiver{{MerchantID: r.EpaycoMerchantID, AmountCents: s.OrganizationCents}}}
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

// methodMatches dice si el medio usado corresponde al declarado. Una tarjeta internacional no se
// distingue de una nacional por la franquicia: si se declaró internacional y se pagó con tarjeta,
// el cliente pagó de más, no de menos — no es una discrepancia que nos cueste dinero.
func methodMatches(declared, used pricing.Method) bool {
	if used == "" || declared == used {
		return true
	}
	return declared == pricing.MethodCardInternational && used == pricing.MethodCard
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

	// Medio usado vs. declarado: se registra siempre (conciliación), no cambia el resultado.
	if event.RawMethod != "" {
		mismatch := payment.Method != nil && !methodMatches(pricing.Method(*payment.Method), event.MethodUsed)
		if _, err := s.repo.SetMethodUsed(ctx, payment.ID, event.RawMethod, mismatch); err != nil {
			return err
		}
	}

	status := StatusFailed
	reason := optionalReason(event)
	if event.Succeeded {
		status = StatusSucceeded
	}
	// Un cobro aprobado por un monto distinto al del pago NO confirma el pedido: la firma prueba
	// que el mensaje es de la pasarela, no que el cliente pagó lo que debía.
	if event.Succeeded && event.AmountCents > 0 && event.AmountCents != int64(payment.AmountCents) {
		status = StatusFailed
		msg := "la pasarela reportó " + strconv.FormatInt(event.AmountCents, 10) + " centavos, se esperaban " +
			strconv.FormatInt(int64(payment.AmountCents), 10)
		reason = &msg
		event.Succeeded = false
	}
	// Settle solo avanza desde pending (ver db/queries/payments.sql): si este pago ya se había
	// resuelto por otro evento, no hay nada más que hacer aparte de lo que RecordEvent ya evitó.
	if _, err := s.repo.Settle(ctx, payment.ID, status, reason); err != nil {
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
