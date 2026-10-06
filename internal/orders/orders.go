// Package orders es el carrito y el pedido: la máquina de estados de docs/ecommerce.md §2.
//
// El carrito ES un pedido en estado draft — no hay una tabla de carritos aparte. Mientras está en
// draft, sus líneas leen el catálogo en vivo cada vez que se agregan (el precio que ves al pedir
// es el actual); en cuanto se confirma (Place), el nombre y el precio de cada línea quedan
// CONGELADOS para siempre — un cambio de menú después no debe alterar un pedido ya hecho.
//
// Quien arma un pedido no es necesariamente miembro de la organización a la que le compra: un
// cliente no pertenece al negocio. Por eso las acciones del cliente (ver/editar su carrito,
// confirmar, cancelar) NO pasan por tenant.Gate.Open (que exige un permiso de organización) sino
// por tenant.Gate.OpenForCustomer (solo exige que el negocio exista y esté activo). Las acciones
// del NEGOCIO (aceptar, despachar...) sí exigen org.orders.manage, como cualquier otro dominio.
package orders

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/catalog"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound          = errors.New("orders: pedido no encontrado")
	ErrItemNotFound      = errors.New("orders: línea no encontrada")
	ErrInvalidTransition = errors.New("orders: transición de estado no permitida")
)

// Status es el estado de un pedido. Ver docs/ecommerce.md §2.1.
type Status string

// Estados.
const (
	StatusDraft          Status = "draft" // el carrito
	StatusPlaced         Status = "placed"
	StatusConfirmed      Status = "confirmed" // pagado
	StatusPaymentFailed  Status = "payment_failed"
	StatusAccepted       Status = "accepted" // el negocio lo aceptó
	StatusRejected       Status = "rejected"
	StatusPreparing      Status = "preparing"
	StatusOutForDelivery Status = "out_for_delivery"
	StatusDelivered      Status = "delivered"
	StatusCancelled      Status = "cancelled"
)

// Operation es un cambio de estado con los estados desde los que se puede pedir. Mismo patrón que
// organizations.Operation: el estado de destino no alcanza para decidir si aplica — "aceptar" un
// pedido ya rechazado no debería revivirlo.
type Operation struct {
	// Label es el verbo, para los mensajes.
	Label string
	// From son los estados desde los que se puede aplicar.
	From []Status
	To   Status
}

// CanApply dice si la operación se puede pedir desde ese estado.
func (o Operation) CanApply(from Status) bool { return slices.Contains(o.From, from) }

// Operaciones del ciclo de vida. Ver docs/ecommerce.md §2.1.
var (
	// OpPlace lo dispara el cliente: confirma el carrito, congela el precio de cada línea.
	OpPlace = Operation{"confirmar", []Status{StatusDraft}, StatusPlaced}
	// OpMarkPaid/OpMarkPaymentFailed los dispara el webhook de internal/payments (no un actor con
	// permiso: la pasarela no es un usuario de negocio) cuando la pasarela confirma o rechaza el
	// cobro — ver payments.Service y orders.Service.MarkPaidByGateway/MarkPaymentFailedByGateway.
	OpMarkPaid          = Operation{"marcar pagado", []Status{StatusPlaced}, StatusConfirmed}
	OpMarkPaymentFailed = Operation{"marcar pago fallido", []Status{StatusPlaced}, StatusPaymentFailed}
	// OpRetryPayment lo dispara el cliente sobre un pedido con el pago fallido: vuelve a placed
	// para intentar cobrar de nuevo, sobre el MISMO pedido — nunca crea uno nuevo.
	OpRetryPayment = Operation{"reintentar el pago", []Status{StatusPaymentFailed}, StatusPlaced}
	// OpAccept/OpReject los dispara el negocio, una vez pagado.
	OpAccept = Operation{"aceptar", []Status{StatusConfirmed}, StatusAccepted}
	OpReject = Operation{"rechazar", []Status{StatusConfirmed}, StatusRejected}
	// OpStartPreparing/OpDispatch/OpMarkDelivered los dispara el negocio (o, más adelante, el
	// sistema de asignación de domis).
	OpStartPreparing = Operation{"empezar a preparar", []Status{StatusAccepted}, StatusPreparing}
	OpDispatch       = Operation{"despachar", []Status{StatusPreparing}, StatusOutForDelivery}
	OpMarkDelivered  = Operation{"marcar entregado", []Status{StatusOutForDelivery}, StatusDelivered}
	// OpCancel lo dispara el cliente o el negocio — nunca desde 'preparing' en adelante: ya hay
	// comida hecha.
	OpCancel = Operation{"cancelar", []Status{StatusDraft, StatusPlaced, StatusConfirmed, StatusAccepted}, StatusCancelled}
)

