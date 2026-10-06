package promotions

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/orders"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// OrdersGateway es lo mínimo que promotions necesita de pedidos: leer el carrito propio del
// cliente para validar un código contra su subtotal real, y grabar el resultado. *orders.Service
// lo satisface sin saberlo — promotions nunca depende de su interfaz completa. Ver el comentario
// del paquete.
type OrdersGateway interface {
	GetCart(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (orders.Order, error)
	ApplyPromotion(ctx context.Context, actor identity.Principal, orgID, promotionID uuid.UUID, discountCents, totalCents int32) (orders.Order, error)
	RemovePromotion(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (orders.Order, error)
}

// Service reúne las reglas de las promociones. gate exige org.promotions.read/manage (lado del
// negocio, administrar cupones); aplicar/quitar un código (lado del cliente) no exige permiso
// propio, ese aislamiento ya lo hace OrdersGateway al abrir el carrito.
type Service struct {
	repo   Repository
	orders OrdersGateway
	gate   *tenant.Gate
	now    func() time.Time
}

// NewService crea el servicio.
func NewService(repo Repository, ordersGateway OrdersGateway, gate *tenant.Gate) *Service {
	return &Service{repo: repo, orders: ordersGateway, gate: gate, now: time.Now}
}

// ---------------------------------------------------------------------------
// Lado del negocio: administrar los cupones de la organización.
// ---------------------------------------------------------------------------

// Create da de alta un cupón nuevo.
func (s *Service) Create(ctx context.Context, actor identity.Principal, orgID uuid.UUID, n NewPromotion) (Promotion, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgPromotionsManage, true); err != nil {
		return Promotion{}, err
	}
	code, err := validateCode(n.Code)
	if err != nil {
		return Promotion{}, err
	}
	n.Code = code
	if err := validateDiscount(n.DiscountType, n.Value); err != nil {
		return Promotion{}, err
	}
	if n.MinOrderCents < 0 {
		return Promotion{}, apperr.Invalid("min_order_cents no puede ser negativo")
	}
	if err := validateDates(n.StartsAt, n.EndsAt); err != nil {
		return Promotion{}, err
	}
	if err := validateLimit(n.MaxUses, "max_uses"); err != nil {
		return Promotion{}, err
	}
	if err := validateLimit(n.PerCustomerLimit, "per_customer_limit"); err != nil {
		return Promotion{}, err
	}
	p, err := s.repo.Insert(ctx, orgID, n)
	if errors.Is(err, ErrDuplicateCode) {
		return Promotion{}, apperr.Conflict("ya existe una promoción activa con ese código")
	}
	return p, err
}

// Get devuelve un cupón de la organización.
func (s *Service) Get(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Promotion, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgPromotionsRead, false); err != nil {
		return Promotion{}, err
	}
	p, err := s.repo.Get(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return Promotion{}, apperr.NotFound("promoción no encontrada")
	}
	return p, err
}

// List devuelve todos los cupones de la organización (activos e inactivos: nunca se borran, ver
// el comentario del paquete).
func (s *Service) List(ctx context.Context, actor identity.Principal, orgID uuid.UUID) ([]Promotion, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgPromotionsRead, false); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, orgID)
}

