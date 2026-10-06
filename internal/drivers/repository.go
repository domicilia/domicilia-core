package drivers

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

type pgRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool, q: store.New(pool)}
}

func toApplication(a store.DriverApplication) Application {
	return Application{
		ID:          a.ID,
		FullName:    a.FullName,
		Email:       a.Email,
		Phone:       a.Phone,
		VehicleType: a.VehicleType,
		Status:      Status(a.Status),
	}
}

func (r *pgRepository) Create(ctx context.Context, in ApplyInput) (Application, error) {
	a, err := r.q.InsertDriverApplication(ctx, store.InsertDriverApplicationParams{
		ID: uuid.New(), FullName: in.FullName, Email: in.Email, Phone: in.Phone, VehicleType: in.VehicleType,
	})
	if err != nil {
		return Application{}, fmt.Errorf("drivers: crear solicitud: %w", err)
	}
	return toApplication(a), nil
}

func (r *pgRepository) List(ctx context.Context, status *Status) ([]Application, error) {
	var filter *store.ApplicationStatus
	if status != nil {
		st := store.ApplicationStatus(*status)
		filter = &st
	}
	rows, err := r.q.ListDriverApplications(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("drivers: listar solicitudes: %w", err)
	}
	out := make([]Application, 0, len(rows))
	for _, a := range rows {
		out = append(out, toApplication(a))
	}
	return out, nil
}

func (r *pgRepository) Get(ctx context.Context, id uuid.UUID) (Application, error) {
	a, err := r.q.GetDriverApplication(ctx, id)
	if db.IsNoRows(err) {
		return Application{}, ErrNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("drivers: leer solicitud: %w", err)
	}
	return toApplication(a), nil
}

func (r *pgRepository) EmailRegistered(ctx context.Context, email string) (bool, error) {
	ok, err := r.q.UserEmailExists(ctx, email)
	if err != nil {
		return false, fmt.Errorf("drivers: comprobar correo: %w", err)
	}
	return ok, nil
}

// review pasa una solicitud pendiente a un estado final. Sin filas puede ser que
// no exista o que ya estuviera revisada: se distingue leyéndola.
func review(ctx context.Context, q *store.Queries, id uuid.UUID, status store.ApplicationStatus) (store.DriverApplication, error) {
	a, err := q.ReviewDriverApplication(ctx, store.ReviewDriverApplicationParams{ID: id, Status: status})
	if !db.IsNoRows(err) {
		return a, err
	}
	if _, gerr := q.GetDriverApplication(ctx, id); gerr != nil {
		if db.IsNoRows(gerr) {
			return store.DriverApplication{}, ErrNotFound
		}
		return store.DriverApplication{}, gerr
	}
	return store.DriverApplication{}, ErrNotPending
}

func (r *pgRepository) Reject(ctx context.Context, id uuid.UUID) (Application, error) {
	a, err := review(ctx, r.q, id, store.ApplicationStatusRejected)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotPending) {
			return Application{}, err
		}
		return Application{}, fmt.Errorf("drivers: rechazar solicitud: %w", err)
	}
	return toApplication(a), nil
}

func (r *pgRepository) Approve(ctx context.Context, id, userID, actor uuid.UUID) (Application, error) {
	var out Application
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		// Primero se reclama la solicitud: si otra petición la aprobó a la vez,
		// esta espera el bloqueo de fila y luego ve que ya no está pendiente.
		a, err := review(ctx, q, id, store.ApplicationStatusApproved)
		if err != nil {
			return err
		}
		if _, err := q.InsertUser(ctx, store.InsertUserParams{
			ID: userID, Email: a.Email, FullName: &a.FullName,
		}); err != nil {
			return err
		}
		// Ser domiciliario es el rol `delivery`.
		if _, err := q.InsertUserPlatformRoleByCode(ctx, store.InsertUserPlatformRoleByCodeParams{
			UserID: userID, Code: access.RoleDelivery, GrantedBy: uuid.NullUUID{UUID: actor, Valid: actor != uuid.Nil},
		}); err != nil {
			return err
		}
		out = toApplication(a)
		return audit.Entry{
			ActorID: actor, Action: audit.PlatformRoleGranted, TargetUserID: userID, RoleCode: access.RoleDelivery,
			Detail: map[string]any{"via": "driver-application", "application_id": id},
		}.Insert(ctx, q)
	})
	switch {
	case err == nil:
		return out, nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNotPending):
		return Application{}, err
	case db.IsUniqueViolation(err):
		return Application{}, ErrEmailTaken
	default:
		return Application{}, fmt.Errorf("drivers: aprobar solicitud: %w", err)
	}
}
