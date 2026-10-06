package orders

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

type pgRepository struct {
	q    *store.Queries
	pool *pgxpool.Pool
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: store.New(pool), pool: pool}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func toOrderBase(o store.Order) Order {
	var promotionID *uuid.UUID
	if o.PromotionID.Valid {
		id := o.PromotionID.UUID
		promotionID = &id
	}
	return Order{
		ID: o.ID, OrganizationID: o.OrganizationID, CustomerID: o.CustomerID, Status: Status(o.Status),
		Items:            []Item{}, // nunca nil: mismo motivo que catalog.Product.Variants — ver su comentario.
		SubtotalCents:    o.SubtotalCents,
		DiscountCents:    o.DiscountCents,
		DeliveryFeeCents: o.DeliveryFeeCents,
		TotalCents:       o.TotalCents,
		PromotionID:      promotionID,
		PlacedAt:         timePtr(o.PlacedAt),
		CreatedAt:        o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

func toItem(row store.OrderItem) (Item, error) {
	var variantID *uuid.UUID
	if row.ProductVariantID.Valid {
		v := row.ProductVariantID.UUID
		variantID = &v
	}
	var mods []ModifierSnapshot
	if len(row.ModifiersSnapshot) > 0 {
		if err := json.Unmarshal(row.ModifiersSnapshot, &mods); err != nil {
			return Item{}, fmt.Errorf("orders: modificadores de la línea %s: %w", row.ID, err)
		}
	}
	if mods == nil {
		mods = []ModifierSnapshot{}
	}
	return Item{
		ID: row.ID, ProductVariantID: variantID, Name: row.NameSnapshot,
		UnitPriceCents: row.UnitPriceCentsSnapshot, Modifiers: mods, UnitTotalCents: row.UnitTotalCents,
		Quantity: int(row.Quantity), LineTotalCents: row.LineTotalCents, CreatedAt: row.CreatedAt,
	}, nil
}

// withItems completa Items de un pedido con una sola consulta.
func withItems(ctx context.Context, q *store.Queries, o Order) (Order, error) {
	rows, err := q.ListOrderItemsByOrder(ctx, o.ID)
	if err != nil {
		return Order{}, fmt.Errorf("orders: listar líneas: %w", err)
	}
	o.Items = make([]Item, len(rows))
	for i, row := range rows {
		item, err := toItem(row)
		if err != nil {
			return Order{}, err
		}
		o.Items[i] = item
	}
	return o, nil
}

func (r *pgRepository) GetOrCreateDraft(ctx context.Context, orgID, customerID uuid.UUID) (Order, error) {
	row, err := r.q.GetOrCreateDraftOrder(ctx, store.GetOrCreateDraftOrderParams{OrganizationID: orgID, CustomerID: customerID})
	if err != nil {
		return Order{}, fmt.Errorf("orders: abrir carrito: %w", err)
	}
	return withItems(ctx, r.q, toOrderBase(row))
}

func (r *pgRepository) GetOrder(ctx context.Context, orgID, id uuid.UUID) (Order, error) {
	row, err := r.q.GetOrder(ctx, store.GetOrderParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("orders: leer pedido: %w", err)
	}
	return withItems(ctx, r.q, toOrderBase(row))
}

func (r *pgRepository) GetOrderByID(ctx context.Context, id uuid.UUID) (Order, error) {
	row, err := r.q.GetOrderByID(ctx, id)
	if db.IsNoRows(err) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("orders: leer pedido por id: %w", err)
	}
	return withItems(ctx, r.q, toOrderBase(row))
}

func (r *pgRepository) ListByOrganization(ctx context.Context, orgID uuid.UUID, status *Status, limit, offset int) ([]Order, int64, error) {
	var s *string
	if status != nil {
		v := string(*status)
		s = &v
	}
	rows, err := r.q.ListOrdersByOrganization(ctx, store.ListOrdersByOrganizationParams{
		OrganizationID: orgID, Status: s, PageSize: int32(limit), PageOffset: int32(offset), //nolint:gosec // acotados por el paginador
	})
	if err != nil {
		return nil, 0, fmt.Errorf("orders: listar de la organización: %w", err)
	}
	total, err := r.q.CountOrdersByOrganization(ctx, store.CountOrdersByOrganizationParams{OrganizationID: orgID, Status: s})
	if err != nil {
		return nil, 0, fmt.Errorf("orders: contar de la organización: %w", err)
	}
	// Sin items: un listado no necesita el detalle de cada línea, y evita N+1 innecesarios.
	out := make([]Order, len(rows))
	for i, row := range rows {
		out[i] = toOrderBase(row)
	}
	return out, total, nil
}

func (r *pgRepository) ListByCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]Order, int64, error) {
	rows, err := r.q.ListOrdersByCustomer(ctx, store.ListOrdersByCustomerParams{
		CustomerID: customerID, PageSize: int32(limit), PageOffset: int32(offset), //nolint:gosec
	})
	if err != nil {
		return nil, 0, fmt.Errorf("orders: listar del cliente: %w", err)
	}
	total, err := r.q.CountOrdersByCustomer(ctx, customerID)
	if err != nil {
		return nil, 0, fmt.Errorf("orders: contar del cliente: %w", err)
	}
	out := make([]Order, len(rows))
	for i, row := range rows {
		out[i] = toOrderBase(row)
	}
	return out, total, nil
}