// Update aplica un cambio parcial a un cupón.
func (s *Service) Update(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, p Patch) (Promotion, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgPromotionsManage, true); err != nil {
		return Promotion{}, err
	}
	if p.Code != nil {
		code, err := validateCode(*p.Code)
		if err != nil {
			return Promotion{}, err
		}
		p.Code = &code
	}
	if p.DiscountType != nil || p.Value != nil {
		current, err := s.repo.Get(ctx, orgID, id)
		if errors.Is(err, ErrNotFound) {
			return Promotion{}, apperr.NotFound("promoción no encontrada")
		}
		if err != nil {
			return Promotion{}, err
		}
		discountType, value := current.DiscountType, current.Value
		if p.DiscountType != nil {
			discountType = *p.DiscountType
		}
		if p.Value != nil {
			value = *p.Value
		}
		if err := validateDiscount(discountType, value); err != nil {
			return Promotion{}, err
		}
	}
	if p.MinOrderCents != nil && *p.MinOrderCents < 0 {
		return Promotion{}, apperr.Invalid("min_order_cents no puede ser negativo")
	}
	if p.SetStartsAt || p.SetEndsAt {
		current, err := s.repo.Get(ctx, orgID, id)
		if errors.Is(err, ErrNotFound) {
			return Promotion{}, apperr.NotFound("promoción no encontrada")
		}
		if err != nil {
			return Promotion{}, err
		}
		startsAt, endsAt := current.StartsAt, current.EndsAt
		if p.SetStartsAt {
			startsAt = p.StartsAt
		}
		if p.SetEndsAt {
			endsAt = p.EndsAt
		}
		if err := validateDates(startsAt, endsAt); err != nil {
			return Promotion{}, err
		}
	}
	if p.SetMaxUses {
		if err := validateLimit(p.MaxUses, "max_uses"); err != nil {
			return Promotion{}, err
		}
	}
	if p.SetPerCustomerLimit {
		if err := validateLimit(p.PerCustomerLimit, "per_customer_limit"); err != nil {
			return Promotion{}, err
		}
	}
	out, err := s.repo.Update(ctx, orgID, id, p)
	switch {
	case errors.Is(err, ErrNotFound):
		return Promotion{}, apperr.NotFound("promoción no encontrada")
	case errors.Is(err, ErrDuplicateCode):
		return Promotion{}, apperr.Conflict("ya existe una promoción activa con ese código")
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Lado del cliente: aplicar/quitar un código del carrito propio.
// ---------------------------------------------------------------------------

// ApplyCode valida un código contra el carrito propio (vigencia, monto mínimo, límites de uso
// reales) y, si aplica, lo graba en el carrito. Reemplaza cualquier código aplicado antes —
// aplicar OTRO código nunca los acumula.
func (s *Service) ApplyCode(ctx context.Context, actor identity.Principal, orgID uuid.UUID, code string) (orders.Order, error) {
	cart, err := s.orders.GetCart(ctx, actor, orgID)
	if err != nil {
		return orders.Order{}, err
	}
	if len(cart.Items) == 0 {
		return orders.Order{}, apperr.Invalid("el carrito está vacío")
	}

	promo, err := s.repo.GetActiveByCode(ctx, orgID, code)
	if errors.Is(err, ErrNotFound) {
		return orders.Order{}, apperr.Invalid("código no válido")
	}
	if err != nil {
		return orders.Order{}, err
	}
	if err := promo.eligible(s.now(), cart.SubtotalCents); err != nil {
		return orders.Order{}, err
	}
	if err := s.checkUsageLimits(ctx, actor, promo); err != nil {
		return orders.Order{}, err
	}

	discount := promo.discountCents(cart.SubtotalCents)
	total := cart.SubtotalCents - discount + cart.DeliveryFeeCents
	if total < 0 {
		total = 0
	}

	updated, err := s.orders.ApplyPromotion(ctx, actor, orgID, promo.ID, discount, total)
	if err != nil {
		return orders.Order{}, err
	}
	// Se graba el canje DESPUÉS de aplicarlo al pedido a propósito: si lo anterior falla, no debe
	// quedar un canje huérfano contando contra el límite de nadie. Un canje que sí quede grabado
	// pero cuyo pedido nunca llegue a confirmarse (status sigue draft) tampoco cuenta — ver
	// CountPromotionRedemptions. Un fallo AQUÍ (recordar el canje) tras un ApplyPromotion exitoso
	// es la única ventana real: el cliente sí obtiene el descuento, pero ese uso podría no quedar
	// contado contra max_uses/per_customer_limit — un riesgo de negocio menor, no de seguridad.
	if err := s.repo.RecordRedemption(ctx, orgID, promo.ID, cart.ID, actor.ID, discount); err != nil {
		return orders.Order{}, err
	}
	return updated, nil
}

// checkUsageLimits comprueba max_uses y per_customer_limit contra canjes REALES (pedidos que ya
// dejaron de ser un borrador) — ver el comentario del paquete.
func (s *Service) checkUsageLimits(ctx context.Context, actor identity.Principal, promo Promotion) error {
	if promo.MaxUses != nil {
		n, err := s.repo.CountRedemptions(ctx, promo.ID)
		if err != nil {
			return err
		}
		if n >= int64(*promo.MaxUses) {
			return apperr.Invalid("este código ya alcanzó su límite de usos")
		}
	}
	if promo.PerCustomerLimit != nil {
		n, err := s.repo.CountCustomerRedemptions(ctx, promo.ID, actor.ID)
		if err != nil {
			return err
		}
		if n >= int64(*promo.PerCustomerLimit) {
			return apperr.Invalid("ya usaste este código el máximo de veces permitido")
		}
	}
	return nil
}

// RemoveCode quita el código aplicado al carrito propio, si tenía uno. Es idempotente: quitarlo
// dos veces no es un error.
func (s *Service) RemoveCode(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (orders.Order, error) {
	return s.orders.RemovePromotion(ctx, actor, orgID)
}
