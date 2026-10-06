package orders

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// Service reúne las reglas de los pedidos. gate exige permiso (lado del negocio);
// gate.OpenForCustomer solo exige que la organización exista y esté activa (lado del cliente) —
// ver el comentario del paquete.
type Service struct {
	repo    Repository
	catalog CatalogReader
	gate    *tenant.Gate
}

// NewService crea el servicio.
func NewService(repo Repository, catalogReader CatalogReader, gate *tenant.Gate) *Service {
	return &Service{repo: repo, catalog: catalogReader, gate: gate}
}

// ---------------------------------------------------------------------------
// Lado del cliente: su propio carrito y sus propios pedidos.
// ---------------------------------------------------------------------------

// GetCart devuelve el carrito abierto del cliente en esta organización, creándolo si hace falta.
func (s *Service) GetCart(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (Order, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return Order{}, err
	}
	return s.repo.GetOrCreateDraft(ctx, orgID, actor.ID)
}

// AddItem agrega una línea al carrito, con su precio y sus modificadores validados contra el
// catálogo real de la organización — nunca contra lo que venga en la petición.
func (s *Service) AddItem(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in AddItemInput) (Order, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return Order{}, err
	}
	if len(in.OptionIDs) > maxOptionsPerLine {
		return Order{}, apperr.Invalid("una línea admite hasta 50 opciones de modificador")
	}
	item, err := resolveLine(ctx, s.catalog, orgID, in)
	if err != nil {
		return Order{}, err
	}
	cart, err := s.repo.GetOrCreateDraft(ctx, orgID, actor.ID)
	if err != nil {
		return Order{}, err
	}
	if len(cart.Items) >= maxItemsPerOrder {
		return Order{}, apperr.Invalid("el carrito admite hasta " + strconv.Itoa(maxItemsPerOrder) + " líneas")
	}
	return s.repo.AddItem(ctx, orgID, cart.ID, item)
}

// UpdateItemQuantity cambia la cantidad de una línea del carrito propio.
func (s *Service) UpdateItemQuantity(ctx context.Context, actor identity.Principal, orgID, itemID uuid.UUID, quantity int) (Order, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return Order{}, err
	}
	if err := validateQuantity(quantity); err != nil {
		return Order{}, err
	}
	cart, err := s.repo.GetOrCreateDraft(ctx, orgID, actor.ID)
	if err != nil {
		return Order{}, err
	}
	out, err := s.repo.UpdateItemQuantity(ctx, orgID, cart.ID, itemID, quantity)
	if errors.Is(err, ErrItemNotFound) {
		return Order{}, apperr.NotFound("línea no encontrada")
	}
	return out, err
}

// RemoveItem quita una línea del carrito propio.
func (s *Service) RemoveItem(ctx context.Context, actor identity.Principal, orgID, itemID uuid.UUID) (Order, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return Order{}, err
	}
	cart, err := s.repo.GetOrCreateDraft(ctx, orgID, actor.ID)
	if err != nil {
		return Order{}, err
	}
	out, err := s.repo.RemoveItem(ctx, orgID, cart.ID, itemID)
	if errors.Is(err, ErrItemNotFound) {
		return Order{}, apperr.NotFound("línea no encontrada")
	}
	return out, err
}

// Place confirma el carrito: congela el nombre y el precio de cada línea y lo deja StatusPlaced,
// listo para que la pasarela de pago lo cobre (internal/payments, siguiente pieza).
func (s *Service) Place(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (Order, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return Order{}, err
	}
	cart, err := s.repo.GetOrCreateDraft(ctx, orgID, actor.ID)
	if err != nil {
		return Order{}, err
	}
	if len(cart.Items) == 0 {
		return Order{}, apperr.Invalid("el carrito está vacío")
	}
	return s.repo.Transition(ctx, orgID, cart.ID, OpPlace, true)
}

// MyOrders es "mis pedidos": cruza organizaciones a propósito, nunca incluye el carrito.
func (s *Service) MyOrders(ctx context.Context, actor identity.Principal, limit, offset int) ([]Order, int64, error) {
	return s.repo.ListByCustomer(ctx, actor.ID, limit, offset)
}