// recalcCartAmounts recalcula subtotal_cents desde las líneas reales y BORRA cualquier promoción
// aplicada (ver RecalcCartAmounts): la llaman las mutaciones del carrito (agregar/cambiar/quitar
// una línea), nunca place(). Sin costo de envío todavía (otra ronda): total_cents = subtotal_cents
// por ahora.
func recalcCartAmounts(ctx context.Context, q *store.Queries, orgID, orderID uuid.UUID) (store.Order, error) {
	sum, err := q.SumOrderItems(ctx, orderID)
	if err != nil {
		return store.Order{}, fmt.Errorf("orders: sumar líneas: %w", err)
	}
	subtotal := int32(sum) //nolint:gosec // la suma de hasta 100 líneas de hasta 100M c/u no desborda int32 en la práctica
	row, err := q.RecalcCartAmounts(ctx, store.RecalcCartAmountsParams{
		ID: orderID, OrganizationID: orgID, SubtotalCents: subtotal, TotalCents: subtotal,
	})
	if err != nil {
		return store.Order{}, fmt.Errorf("orders: fijar montos: %w", err)
	}
	return row, nil
}

// finalizeAmounts reconfirma subtotal_cents desde las líneas reales al confirmar el carrito
// (place) SIN tocar discount_cents/promotion_id (a diferencia de recalcCartAmounts) — ya no
// pudieron cambiar desde que se aplicó una promoción, eso ya la habría borrado.
func finalizeAmounts(ctx context.Context, q *store.Queries, orgID, orderID uuid.UUID) (store.Order, error) {
	current, err := q.GetOrder(ctx, store.GetOrderParams{ID: orderID, OrganizationID: orgID})
	if err != nil {
		return store.Order{}, fmt.Errorf("orders: leer pedido para confirmar montos: %w", err)
	}
	sum, err := q.SumOrderItems(ctx, orderID)
	if err != nil {
		return store.Order{}, fmt.Errorf("orders: sumar líneas: %w", err)
	}
	subtotal := int32(sum) //nolint:gosec // la suma de hasta 100 líneas de hasta 100M c/u no desborda int32 en la práctica
	total := subtotal - current.DiscountCents + current.DeliveryFeeCents
	if total < 0 {
		total = 0
	}
	row, err := q.SetOrderAmounts(ctx, store.SetOrderAmountsParams{
		ID: orderID, OrganizationID: orgID, SubtotalCents: subtotal, TotalCents: total,
	})
	if err != nil {
		return store.Order{}, fmt.Errorf("orders: fijar montos: %w", err)
	}
	return row, nil
}

func (r *pgRepository) AddItem(ctx context.Context, orgID, orderID uuid.UUID, item NewItem) (Order, error) {
	var out Order
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		mods, err := json.Marshal(item.Modifiers)
		if err != nil {
			return fmt.Errorf("orders: codificar modificadores: %w", err)
		}
		var variantID uuid.NullUUID
		if item.ProductVariantID != nil {
			variantID = uuid.NullUUID{UUID: *item.ProductVariantID, Valid: true}
		}
		if _, err := q.InsertOrderItem(ctx, store.InsertOrderItemParams{
			OrderID: orderID, OrganizationID: orgID, ProductVariantID: variantID, NameSnapshot: item.Name,
			UnitPriceCentsSnapshot: item.UnitPriceCents, ModifiersSnapshot: mods, UnitTotalCents: item.UnitTotalCents,
			Quantity: int32(item.Quantity), LineTotalCents: item.LineTotalCents, //nolint:gosec // acotado por validateQuantity
		}); err != nil {
			return fmt.Errorf("orders: agregar línea: %w", err)
		}
		row, err := recalcCartAmounts(ctx, q, orgID, orderID)
		if err != nil {
			return err
		}
		out, err = withItems(ctx, q, toOrderBase(row))
		return err
	})
	return out, err
}