// Límites.
const (
	maxQuantity      = 50
	maxItemsPerOrder = 100
)

// ModifierSnapshot es una opción de modificador elegida, con su precio en el momento del pedido.
type ModifierSnapshot struct {
	ModifierGroupID   uuid.UUID `json:"modifier_group_id"`
	ModifierGroupName string    `json:"modifier_group_name"`
	OptionID          uuid.UUID `json:"option_id"`
	OptionName        string    `json:"option_name"`
	PriceDeltaCents   int32     `json:"price_delta_cents"`
}

// Item es una línea del pedido (o del carrito, antes de confirmar).
type Item struct {
	ID uuid.UUID `json:"id"`
	// ProductVariantID se conserva para reordenar fácil y para analítica; nunca se usa para
	// mostrar nombre o precio — eso son los campos *Snapshot/UnitPriceCents de abajo.
	ProductVariantID *uuid.UUID         `json:"product_variant_id"`
	Name             string             `json:"name"`
	UnitPriceCents   int32              `json:"unit_price_cents"`
	Modifiers        []ModifierSnapshot `json:"modifiers"`
	// UnitTotalCents = UnitPriceCents + la suma de los price_delta_cents de Modifiers.
	UnitTotalCents int32     `json:"unit_total_cents"`
	Quantity       int       `json:"quantity"`
	LineTotalCents int32     `json:"line_total_cents"`
	CreatedAt      time.Time `json:"created_at"`
}