// MyDrafts es "mis carritos": los borradores abiertos del cliente, cruzando organizaciones a
// propósito — lo que necesita un indicador de carrito visible en cualquier ruta del cliente.
func (s *Service) MyDrafts(ctx context.Context, actor identity.Principal) ([]DraftSummary, error) {
	return s.repo.ListMyDrafts(ctx, actor.ID)
}

// GetMyOrder devuelve uno de los propios pedidos del cliente (nunca el de otro, ni el carrito de
// otra organización): 404 en ambos casos, no 403 — no hace falta que sepa que el id es de otro.
func (s *Service) GetMyOrder(ctx context.Context, actor identity.Principal, id uuid.UUID) (Order, error) {
	o, err := s.repo.GetOrderByID(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && (o.CustomerID != actor.ID || o.Status == StatusDraft)) {
		return Order{}, apperr.NotFound("pedido no encontrado")
	}
	return o, err
}

// CancelMyOrder cancela un pedido propio, si su estado todavía lo permite.
func (s *Service) CancelMyOrder(ctx context.Context, actor identity.Principal, id uuid.UUID) (Order, error) {
	o, err := s.GetMyOrder(ctx, actor, id)
	if err != nil {
		return Order{}, err
	}
	out, err := s.repo.Transition(ctx, o.OrganizationID, o.ID, OpCancel, false)
	if errors.Is(err, ErrInvalidTransition) {
		return Order{}, apperr.Conflict("el pedido ya no se puede cancelar en su estado actual")
	}
	return out, err
}

