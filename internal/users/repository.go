package users

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

// withRoles arma el usuario con sus roles de plataforma.
func withRoles(ctx context.Context, q *store.Queries, u store.User) (User, error) {
	rows, err := q.ListUserPlatformRoles(ctx, u.ID)
	if err != nil {
		return User{}, fmt.Errorf("users: roles de plataforma: %w", err)
	}
	refs := make([]access.RoleRef, 0, len(rows))
	for _, r := range rows {
		refs = append(refs, access.RoleRef{ID: r.RoleID.String(), Code: r.RoleCode, Name: r.RoleName})
	}
	return NewUser(u.ID, u.Email, u.FullName, u.IsActive, refs), nil
}

func (r *pgRepository) Get(ctx context.Context, id uuid.UUID) (User, error) {
	u, err := r.q.GetUser(ctx, id)
	if db.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("users: leer usuario: %w", err)
	}
	return withRoles(ctx, r.q, u)
}

func (r *pgRepository) Memberships(ctx context.Context, id uuid.UUID) ([]MembershipBrief, error) {
	rows, err := r.q.ListMembershipsByUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("users: leer organizaciones: %w", err)
	}
	out := make([]MembershipBrief, 0, len(rows))
	for _, row := range rows {
		out = append(out, MembershipBrief{
			OrganizationID:     row.OrganizationID,
			OrganizationName:   row.OrganizationName,
			OrganizationSlug:   row.OrganizationSlug,
			OrganizationStatus: row.OrganizationStatus,
			PlanTier:           row.PlanTier,
			Role:               row.RoleCode,
			RoleID:             row.RoleID,
			RoleName:           row.RoleName,
		})
	}
	return out, nil
}

func (r *pgRepository) UpdateName(ctx context.Context, id uuid.UUID, fullName *string) (User, error) {
	u, err := r.q.UpdateUserFullName(ctx, store.UpdateUserFullNameParams{ID: id, FullName: fullName})
	if db.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("users: actualizar nombre: %w", err)
	}
	return withRoles(ctx, r.q, u)
}

func (r *pgRepository) List(ctx context.Context, f ListFilter) ([]User, int64, error) {
	p := store.ListUsersParams{PageSize: int32(f.Limit), PageOffset: int32(f.Offset)} //nolint:gosec // acotado por el paginador
	c := store.CountUsersFilteredParams{}
	if f.Search != "" {
		s := db.EscapeLike(f.Search)
		p.Search, c.Search = &s, &s
	}
	if f.Role != "" {
		role := f.Role
		p.Role, c.Role = &role, &role
	}
	if f.OrganizationID != nil {
		org := uuid.NullUUID{UUID: *f.OrganizationID, Valid: true}
		p.OrganizationID, c.OrganizationID = org, org
	}
	if f.IsActive != nil {
		p.IsActive, c.IsActive = f.IsActive, f.IsActive
	}

	rows, err := r.q.ListUsers(ctx, p)
	if err != nil {
		return nil, 0, fmt.Errorf("users: listar: %w", err)
	}
	total, err := r.q.CountUsersFiltered(ctx, c)
	if err != nil {
		return nil, 0, fmt.Errorf("users: contar: %w", err)
	}

	// Los roles de toda la página en una sola consulta.
	ids := make([]uuid.UUID, len(rows))
	for i, u := range rows {
		ids[i] = u.ID
	}
	byUser := map[uuid.UUID][]access.RoleRef{}
	if len(ids) > 0 {
		rr, err := r.q.ListPlatformRolesForUsers(ctx, ids)
		if err != nil {
			return nil, 0, fmt.Errorf("users: roles de la página: %w", err)
		}
		for _, x := range rr {
			byUser[x.UserID] = append(byUser[x.UserID], access.RoleRef{ID: x.RoleID.String(), Code: x.RoleCode, Name: x.RoleName})
		}
	}
	out := make([]User, 0, len(rows))
	for _, u := range rows {
		out = append(out, NewUser(u.ID, u.Email, u.FullName, u.IsActive, byUser[u.ID]))
	}
	return out, total, nil
}

func (r *pgRepository) SetActive(ctx context.Context, id uuid.UUID, active bool, actor uuid.UUID) (User, error) {
	var out store.User
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if !active {
			// Bloquea antes de tocar: dos desactivaciones simultáneas no pueden dejar
			// la plataforma sin superadmin activo.
			if err := q.LockSuperadmins(ctx); err != nil {
				return err
			}
		}
		u, err := q.SetUserActive(ctx, store.SetUserActiveParams{ID: id, IsActive: active})
		if err != nil {
			return err
		}
		if !active {
			left, err := q.CountActiveUsersWithPlatformRole(ctx, access.RoleSuperadmin)
			if err != nil {
				return err
			}
			if left == 0 {
				holds, err := q.CountUsersWithPlatformRole(ctx, access.RoleSuperadmin)
				if err != nil {
					return err
				}
				if holds > 0 {
					return ErrLastSuperadmin // se revierte: la cuenta sigue activa
				}
			}
		}
		action := audit.UserActivated
		if !active {
			action = audit.UserDeactivated
		}
		out = u
		return audit.Entry{ActorID: actor, Action: action, TargetUserID: id}.Insert(ctx, q)
	})
	switch {
	case db.IsNoRows(err):
		return User{}, ErrNotFound
	case errors.Is(err, ErrLastSuperadmin):
		return User{}, ErrLastSuperadmin
	case err != nil:
		return User{}, fmt.Errorf("users: cambiar estado: %w", err)
	}
	return withRoles(ctx, r.q, out)
}

func (r *pgRepository) Counts(ctx context.Context) (total, delivery int64, err error) {
	total, err = r.q.CountUsers(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("users: contar usuarios: %w", err)
	}
	delivery, err = r.q.CountUsersWithPlatformRole(ctx, access.RoleDelivery)
	if err != nil {
		return 0, 0, fmt.Errorf("users: contar domiciliarios: %w", err)
	}
	return total, delivery, nil
}
