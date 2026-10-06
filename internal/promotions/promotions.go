// Package promotions son los cupones/descuentos aplicables al carrito. Diseño en
// docs/ecommerce.md §4 — cuarta y última pieza planeada del módulo de e-commerce
// (catalog → orders → payments → promotions).
//
// Depende de internal/orders (aplicar un código actúa sobre el carrito de un cliente), nunca al
// revés, a través de una interfaz propia y angosta (OrdersGateway) — mismo patrón que
// orders.CatalogReader y payments.OrdersGateway.
//
// Un canje (PromotionRedemption) solo cuenta para max_uses/per_customer_limit si el pedido dejó
// de ser un borrador Y sigue teniendo esa promoción aplicada — ver el comentario de la migración
// 00008 y db/queries/promotions.sql (CountPromotionRedemptions). Así, aplicar un código y seguir
// editando el carrito (lo que borra orders.promotion_id) nunca gasta un cupo de verdad.
package promotions

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound      = errors.New("promotions: no encontrada")
	ErrDuplicateCode = errors.New("promotions: código repetido")
)

// Tipos de descuento.
const (
	DiscountPercent = "percent" // 1-100
	DiscountFixed   = "fixed"   // centavos, > 0
)

// Límites de los campos.
const (
	maxCodeLen = 40
)

// Promotion es un cupón de una organización.
type Promotion struct {
	ID               uuid.UUID  `json:"id"`
	OrganizationID   uuid.UUID  `json:"organization_id"`
	Code             string     `json:"code"`
	DiscountType     string     `json:"discount_type"`
	Value            int32      `json:"value"`
	MinOrderCents    int32      `json:"min_order_cents"`
	StartsAt         *time.Time `json:"starts_at"`
	EndsAt           *time.Time `json:"ends_at"`
	MaxUses          *int       `json:"max_uses"`
	PerCustomerLimit *int       `json:"per_customer_limit"`
	IsActive         bool       `json:"is_active"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// validateCode exige un código no vacío, hasta maxCodeLen, y lo normaliza a mayúsculas — mismo
// texto en mayúsculas o minúsculas es el mismo cupón (ver el índice de la migración).
func validateCode(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" || len(code) > maxCodeLen {
		return "", apperr.Invalid("code debe tener entre 1 y 40 caracteres")
	}
	return code, nil
}

func validateDiscount(discountType string, value int32) error {
	switch discountType {
	case DiscountPercent:
		if value < 1 || value > 100 {
			return apperr.Invalid("un descuento percent debe estar entre 1 y 100")
		}
	case DiscountFixed:
		if value < 1 {
			return apperr.Invalid("un descuento fixed debe ser mayor que 0")
		}
	default:
		return apperr.Invalid("discount_type debe ser percent o fixed")
	}
	return nil
}

func validateDates(startsAt, endsAt *time.Time) error {
	if startsAt != nil && endsAt != nil && !startsAt.Before(*endsAt) {
		return apperr.Invalid("starts_at debe ser anterior a ends_at")
	}
	return nil
}

func validateLimit(n *int, field string) error {
	if n != nil && *n < 1 {
		return apperr.Invalid(field + " debe ser mayor que 0 (o ausente, para sin límite)")
	}
	return nil
}

// discountCents calcula el descuento de esta promoción sobre un subtotal — nunca más que el
// subtotal mismo (un fixed más grande que el pedido no vuelve el pedido negativo).
func (p Promotion) discountCents(subtotalCents int32) int32 {
	var d int32
	switch p.DiscountType {
	case DiscountPercent:
		d = subtotalCents * p.Value / 100
	default: // DiscountFixed
		d = p.Value
	}
	if d > subtotalCents {
		d = subtotalCents
	}
	return d
}

// eligible dice si esta promoción se puede aplicar AHORA (vigencia y monto mínimo) — no
// comprueba límites de uso, eso exige consultar la base (ver Service.applyValidated).
func (p Promotion) eligible(now time.Time, subtotalCents int32) error {
	if !p.IsActive {
		return apperr.Invalid("código no válido")
	}
	if p.StartsAt != nil && now.Before(*p.StartsAt) {
		return apperr.Invalid("esta promoción todavía no empieza")
	}
	if p.EndsAt != nil && now.After(*p.EndsAt) {
		return apperr.Invalid("esta promoción ya venció")
	}
	if subtotalCents < p.MinOrderCents {
		return apperr.Invalid("el pedido no alcanza el mínimo de esta promoción")
	}
	return nil
}

// NewPromotion son los datos para crear una promoción.
type NewPromotion struct {
	Code             string
	DiscountType     string
	Value            int32
	MinOrderCents    int32
	StartsAt         *time.Time
	EndsAt           *time.Time
	MaxUses          *int
	PerCustomerLimit *int
}

// Patch es un cambio parcial. Code/DiscountType/Value/MinOrderCents/IsActive nunca se ponen en
// blanco (un puntero nil = no tocar basta). StartsAt/EndsAt/MaxUses/PerCustomerLimit SÍ admiten
// null ("sin fecha", "sin límite"): SetX distingue ausente de null, mismo patrón que catalog.
type Patch struct {
	Code                *string
	DiscountType        *string
	Value               *int32
	MinOrderCents       *int32
	SetStartsAt         bool
	StartsAt            *time.Time
	SetEndsAt           bool
	EndsAt              *time.Time
	SetMaxUses          bool
	MaxUses             *int
	SetPerCustomerLimit bool
	PerCustomerLimit    *int
	IsActive            *bool
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	Insert(ctx context.Context, orgID uuid.UUID, n NewPromotion) (Promotion, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Promotion, error)
	// GetActiveByCode devuelve ErrNotFound si no hay ninguna activa con ese código.
	GetActiveByCode(ctx context.Context, orgID uuid.UUID, code string) (Promotion, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Promotion, error)
	Update(ctx context.Context, orgID, id uuid.UUID, p Patch) (Promotion, error)
	// CountRedemptions y CountCustomerRedemptions solo cuentan canjes reales — ver el comentario
	// del paquete.
	CountRedemptions(ctx context.Context, promotionID uuid.UUID) (int64, error)
	CountCustomerRedemptions(ctx context.Context, promotionID, customerID uuid.UUID) (int64, error)
	// RecordRedemption reemplaza el canje del pedido (nunca lo acumula): aplicar otro código
	// sobre el mismo carrito pisa el anterior.
	RecordRedemption(ctx context.Context, orgID, promotionID, orderID, customerID uuid.UUID, discountCents int32) error
}
