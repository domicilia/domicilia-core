package promotions

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

type pgRepository struct{ q *store.Queries }

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository { return &pgRepository{q: store.New(pool)} }

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func toTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func intPtr32(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func int32Ptr(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(*n) //nolint:gosec // límites de uso acotados por validate.go, no cantidades de dinero
	return &v
}

func toPromotion(p store.Promotion) Promotion {
	return Promotion{
		ID: p.ID, OrganizationID: p.OrganizationID, Code: p.Code, DiscountType: p.DiscountType,
		Value: p.Value, MinOrderCents: p.MinOrderCents,
		StartsAt: timePtr(p.StartsAt), EndsAt: timePtr(p.EndsAt),
		MaxUses: intPtr32(p.MaxUses), PerCustomerLimit: intPtr32(p.PerCustomerLimit),
		IsActive: p.IsActive, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (r *pgRepository) Insert(ctx context.Context, orgID uuid.UUID, n NewPromotion) (Promotion, error) {
	p, err := r.q.InsertPromotion(ctx, store.InsertPromotionParams{
		OrganizationID: orgID, Code: n.Code, DiscountType: n.DiscountType, Value: n.Value,
		MinOrderCents: n.MinOrderCents, StartsAt: toTimestamptz(n.StartsAt), EndsAt: toTimestamptz(n.EndsAt),
		MaxUses: int32Ptr(n.MaxUses), PerCustomerLimit: int32Ptr(n.PerCustomerLimit),
	})
	if db.IsUniqueViolation(err) {
		return Promotion{}, ErrDuplicateCode
	}
	if err != nil {
		return Promotion{}, fmt.Errorf("promotions: crear: %w", err)
	}
	return toPromotion(p), nil
}

func (r *pgRepository) Get(ctx context.Context, orgID, id uuid.UUID) (Promotion, error) {
	p, err := r.q.GetPromotion(ctx, store.GetPromotionParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Promotion{}, ErrNotFound
	}
	if err != nil {
		return Promotion{}, fmt.Errorf("promotions: leer: %w", err)
	}
	return toPromotion(p), nil
}

func (r *pgRepository) GetActiveByCode(ctx context.Context, orgID uuid.UUID, code string) (Promotion, error) {
	p, err := r.q.GetActivePromotionByCode(ctx, store.GetActivePromotionByCodeParams{OrganizationID: orgID, Code: code})
	if db.IsNoRows(err) {
		return Promotion{}, ErrNotFound
	}
	if err != nil {
		return Promotion{}, fmt.Errorf("promotions: leer por código: %w", err)
	}
	return toPromotion(p), nil
}

func (r *pgRepository) List(ctx context.Context, orgID uuid.UUID) ([]Promotion, error) {
	rows, err := r.q.ListPromotions(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("promotions: listar: %w", err)
	}
	out := make([]Promotion, len(rows))
	for i, row := range rows {
		out[i] = toPromotion(row)
	}
	return out, nil
}

func (r *pgRepository) Update(ctx context.Context, orgID, id uuid.UUID, p Patch) (Promotion, error) {
	row, err := r.q.UpdatePromotion(ctx, store.UpdatePromotionParams{
		ID: id, OrganizationID: orgID,
		Code: p.Code, DiscountType: p.DiscountType, Value: p.Value, MinOrderCents: p.MinOrderCents,
		SetStartsAt: p.SetStartsAt, StartsAt: toTimestamptz(p.StartsAt),
		SetEndsAt: p.SetEndsAt, EndsAt: toTimestamptz(p.EndsAt),
		SetMaxUses: p.SetMaxUses, MaxUses: int32Ptr(p.MaxUses),
		SetPerCustomerLimit: p.SetPerCustomerLimit, PerCustomerLimit: int32Ptr(p.PerCustomerLimit),
		IsActive: p.IsActive,
	})
	if db.IsNoRows(err) {
		return Promotion{}, ErrNotFound
	}
	if db.IsUniqueViolation(err) {
		return Promotion{}, ErrDuplicateCode
	}
	if err != nil {
		return Promotion{}, fmt.Errorf("promotions: actualizar: %w", err)
	}
	return toPromotion(row), nil
}

func (r *pgRepository) CountRedemptions(ctx context.Context, promotionID uuid.UUID) (int64, error) {
	n, err := r.q.CountPromotionRedemptions(ctx, promotionID)
	if err != nil {
		return 0, fmt.Errorf("promotions: contar canjes: %w", err)
	}
	return n, nil
}

func (r *pgRepository) CountCustomerRedemptions(ctx context.Context, promotionID, customerID uuid.UUID) (int64, error) {
	n, err := r.q.CountCustomerPromotionRedemptions(ctx, store.CountCustomerPromotionRedemptionsParams{
		PromotionID: promotionID, CustomerID: customerID,
	})
	if err != nil {
		return 0, fmt.Errorf("promotions: contar canjes del cliente: %w", err)
	}
	return n, nil
}

func (r *pgRepository) RecordRedemption(ctx context.Context, orgID, promotionID, orderID, customerID uuid.UUID, discountCents int32) error {
	if err := r.q.DeletePromotionRedemptionByOrder(ctx, orderID); err != nil {
		return fmt.Errorf("promotions: soltar canje anterior: %w", err)
	}
	if _, err := r.q.InsertPromotionRedemption(ctx, store.InsertPromotionRedemptionParams{
		PromotionID: promotionID, OrganizationID: orgID, OrderID: orderID, CustomerID: customerID, DiscountCents: discountCents,
	}); err != nil {
		return fmt.Errorf("promotions: registrar canje: %w", err)
	}
	return nil
}
