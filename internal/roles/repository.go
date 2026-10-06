package roles

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

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	Role(ctx context.Context, id uuid.UUID) (Role, error)
	// SystemRole busca un rol SIN organización por alcance y código (los de sistema
	// y los personalizados de plataforma).
	SystemRole(ctx context.Context, scope access.Scope, code string) (Role, error)
	OrgRole(ctx context.Context, orgID uuid.UUID, code string) (Role, error)
	List(ctx context.Context, scope *access.Scope, orgID *uuid.UUID) ([]Role, error)
	ListForOrganization(ctx context.Context, orgID uuid.UUID) ([]Role, error)

	OrganizationExists(ctx context.Context, id uuid.UUID) (bool, error)
	UserExists(ctx context.Context, id uuid.UUID) (bool, error)

	// Create devuelve ErrDuplicate si el código ya existe en ese ámbito.
	Create(ctx context.Context, r NewRole, actor uuid.UUID) (Role, error)
	// Update devuelve ErrNotFound o ErrSystemRole.
	Update(ctx context.Context, id uuid.UUID, p Patch, actor uuid.UUID) (Role, error)
	// Delete devuelve ErrNotFound, ErrSystemRole o ErrInUse.
	Delete(ctx context.Context, id, actor uuid.UUID) error

	PlatformRoles(ctx context.Context, userID uuid.UUID) ([]access.RoleRef, error)
	// Grant da un rol de plataforma. false si ya lo tenía (no se audita).
	Grant(ctx context.Context, userID uuid.UUID, role Role, actor uuid.UUID) (bool, error)
	// Revoke quita un rol de plataforma. false si no lo tenía. Devuelve
	// ErrLastSuperadmin si dejaría la plataforma sin un superadmin activo.
	Revoke(ctx context.Context, userID uuid.UUID, role Role, actor uuid.UUID) (bool, error)

	Audit(ctx context.Context, f audit.Filter) ([]audit.Record, int64, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool, q: store.New(pool)}
}

func nullable(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	return &id.UUID
}

// load arma los roles con sus permisos en una sola consulta de permisos.
func load(ctx context.Context, q *store.Queries, rows []store.Role) ([]Role, error) {
	out := make([]Role, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	perms, err := q.ListRolePermissions(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("roles: permisos: %w", err)
	}
	byRole := make(map[uuid.UUID][]string, len(rows))
	for _, p := range perms {
		byRole[p.RoleID] = append(byRole[p.RoleID], p.PermissionCode)
	}
	for _, r := range rows {
		ps := byRole[r.ID]
		if ps == nil {
			ps = []string{}
		}
		out = append(out, Role{
			ID:             r.ID,
			Code:           r.Code,
			Name:           r.Name,
			Description:    r.Description,
			Scope:          access.Scope(r.Scope),
			OrganizationID: nullable(r.OrganizationID),
			IsSystem:       r.IsSystem,
			OrgAssignable:  r.OrgAssignable,
			Permissions:    ps,
		})
	}
	return out, nil
}

func loadOne(ctx context.Context, q *store.Queries, r store.Role) (Role, error) {
	rs, err := load(ctx, q, []store.Role{r})
	if err != nil {
		return Role{}, err
	}
	return rs[0], nil
}

func (r *pgRepository) one(ctx context.Context, row store.Role, err error) (Role, error) {
	if db.IsNoRows(err) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, fmt.Errorf("roles: leer: %w", err)
	}
	return loadOne(ctx, r.q, row)
}

func (r *pgRepository) Role(ctx context.Context, id uuid.UUID) (Role, error) {
	row, err := r.q.GetRole(ctx, id)
	return r.one(ctx, row, err)
}

func (r *pgRepository) SystemRole(ctx context.Context, scope access.Scope, code string) (Role, error) {
	row, err := r.q.GetSystemRoleByCode(ctx, store.GetSystemRoleByCodeParams{Scope: string(scope), Code: code})
	return r.one(ctx, row, err)
}

func (r *pgRepository) OrgRole(ctx context.Context, orgID uuid.UUID, code string) (Role, error) {
	row, err := r.q.GetOrgRoleByCode(ctx, store.GetOrgRoleByCodeParams{
		OrganizationID: uuid.NullUUID{UUID: orgID, Valid: true}, Code: code,
	})
	return r.one(ctx, row, err)
}