// RetryPayment vuelve un pedido con el pago fallido a placed, sobre el MISMO pedido — nunca crea
// uno nuevo. Después de esto, el cliente llama de nuevo a payments para intentar cobrar.
func (s *Service) RetryPayment(ctx context.Context, actor identity.Principal, id uuid.UUID) (Order, error) {
	o, err := s.GetMyOrder(ctx, actor, id)
	if err != nil {
		return Order{}, err
	}
	out, err := s.repo.Transition(ctx, o.OrganizationID, o.ID, OpRetryPayment, false)
	if errors.Is(err, ErrInvalidTransition) {
		return Order{}, apperr.Conflict("el pedido no tiene un pago fallido que reintentar")
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Lado de internal/promotions (OrdersGateway) — aplicar un código actúa sobre el carrito de un
// cliente, pero la validación del código (vigencia, mínimo, límites de uso) es de promotions, no
// de aquí. Ver el comentario del paquete promotions.
// ---------------------------------------------------------------------------

// ApplyPromotion graba en el carrito propio el descuento que internal/promotions ya calculó y
// validó — este servicio no vuelve a comprobar nada del cupón, solo que el carrito siga siendo un
// carrito.
func (s *Service) ApplyPromotion(ctx context.Context, actor identity.Principal, orgID uuid.UUID, promotionID uuid.UUID, discountCents, totalCents int32) (Order, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return Order{}, err
	}
	cart, err := s.repo.GetOrCreateDraft(ctx, orgID, actor.ID)
	if err != nil {
		return Order{}, err
	}
	out, err := s.repo.ApplyPromotion(ctx, orgID, cart.ID, promotionID, discountCents, totalCents)
	if errors.Is(err, ErrNotFound) {
		return Order{}, apperr.Conflict("el carrito cambió justo antes de aplicar el código, volvé a intentar")
	}
	return out, err
}

// RemovePromotion quita el código aplicado al carrito propio, si tenía uno.
func (s *Service) RemovePromotion(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (Order, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return Order{}, err
	}
	cart, err := s.repo.GetOrCreateDraft(ctx, orgID, actor.ID)
	if err != nil {
		return Order{}, err
	}
	out, err := s.repo.RemovePromotion(ctx, orgID, cart.ID)
	if errors.Is(err, ErrNotFound) {
		return Order{}, apperr.Conflict("el carrito cambió justo antes de quitar el código, volvé a intentar")
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Lado de la pasarela de pago (internal/payments) — no de un usuario con permiso.
// ---------------------------------------------------------------------------

// MarkPaidByGateway y MarkPaymentFailedByGateway las llama el webhook de la pasarela de pago
// (internal/payments), no un usuario: no reciben actor ni piden permiso. La pasarela se autentica
// con la firma del webhook, no con un JWT — ver payments.Gateway.ParseWebhook.
func (s *Service) MarkPaidByGateway(ctx context.Context, orgID, id uuid.UUID) (Order, error) {
	return s.repo.Transition(ctx, orgID, id, OpMarkPaid, false)
}

func (s *Service) MarkPaymentFailedByGateway(ctx context.Context, orgID, id uuid.UUID) (Order, error) {
	return s.repo.Transition(ctx, orgID, id, OpMarkPaymentFailed, false)
}

// ---------------------------------------------------------------------------
// Lado del negocio: gestionar los pedidos que le llegan.
// ---------------------------------------------------------------------------

// ListForOrg devuelve los pedidos de la organización, sin el detalle de líneas (un listado no lo
// necesita) y sin los carritos (drafts) de otros — solo lo que de verdad se confirmó.
func (s *Service) ListForOrg(ctx context.Context, actor identity.Principal, orgID uuid.UUID, status *Status, limit, offset int) ([]Order, int64, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgOrdersRead, false); err != nil {
		return nil, 0, err
	}
	if status != nil && *status == StatusDraft {
		return nil, 0, apperr.Invalid("status no admite draft: eso es el carrito de un cliente, no un pedido del negocio")
	}
	return s.repo.ListByOrganization(ctx, orgID, status, limit, offset)
}

// GetForOrg devuelve un pedido de la organización, con sus líneas.
func (s *Service) GetForOrg(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Order, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgOrdersRead, false); err != nil {
		return Order{}, err
	}
	o, err := s.repo.GetOrder(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) || (err == nil && o.Status == StatusDraft) {
		return Order{}, apperr.NotFound("pedido no encontrado")
	}
	return o, err
}

func (s *Service) transitionForOrg(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, op Operation) (Order, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgOrdersManage, true); err != nil {
		return Order{}, err
	}
	out, err := s.repo.Transition(ctx, orgID, id, op, false)
	switch {
	case errors.Is(err, ErrNotFound):
		return Order{}, apperr.NotFound("pedido no encontrado")
	case errors.Is(err, ErrInvalidTransition):
		var te *TransitionError
		errors.As(err, &te)
		return Order{}, apperr.Conflict("no se puede " + op.Label + " un pedido en estado " + string(te.From))
	}
	return out, err
}

// Accept, Reject, StartPreparing, Dispatch y MarkDelivered son las acciones del negocio sobre un
// pedido ya confirmado (pagado). Todas exigen org.orders.manage.
func (s *Service) Accept(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Order, error) {
	return s.transitionForOrg(ctx, actor, orgID, id, OpAccept)
}

func (s *Service) Reject(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Order, error) {
	return s.transitionForOrg(ctx, actor, orgID, id, OpReject)
}

func (s *Service) StartPreparing(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Order, error) {
	return s.transitionForOrg(ctx, actor, orgID, id, OpStartPreparing)
}

func (s *Service) Dispatch(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Order, error) {
	return s.transitionForOrg(ctx, actor, orgID, id, OpDispatch)
}

func (s *Service) MarkDelivered(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Order, error) {
	return s.transitionForOrg(ctx, actor, orgID, id, OpMarkDelivered)
}

// CancelForOrg cancela un pedido desde el lado del negocio (p. ej. no hay insumos).
func (s *Service) CancelForOrg(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Order, error) {
	return s.transitionForOrg(ctx, actor, orgID, id, OpCancel)
}

// maxOptionsPerLine acota entradas absurdas (no hay un producto real con 50 modificadores en una
// sola línea); las reglas de negocio de verdad (min/max por grupo) ya las exige resolveModifiers.
const maxOptionsPerLine = 50
