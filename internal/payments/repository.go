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
	breakdown := p.Breakdown
	if len(breakdown) == 0 {
		breakdown = []byte("{}")
	}
	return Payment{
		ID: p.ID, OrderID: p.OrderID, OrganizationID: p.OrganizationID, Status: Status(p.Status),
		AmountCents: p.AmountCents, Currency: p.Currency, Gateway: p.Gateway,
		CheckoutURL: p.CheckoutUrl, FailureReason: p.FailureReason,
		Method: p.Method, BaseCents: p.BaseCents, TransactionFeeCents: p.TransactionFeeCents,
		GatewayPlanCode: p.GatewayPlanCode, Breakdown: breakdown, SessionID: p.SessionID,
		MethodUsed: p.MethodUsed, MethodMismatch: p.MethodMismatch,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (r *pgRepository) Insert(ctx context.Context, n NewPayment) (Payment, error) {
	params := store.InsertPaymentParams{
		OrderID: n.OrderID, OrganizationID: n.OrganizationID, AmountCents: n.AmountCents, Currency: n.Currency,
		Gateway: n.Gateway, TransactionFeeCents: n.TransactionFeeCents, Breakdown: n.Breakdown,
	}
	if params.Breakdown == nil {
		params.Breakdown = []byte("{}")
	}
	if n.Method != "" {
		m := string(n.Method)
		params.Method = &m
		base := n.BaseCents
		params.BaseCents = &base
	}
	if n.GatewayPlanCode != "" {
		code := n.GatewayPlanCode
		params.GatewayPlanCode = &code
	}
	p, err := r.q.InsertPayment(ctx, params)
	if err != nil {
		return Payment{}, fmt.Errorf("payments: crear: %w", err)
	}
	return toPayment(p), nil
}

func (r *pgRepository) SetSession(ctx context.Context, id uuid.UUID, sessionID string) (Payment, error) {
	p, err := r.q.SetPaymentSession(ctx, store.SetPaymentSessionParams{ID: id, SessionID: &sessionID})
	if db.IsNoRows(err) {
		return Payment{}, ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("payments: guardar sesión: %w", err)
	}
	return toPayment(p), nil
}

func (r *pgRepository) SetMethodUsed(ctx context.Context, id uuid.UUID, methodUsed string, mismatch bool) (Payment, error) {
	p, err := r.q.SetPaymentMethodUsed(ctx, store.SetPaymentMethodUsedParams{ID: id, MethodUsed: &methodUsed, MethodMismatch: mismatch})
	if db.IsNoRows(err) {
		return Payment{}, ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("payments: guardar medio usado: %w", err)
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
