package customers

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

// Nombres de las restricciones (ver db/migrations).
const (
	constraintUserPK    = "users_pkey"
	constraintUserEmail = "ix_users_email"
)

type pgRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool, q: store.New(pool)}
}

func (r *pgRepository) SignUp(ctx context.Context, id uuid.UUID, email string, in SignUpInput) (Registration, error) {
	var out Registration
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		u, err := q.InsertUser(ctx, store.InsertUserParams{ID: id, Email: email, FullName: in.FullName})
		if err != nil {
			return err
		}
		if _, err := q.InsertCustomer(ctx, store.InsertCustomerParams{
			UserID: u.ID, Phone: in.Phone, DefaultAddress: in.DefaultAddress,
		}); err != nil {
			return err
		}
		if _, err := q.InsertUserPlatformRoleByCode(ctx, store.InsertUserPlatformRoleByCodeParams{
			UserID: u.ID, Code: access.RoleCustomer,
		}); err != nil {
			return err
		}
		out = Registration{UserID: u.ID, Email: u.Email, FullName: u.FullName}
		return nil
	})
	if db.IsUniqueViolation(err) {
		if db.ConstraintName(err) == constraintUserEmail {
			return Registration{}, ErrEmailTaken
		}
		if db.ConstraintName(err) == constraintUserPK {
			return Registration{}, ErrProfileExists
		}
	}
	if err != nil {
		return Registration{}, fmt.Errorf("customers: alta de cliente: %w", err)
	}
	return out, nil
}

func (r *pgRepository) Get(ctx context.Context, userID uuid.UUID) (Profile, error) {
	c, err := r.q.GetCustomer(ctx, userID)
	if db.IsNoRows(err) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("customers: leer perfil: %w", err)
	}
	return Profile{Phone: c.Phone, DefaultAddress: c.DefaultAddress}, nil
}

func (r *pgRepository) Update(ctx context.Context, userID uuid.UUID, in ProfileInput) (Profile, error) {
	n, err := r.q.UpdateCustomer(ctx, store.UpdateCustomerParams{
		UserID: userID, Phone: in.Phone, DefaultAddress: in.DefaultAddress,
	})
	if err != nil {
		return Profile{}, fmt.Errorf("customers: actualizar perfil: %w", err)
	}
	if n == 0 {
		return Profile{}, ErrNotFound
	}
	return r.Get(ctx, userID)
}
