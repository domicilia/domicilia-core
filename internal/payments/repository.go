package payments

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

type pgRepository struct{ q *store.Queries }

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository { return &pgRepository{q: store.New(pool)} }

func toPayment(p store.Payment) Payment {
	return Payment{
		ID: p.ID, OrderID: p.OrderID, OrganizationID: p.OrganizationID, Status: Status(p.Status),
		AmountCents: p.AmountCents, Currency: p.Currency, Gateway: p.Gateway,
		CheckoutURL: p.CheckoutUrl, FailureReason: p.FailureReason,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (r *pgRepository) Insert(ctx context.Context, orderID, orgID uuid.UUID, amountCents int32, currency, gateway string) (Payment, error) {
	p, err := r.q.InsertPayment(ctx, store.InsertPaymentParams{
		OrderID: orderID, OrganizationID: orgID, AmountCents: amountCents, Currency: currency, Gateway: gateway,
	})
	if err != nil {
		return Payment{}, fmt.Errorf("payments: crear: %w", err)
	}
	return toPayment(p), nil
}

func (r *pgRepository) Get(ctx context.Context, orgID, id uuid.UUID) (Payment, error) {
	p, err := r.q.GetPayment(ctx, store.GetPaymentParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Payment{}, ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("payments: leer: %w", err)
	}
	return toPayment(p), nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (Payment, error) {
	p, err := r.q.GetPaymentByID(ctx, id)
	if db.IsNoRows(err) {
		return Payment{}, ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("payments: leer por id: %w", err)
	}
	return toPayment(p), nil
}

func (r *pgRepository) ListByOrder(ctx context.Context, orgID, orderID uuid.UUID) ([]Payment, error) {
	rows, err := r.q.ListPaymentsByOrder(ctx, store.ListPaymentsByOrderParams{OrderID: orderID, OrganizationID: orgID})
	if err != nil {
		return nil, fmt.Errorf("payments: listar del pedido: %w", err)
	}
	out := make([]Payment, len(rows))
	for i, row := range rows {
		out[i] = toPayment(row)
	}
	return out, nil
}

func (r *pgRepository) SetCheckoutURL(ctx context.Context, id uuid.UUID, url string) (Payment, error) {
	p, err := r.q.SetPaymentCheckoutURL(ctx, store.SetPaymentCheckoutURLParams{ID: id, CheckoutUrl: &url})
	if db.IsNoRows(err) {
		return Payment{}, ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("payments: fijar url de pago: %w", err)
	}
	return toPayment(p), nil
}

func (r *pgRepository) Settle(ctx context.Context, id uuid.UUID, status Status, failureReason *string) (Payment, error) {
	p, err := r.q.SettlePayment(ctx, store.SettlePaymentParams{ID: id, Status: string(status), FailureReason: failureReason})
	if db.IsNoRows(err) {
		return Payment{}, ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("payments: resolver: %w", err)
	}
	return toPayment(p), nil
}

func (r *pgRepository) RecordEvent(ctx context.Context, source, eventKey string, payload []byte) (bool, error) {
	n, err := r.q.InsertPaymentEvent(ctx, store.InsertPaymentEventParams{Source: source, EventKey: eventKey, Payload: payload})
	if err != nil {
		return false, fmt.Errorf("payments: registrar evento: %w", err)
	}
	return n > 0, nil
}