func (r *pgRepository) List(ctx context.Context, scope *access.Scope, orgID *uuid.UUID) ([]Role, error) {
	p := store.ListRolesParams{}
	if scope != nil {
		s := string(*scope)
		p.Scope = &s
	}
	if orgID != nil {
		p.OrganizationID = uuid.NullUUID{UUID: *orgID, Valid: true}
	}
	rows, err := r.q.ListRoles(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("roles: listar: %w", err)
	}
	return load(ctx, r.q, rows)
}

func (r *pgRepository) ListForOrganization(ctx context.Context, orgID uuid.UUID) ([]Role, error) {
	rows, err := r.q.ListRolesForOrganization(ctx, uuid.NullUUID{UUID: orgID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("roles: listar los de la organización: %w", err)
	}
	return load(ctx, r.q, rows)
}

func (r *pgRepository) OrganizationExists(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := r.q.GetOrganization(ctx, id)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("roles: comprobar organización: %w", err)
	}
	return true, nil
}

func (r *pgRepository) UserExists(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := r.q.GetUser(ctx, id)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("roles: comprobar usuario: %w", err)
	}
	return true, nil
}

func (r *pgRepository) Create(ctx context.Context, n NewRole, actor uuid.UUID) (Role, error) {
	var out Role
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		row, err := q.InsertRole(ctx, store.InsertRoleParams{
			Code:           n.Code,
			Name:           n.Name,
			Description:    n.Description,
			Scope:          string(n.Scope),
			OrganizationID: uuid.NullUUID{UUID: n.OrganizationID, Valid: n.OrganizationID != uuid.Nil},
			OrgAssignable:  n.OrgAssignable,
		})
		if err != nil {
			return err
		}
		if len(n.Permissions) > 0 {
			if err := q.InsertRolePermissions(ctx, store.InsertRolePermissionsParams{RoleID: row.ID, PermissionCodes: n.Permissions}); err != nil {
				return err
			}
		}
		if out, err = loadOne(ctx, q, row); err != nil {
			return err
		}
		return audit.Entry{
			ActorID: actor, Action: audit.RoleCreated, OrganizationID: n.OrganizationID,
			RoleID: row.ID, RoleCode: row.Code,
			Detail: map[string]any{"scope": n.Scope, "permissions": n.Permissions},
		}.Insert(ctx, q)
	})
	if db.IsUniqueViolation(err) {
		return Role{}, ErrDuplicate
	}
	if err != nil {
		return Role{}, fmt.Errorf("roles: crear: %w", err)
	}
	return out, nil
}

func (r *pgRepository) Update(ctx context.Context, id uuid.UUID, p Patch, actor uuid.UUID) (Role, error) {
	var out Role
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		cur, err := q.GetRole(ctx, id)
		if err != nil {
			return err
		}
		if cur.IsSystem {
			return ErrSystemRole
		}
		row, err := q.UpdateRole(ctx, store.UpdateRoleParams{
			ID: id, Name: p.Name, Description: p.Description, OrgAssignable: p.OrgAssignable,
		})
		if err != nil {
			return err
		}
		detail := map[string]any{}
		if p.Permissions != nil {
			if err := q.DeleteRolePermissions(ctx, id); err != nil {
				return err
			}
			if len(*p.Permissions) > 0 {
				if err := q.InsertRolePermissions(ctx, store.InsertRolePermissionsParams{RoleID: id, PermissionCodes: *p.Permissions}); err != nil {
					return err
				}
			}
			detail["permissions"] = *p.Permissions
		}
		if p.Name != nil {
			detail["name"] = *p.Name
		}
		if out, err = loadOne(ctx, q, row); err != nil {
			return err
		}
		return audit.Entry{
			ActorID: actor, Action: audit.RoleUpdated, OrganizationID: derefUUID(nullable(cur.OrganizationID)),
			RoleID: id, RoleCode: cur.Code, Detail: detail,
		}.Insert(ctx, q)
	})
	switch {
	case db.IsNoRows(err):
		return Role{}, ErrNotFound
	case errors.Is(err, ErrSystemRole):
		return Role{}, ErrSystemRole
	case err != nil:
		return Role{}, fmt.Errorf("roles: actualizar: %w", err)
	}
	return out, nil
}

func derefUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func (r *pgRepository) Delete(ctx context.Context, id, actor uuid.UUID) error {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		cur, err := q.GetRole(ctx, id)
		if err != nil {
			return err
		}
		if cur.IsSystem {
			return ErrSystemRole
		}
		members, err := q.CountMembersWithRole(ctx, id)
		if err != nil {
			return err
		}
		holders, err := q.CountUsersWithPlatformRoleID(ctx, id)
		if err != nil {
			return err
		}
		if members+holders > 0 {
			return ErrInUse
		}
		if _, err := q.DeleteRole(ctx, id); err != nil {
			return err
		}
		return audit.Entry{
			ActorID: actor, Action: audit.RoleDeleted, OrganizationID: derefUUID(nullable(cur.OrganizationID)),
			RoleCode: cur.Code, Detail: map[string]any{"scope": cur.Scope},
		}.Insert(ctx, q)
	})
	switch {
	case db.IsNoRows(err):
		return ErrNotFound
	case errors.Is(err, ErrSystemRole), errors.Is(err, ErrInUse):
		return err
	case db.IsForeignKeyViolation(err):
		return ErrInUse
	case err != nil:
		return fmt.Errorf("roles: borrar: %w", err)
	}
	return nil
}

func (r *pgRepository) PlatformRoles(ctx context.Context, userID uuid.UUID) ([]access.RoleRef, error) {
	rows, err := r.q.ListUserPlatformRoles(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("roles: roles de plataforma: %w", err)
	}
	out := make([]access.RoleRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, access.RoleRef{ID: row.RoleID.String(), Code: row.RoleCode, Name: row.RoleName})
	}
	return out, nil
}

func (r *pgRepository) Grant(ctx context.Context, userID uuid.UUID, role Role, actor uuid.UUID) (bool, error) {
	granted := false
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		n, err := q.InsertUserPlatformRole(ctx, store.InsertUserPlatformRoleParams{
			UserID: userID, RoleID: role.ID, GrantedBy: uuid.NullUUID{UUID: actor, Valid: actor != uuid.Nil},
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil // ya lo tenía: no hay cambio que auditar
		}
		granted = true
		return audit.Entry{
			ActorID: actor, Action: audit.PlatformRoleGranted, TargetUserID: userID,
			RoleID: role.ID, RoleCode: role.Code,
		}.Insert(ctx, q)
	})
	if db.IsForeignKeyViolation(err) {
		return false, ErrNotFound // el usuario o el rol desaparecieron entre la comprobación y el alta
	}
	if err != nil {
		return false, fmt.Errorf("roles: asignar rol de plataforma: %w", err)
	}
	return granted, nil
}

func (r *pgRepository) Revoke(ctx context.Context, userID uuid.UUID, role Role, actor uuid.UUID) (bool, error) {
	revoked := false
	isSuperadmin := role.Code == access.RoleSuperadmin && role.OrganizationID == nil
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if isSuperadmin {
			// Bloquea antes de tocar: dos quitas simultáneas no pueden dejar a cero.
			if err := q.LockSuperadmins(ctx); err != nil {
				return err
			}
		}
		n, err := q.DeleteUserPlatformRole(ctx, store.DeleteUserPlatformRoleParams{UserID: userID, RoleID: role.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil // no lo tenía
		}
		if isSuperadmin {
			active, err := q.CountActiveUsersWithPlatformRole(ctx, access.RoleSuperadmin)
			if err != nil {
				return err
			}
			if active == 0 {
				return ErrLastSuperadmin // la transacción se revierte: el rol se conserva
			}
		}
		revoked = true
		return audit.Entry{
			ActorID: actor, Action: audit.PlatformRoleRevoked, TargetUserID: userID,
			RoleID: role.ID, RoleCode: role.Code,
		}.Insert(ctx, q)
	})
	switch {
	case errors.Is(err, ErrLastSuperadmin):
		return false, ErrLastSuperadmin
	case err != nil:
		return false, fmt.Errorf("roles: quitar rol de plataforma: %w", err)
	}
	return revoked, nil
}

func (r *pgRepository) Audit(ctx context.Context, f audit.Filter) ([]audit.Record, int64, error) {
	return audit.List(ctx, r.q, f)
}
