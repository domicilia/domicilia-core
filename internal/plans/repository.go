package plans

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound = errors.New("plans: organización no encontrada")
	ErrArchived = errors.New("plans: organización archivada")
	ErrSamePlan = errors.New("plans: ya tiene ese plan")
)

// State es lo que hace falta saber de una organización para decidir qué puede usar.
type State struct {
	Tier      Tier
	Status    string
	Overrides []Override
}

// Subscription es un periodo con un plan. EndedAt nil es la vigente.
type Subscription struct {
	ID        uuid.UUID  `json:"id"`
	PlanTier  string     `json:"plan_tier"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	ChangedBy *uuid.UUID `json:"changed_by"`
	Reason    *string    `json:"reason"`
}

// Usage es lo que la organización ya consume de sus límites.
type Usage struct {
	Members            int64 `json:"members"`
	PendingInvitations int64 `json:"pending_invitations"`
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	State(ctx context.Context, orgID uuid.UUID) (State, error)
	Subscriptions(ctx context.Context, orgID uuid.UUID) ([]Subscription, error)
	Usage(ctx context.Context, orgID uuid.UUID) (Usage, error)
	// ChangePlan cierra la suscripción vigente, abre la nueva, actualiza el plan de la
	// organización y audita, todo en una transacción. Devuelve la nueva suscripción.
	ChangePlan(ctx context.Context, orgID uuid.UUID, to Tier, actor uuid.UUID, reason *string) (Subscription, error)
	SetOverride(ctx context.Context, orgID uuid.UUID, o Override, actor uuid.UUID) error
	// ClearOverride devuelve false si no había excepción.
	ClearOverride(ctx context.Context, orgID uuid.UUID, f Feature, actor uuid.UUID) (bool, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool, q: store.New(pool)}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	return &n.UUID
}

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

func toSubscription(s store.OrganizationSubscription) Subscription {
	return Subscription{
		ID: s.ID, PlanTier: s.PlanTier, StartedAt: s.StartedAt, EndedAt: timePtr(s.EndedAt),
		ChangedBy: uuidPtr(s.ChangedBy), Reason: s.Reason,
	}
}

func (r *pgRepository) State(ctx context.Context, orgID uuid.UUID) (State, error) {
	o, err := r.q.GetOrganization(ctx, orgID)
	if db.IsNoRows(err) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("plans: leer organización: %w", err)
	}
	rows, err := r.q.ListFeatureOverrides(ctx, orgID)
	if err != nil {
		return State{}, fmt.Errorf("plans: leer excepciones: %w", err)
	}
	st := State{Tier: Tier(o.PlanTier), Status: o.Status, Overrides: make([]Override, 0, len(rows))}
	for _, row := range rows {
		st.Overrides = append(st.Overrides, Override{Feature: Feature(row.Feature), Enabled: row.Enabled, Reason: row.Reason})
	}
	return st, nil
}

func (r *pgRepository) Subscriptions(ctx context.Context, orgID uuid.UUID) ([]Subscription, error) {
	rows, err := r.q.ListSubscriptions(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("plans: listar suscripciones: %w", err)
	}
	out := make([]Subscription, 0, len(rows))
	for _, s := range rows {
		out = append(out, toSubscription(s))
	}
	return out, nil
}

func (r *pgRepository) Usage(ctx context.Context, orgID uuid.UUID) (Usage, error) {
	members, err := r.q.CountMembers(ctx, orgID)
	if err != nil {
		return Usage{}, fmt.Errorf("plans: contar miembros: %w", err)
	}
	pending, err := r.q.CountPendingInvitations(ctx, orgID)
	if err != nil {
		return Usage{}, fmt.Errorf("plans: contar invitaciones: %w", err)
	}
	return Usage{Members: members, PendingInvitations: pending}, nil
}

func (r *pgRepository) ChangePlan(ctx context.Context, orgID uuid.UUID, to Tier, actor uuid.UUID, reason *string) (Subscription, error) {
	var sub Subscription
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		org, err := q.GetOrganizationForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		if org.Status == "archived" {
			return ErrArchived
		}
		if org.PlanTier == string(to) {
			return ErrSamePlan
		}
		if _, err := q.EndCurrentSubscription(ctx, orgID); err != nil {
			return err
		}
		row, err := q.InsertSubscription(ctx, store.InsertSubscriptionParams{
			OrganizationID: orgID, PlanTier: string(to), ChangedBy: nullUUID(actor), Reason: reason,
		})
		if err != nil {
			return err
		}
		if _, err := q.SetOrganizationPlan(ctx, store.SetOrganizationPlanParams{ID: orgID, PlanTier: string(to)}); err != nil {
			return err
		}
		sub = toSubscription(row)
		detail := map[string]any{"from": org.PlanTier, "to": string(to)}
		if reason != nil {
			detail["reason"] = *reason
		}
		return audit.Entry{ActorID: actor, Action: audit.OrganizationPlanChanged, OrganizationID: orgID, Detail: detail}.Insert(ctx, q)
	})
	switch {
	case db.IsNoRows(err):
		return Subscription{}, ErrNotFound
	case errors.Is(err, ErrArchived), errors.Is(err, ErrSamePlan):
		return Subscription{}, err
	case err != nil:
		return Subscription{}, fmt.Errorf("plans: cambiar plan: %w", err)
	}
	return sub, nil
}

func (r *pgRepository) SetOverride(ctx context.Context, orgID uuid.UUID, o Override, actor uuid.UUID) error {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.UpsertFeatureOverride(ctx, store.UpsertFeatureOverrideParams{
			OrganizationID: orgID, Feature: string(o.Feature), Enabled: o.Enabled, Reason: o.Reason, CreatedBy: nullUUID(actor),
		}); err != nil {
			return err
		}
		detail := map[string]any{"feature": string(o.Feature), "enabled": o.Enabled}
		if o.Reason != nil {
			detail["reason"] = *o.Reason
		}
		return audit.Entry{ActorID: actor, Action: audit.FeatureOverrideSet, OrganizationID: orgID, Detail: detail}.Insert(ctx, q)
	})
	if db.IsForeignKeyViolation(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("plans: fijar excepción: %w", err)
	}
	return nil
}

func (r *pgRepository) ClearOverride(ctx context.Context, orgID uuid.UUID, f Feature, actor uuid.UUID) (bool, error) {
	cleared := false
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		n, err := q.DeleteFeatureOverride(ctx, store.DeleteFeatureOverrideParams{OrganizationID: orgID, Feature: string(f)})
		if err != nil || n == 0 {
			return err
		}
		cleared = true
		return audit.Entry{ActorID: actor, Action: audit.FeatureOverrideCleared, OrganizationID: orgID,
			Detail: map[string]any{"feature": string(f)}}.Insert(ctx, q)
	})
	if err != nil {
		return false, fmt.Errorf("plans: quitar excepción: %w", err)
	}
	return cleared, nil
}