func (r *pgRepository) UpdateItemQuantity(ctx context.Context, orgID, orderID, itemID uuid.UUID, quantity int) (Order, error) {
	var out Order
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.UpdateOrderItemQuantity(ctx, store.UpdateOrderItemQuantityParams{
			ID: itemID, OrderID: orderID, OrganizationID: orgID, Quantity: int32(quantity), //nolint:gosec
		}); err != nil {
			if db.IsNoRows(err) {
				return ErrItemNotFound
			}
			return fmt.Errorf("orders: cambiar cantidad: %w", err)
		}
		row, err := recalcCartAmounts(ctx, q, orgID, orderID)
		if err != nil {
			return err
		}
		out, err = withItems(ctx, q, toOrderBase(row))
		return err
	})
	return out, err
}

func (r *pgRepository) RemoveItem(ctx context.Context, orgID, orderID, itemID uuid.UUID) (Order, error) {
	var out Order
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		n, err := q.DeleteOrderItem(ctx, store.DeleteOrderItemParams{ID: itemID, OrderID: orderID, OrganizationID: orgID})
		if err != nil {
			return fmt.Errorf("orders: quitar línea: %w", err)
		}
		if n == 0 {
			return ErrItemNotFound
		}
		row, err := recalcCartAmounts(ctx, q, orgID, orderID)
		if err != nil {
			return err
		}
		out, err = withItems(ctx, q, toOrderBase(row))
		return err
	})
	return out, err
}

func (r *pgRepository) Transition(ctx context.Context, orgID, id uuid.UUID, op Operation, place bool) (Order, error) {
	var out Order
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if place {
			if _, err := finalizeAmounts(ctx, q, orgID, id); err != nil {
				return err
			}
		}
		fromStatuses := make([]string, len(op.From))
		for i, s := range op.From {
			fromStatuses[i] = string(s)
		}
		row, err := q.TransitionOrder(ctx, store.TransitionOrderParams{
			Status: string(op.To), SetPlacedAt: place, ID: id, OrganizationID: orgID, FromStatuses: fromStatuses,
		})
		if db.IsNoRows(err) {
			// No coincidió ningún estado de origen: o no existe, o está en otro estado.
			current, gerr := q.GetOrder(ctx, store.GetOrderParams{ID: id, OrganizationID: orgID})
			if db.IsNoRows(gerr) {
				return ErrNotFound
			}
			if gerr != nil {
				return fmt.Errorf("orders: leer para diagnosticar transición: %w", gerr)
			}
			return &TransitionError{Op: op, From: Status(current.Status)}
		}
		if err != nil {
			return fmt.Errorf("orders: transición: %w", err)
		}
		out, err = withItems(ctx, q, toOrderBase(row))
		return err
	})
	return out, err
}

// ApplyPromotion y RemovePromotion las usa internal/promotions a través de OrdersGateway — ver el
// comentario de Repository. Ambas consultas exigen status = 'draft' en el WHERE (no aquí): un
// pedido que ya no es un carrito, o que no existe, devuelve ErrNotFound.
func (r *pgRepository) ApplyPromotion(ctx context.Context, orgID, orderID, promotionID uuid.UUID, discountCents, totalCents int32) (Order, error) {
	row, err := r.q.ApplyPromotionToOrder(ctx, store.ApplyPromotionToOrderParams{
		PromotionID: uuid.NullUUID{UUID: promotionID, Valid: true}, DiscountCents: discountCents,
		TotalCents: totalCents, ID: orderID, OrganizationID: orgID,
	})
	if db.IsNoRows(err) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("orders: aplicar promoción: %w", err)
	}
	return withItems(ctx, r.q, toOrderBase(row))
}

func (r *pgRepository) RemovePromotion(ctx context.Context, orgID, orderID uuid.UUID) (Order, error) {
	row, err := r.q.RemovePromotionFromOrder(ctx, store.RemovePromotionFromOrderParams{ID: orderID, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("orders: quitar promoción: %w", err)
	}
	return withItems(ctx, r.q, toOrderBase(row))
}

func (r *pgRepository) ListMyDrafts(ctx context.Context, customerID uuid.UUID) ([]DraftSummary, error) {
	rows, err := r.q.ListMyDraftOrders(ctx, customerID)
	if err != nil {
		return nil, fmt.Errorf("orders: listar mis carritos: %w", err)
	}
	out := make([]DraftSummary, len(rows))
	for i, row := range rows {
		out[i] = DraftSummary{
			OrderID: row.ID, OrganizationID: row.OrganizationID, OrganizationName: row.OrganizationName,
			OrganizationSlug: row.OrganizationSlug, ItemCount: int(row.ItemCount),
			TotalCents: row.TotalCents, UpdatedAt: row.UpdatedAt,
		}
	}
	return out, nil
}