// Order es un pedido — o, mientras Status == StatusDraft, el carrito.
type Order struct {
	ID               uuid.UUID `json:"id"`
	OrganizationID   uuid.UUID `json:"organization_id"`
	CustomerID       uuid.UUID `json:"customer_id"`
	Status           Status    `json:"status"`
	Items            []Item    `json:"items"`
	SubtotalCents    int32     `json:"subtotal_cents"`
	DiscountCents    int32     `json:"discount_cents"`
	DeliveryFeeCents int32     `json:"delivery_fee_cents"`
	TotalCents       int32     `json:"total_cents"`
	// PromotionID es el cupón aplicado al carrito, si hay uno — ver internal/promotions. Se borra
	// solo (junto con DiscountCents) en cuanto el carrito vuelve a editarse.
	PromotionID *uuid.UUID `json:"promotion_id"`
	PlacedAt    *time.Time `json:"placed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// AddItemInput son los datos para agregar una línea al carrito.
type AddItemInput struct {
	ProductID uuid.UUID
	VariantID uuid.UUID
	Quantity  int
	// OptionIDs son las opciones de modificador elegidas, de cualquier grupo que aplique al
	// producto. Se validan contra TODOS los grupos del producto (no solo los elegidos): un
	// grupo obligatorio (min_select >= 1) sin ninguna opción elegida es un error, aunque el
	// cliente no haya tocado ese grupo.
	OptionIDs []uuid.UUID
}

func validateQuantity(q int) error {
	if q < 1 || q > maxQuantity {
		return apperr.Invalid("quantity debe estar entre 1 y 50")
	}
	return nil
}

// CatalogReader es lo mínimo del catálogo que orders necesita para armar una línea de pedido. Lo
// satisface catalog.Repository de forma estructural — orders no depende de esa interfaz
// completa, solo de estos dos métodos.
type CatalogReader interface {
	GetProduct(ctx context.Context, orgID, id uuid.UUID) (catalog.Product, error)
	GetModifierGroupsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]catalog.ModifierGroup, error)
}

// resolveLine valida la variante y las opciones elegidas contra el catálogo real y arma el
// snapshot de la línea. Nunca confía en un nombre o un precio que venga en la petición — todo
// sale de leer el producto.
func resolveLine(ctx context.Context, reader CatalogReader, orgID uuid.UUID, in AddItemInput) (NewItem, error) {
	if err := validateQuantity(in.Quantity); err != nil {
		return NewItem{}, err
	}
	product, err := reader.GetProduct(ctx, orgID, in.ProductID)
	if errors.Is(err, catalog.ErrNotFound) || !product.IsActive {
		return NewItem{}, apperr.Invalid("product_id no existe o no está a la venta")
	}
	if err != nil {
		return NewItem{}, err
	}
	var variant *catalog.Variant
	for i := range product.Variants {
		if product.Variants[i].ID == in.VariantID && product.Variants[i].IsActive {
			variant = &product.Variants[i]
			break
		}
	}
	if variant == nil {
		return NewItem{}, apperr.Invalid("variant_id no existe o no está a la venta para este producto")
	}

	groups, err := reader.GetModifierGroupsByIDs(ctx, orgID, product.ModifierGroupIDs)
	if err != nil {
		return NewItem{}, err
	}
	modifiers, err := resolveModifiers(groups, in.OptionIDs)
	if err != nil {
		return NewItem{}, err
	}

	unitTotal := variant.PriceCents
	for _, m := range modifiers {
		unitTotal += m.PriceDeltaCents
	}
	return NewItem{
		ProductVariantID: &variant.ID,
		Name:             product.Name + " — " + variant.Name,
		UnitPriceCents:   variant.PriceCents,
		Modifiers:        modifiers,
		UnitTotalCents:   unitTotal,
		Quantity:         in.Quantity,
		LineTotalCents:   unitTotal * int32(in.Quantity), //nolint:gosec // quantity acotado a 50
	}, nil
}

// resolveModifiers valida optionIDs contra TODOS los grupos que aplican al producto (groups) —
// no solo contra los elegidos — para poder exigir los obligatorios (min_select >= 1) aunque el
// cliente no haya elegido nada de ese grupo.
func resolveModifiers(groups []catalog.ModifierGroup, optionIDs []uuid.UUID) ([]ModifierSnapshot, error) {
	type located struct {
		group  catalog.ModifierGroup
		option catalog.ModifierOption
	}
	byOption := make(map[uuid.UUID]located)
	for _, g := range groups {
		for _, o := range g.Options {
			if o.IsActive {
				byOption[o.ID] = located{group: g, option: o}
			}
		}
	}

	selectedPerGroup := make(map[uuid.UUID]int, len(groups))
	out := make([]ModifierSnapshot, 0, len(optionIDs))
	seen := make(map[uuid.UUID]struct{}, len(optionIDs))
	for _, id := range optionIDs {
		if _, dup := seen[id]; dup {
			continue // un id repetido no cuenta dos veces — mismo criterio que catalog con modifier_group_ids
		}
		seen[id] = struct{}{}
		loc, ok := byOption[id]
		if !ok {
			return nil, apperr.Invalid("una opción de modificador no aplica a este producto")
		}
		selectedPerGroup[loc.group.ID]++
		out = append(out, ModifierSnapshot{
			ModifierGroupID: loc.group.ID, ModifierGroupName: loc.group.Name,
			OptionID: loc.option.ID, OptionName: loc.option.Name, PriceDeltaCents: loc.option.PriceDeltaCents,
		})
	}
	for _, g := range groups {
		n := selectedPerGroup[g.ID]
		if n < g.MinSelect || n > g.MaxSelect {
			return nil, apperr.Invalid("el grupo \"" + g.Name + "\" exige entre " +
				strconv.Itoa(g.MinSelect) + " y " + strconv.Itoa(g.MaxSelect) + " opciones, se eligieron " + strconv.Itoa(n))
		}
	}
	return out, nil
}

// NewItem es una línea ya validada y congelada, lista para insertarse.
type NewItem struct {
	ProductVariantID *uuid.UUID
	Name             string
	UnitPriceCents   int32
	Modifiers        []ModifierSnapshot
	UnitTotalCents   int32
	Quantity         int
	LineTotalCents   int32
}

// DraftSummary es un carrito abierto del cliente, sin el detalle de sus líneas — lo que necesita
// un indicador de carrito visible en cualquier ruta del cliente (docs/ecommerce.md §7). Nunca
// incluye un carrito vacío: abrir el menú de un restaurante crea un borrador aunque todavía no se
// agregue nada, y eso no es un carrito que mostrar.
type DraftSummary struct {
	OrderID          uuid.UUID `json:"order_id"`
	OrganizationID   uuid.UUID `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	OrganizationSlug string    `json:"organization_slug"`
	ItemCount        int       `json:"item_count"`
	TotalCents       int32     `json:"total_cents"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	// GetOrCreateDraft devuelve el carrito abierto de este cliente en esta organización,
	// creándolo si no existe. Ver db/queries/orders.sql (GetOrCreateDraftOrder).
	GetOrCreateDraft(ctx context.Context, orgID, customerID uuid.UUID) (Order, error)
	GetOrder(ctx context.Context, orgID, id uuid.UUID) (Order, error)
	// GetOrderByID lo usa el cliente para ver/cancelar uno de sus propios pedidos sin tener que
	// declarar de qué organización es — ver db/queries/orders.sql.
	GetOrderByID(ctx context.Context, id uuid.UUID) (Order, error)
	ListByOrganization(ctx context.Context, orgID uuid.UUID, status *Status, limit, offset int) ([]Order, int64, error)
	// ListByCustomer cruza organizaciones a propósito: es "mis pedidos" del cliente, no el de un
	// negocio. Nunca incluye el carrito (draft).
	ListByCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]Order, int64, error)
	// ListMyDrafts son "mis carritos": los borradores NO vacíos del cliente, cruzando
	// organizaciones a propósito — al revés de ListByCustomer. Sin paginar: a lo sumo un puñado
	// de carritos abiertos a la vez.
	ListMyDrafts(ctx context.Context, customerID uuid.UUID) ([]DraftSummary, error)
	// AddItem, UpdateItemQuantity y RemoveItem devuelven el pedido completo ya recalculado
	// (nunca solo la línea): así el llamador siempre tiene el total al día.
	AddItem(ctx context.Context, orgID, orderID uuid.UUID, item NewItem) (Order, error)
	UpdateItemQuantity(ctx context.Context, orgID, orderID, itemID uuid.UUID, quantity int) (Order, error)
	RemoveItem(ctx context.Context, orgID, orderID, itemID uuid.UUID) (Order, error)
	// Transition aplica la operación si el estado actual la permite; si no, devuelve
	// *TransitionError (ErrInvalidTransition). place recalcula subtotal_cents/total_cents
	// desde las líneas reales ANTES de aplicar la transición, en la misma llamada.
	Transition(ctx context.Context, orgID, id uuid.UUID, op Operation, place bool) (Order, error)
	// ApplyPromotion y RemovePromotion los usa internal/promotions (a través de OrdersGateway),
	// nunca un actor directamente — ver el comentario del paquete promotions. Ambos exigen que el
	// pedido siga en draft (lo exige la consulta, no el llamador) y devuelven ErrNotFound si no
	// aplicó (no existe, o ya no es un carrito).
	ApplyPromotion(ctx context.Context, orgID, orderID, promotionID uuid.UUID, discountCents, totalCents int32) (Order, error)
	RemovePromotion(ctx context.Context, orgID, orderID uuid.UUID) (Order, error)
}

// TransitionError da el estado real cuando una transición no aplica, para un mensaje útil.
type TransitionError struct {
	Op   Operation
	From Status
}

func (e *TransitionError) Error() string {
	return ErrInvalidTransition.Error() + ": " + e.Op.Label + " desde " + string(e.From)
}

// Is hace que errors.Is(err, ErrInvalidTransition) funcione.
func (e *TransitionError) Is(target error) bool { return target == ErrInvalidTransition } //nolint:errorlint
