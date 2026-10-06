// Package identity traduce el JWT ya validado (auth) en el usuario de negocio:
// alguien que existe en public.users, está activo y tiene ciertos roles y permisos.
// Es la frontera de seguridad de la API: un fallo aquí no da un error visible, da
// acceso a quien no corresponde.
//
// Cada petición carga el acceso completo del usuario (sus roles de plataforma, sus
// membresías y los permisos de cada rol), así las decisiones de los dominios son
// consultas en memoria (Can, CanInOrg) y no piden nada más a la base.
package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/auth"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

// Membership es el rol de un usuario en una organización y lo que ese rol permite.
type Membership struct {
	OrganizationID uuid.UUID
	RoleID         uuid.UUID
	RoleCode       string
	RoleName       string
	Permissions    access.Set
}

// Principal es el usuario de negocio que hace la petición.
type Principal struct {
	ID       uuid.UUID
	Email    string
	FullName *string
	IsActive bool
	// PlatformRoles son los roles de plataforma (superadmin, domiciliario, cliente...).
	PlatformRoles []access.RoleRef
	// Permissions son los permisos de plataforma que dan esos roles.
	Permissions access.Set
	// Memberships es el rol en cada organización a la que pertenece, por id.
	Memberships map[uuid.UUID]Membership
}

// Can dice si tiene un permiso de plataforma.
func (p Principal) Can(perm access.Permission) bool { return p.Permissions.Has(perm) }

// HasRole dice si tiene un rol de plataforma por código.
func (p Principal) HasRole(code string) bool {
	for _, r := range p.PlatformRoles {
		if r.Code == code {
			return true
		}
	}
	return false
}

// IsSuperadmin dice si tiene el rol de superadmin.
func (p Principal) IsSuperadmin() bool { return p.HasRole(access.RoleSuperadmin) }

// canAccessAllOrgs: actúa como miembro con todos los permisos en cualquier
// organización (el operador debe poder inspeccionar lo que suspendió).
func (p Principal) canAccessAllOrgs() bool { return p.Can(access.PlatformOrganizationsAccessAll) }

// IsMember dice si pertenece a la organización o puede acceder a todas.
func (p Principal) IsMember(orgID uuid.UUID) bool {
	if p.canAccessAllOrgs() {
		return true
	}
	_, ok := p.Memberships[orgID]
	return ok
}

// CanInOrg dice si tiene un permiso de organización dentro de esa organización.
func (p Principal) CanInOrg(orgID uuid.UUID, perm access.Permission) bool {
	if p.canAccessAllOrgs() {
		def, ok := access.Lookup(string(perm))
		return ok && def.Scope == access.ScopeOrganization
	}
	return p.Memberships[orgID].Permissions.Has(perm)
}

// PermissionsInOrg devuelve todos los permisos de organización que tiene en esa
// organización. Sirve para comprobar que nadie otorga más de lo que tiene.
func (p Principal) PermissionsInOrg(orgID uuid.UUID) access.Set {
	if p.canAccessAllOrgs() {
		return access.AllInScope(access.ScopeOrganization)
	}
	return p.Memberships[orgID].Permissions
}

// ErrUserNotFound: el token es válido pero no hay fila en public.users (aún no
// tiene perfil, o se borró la cuenta y el token todavía no caducó).
var ErrUserNotFound = errors.New("identity: usuario sin perfil")

// Store busca al usuario de negocio con su acceso.
type Store interface {
	UserByID(ctx context.Context, id uuid.UUID) (Principal, error)
}

// PGStore implementa Store sobre Postgres.
type PGStore struct{ q *store.Queries }

// NewPGStore crea el Store de Postgres.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{q: store.New(pool)} }

// UserByID devuelve el usuario con su acceso, o ErrUserNotFound.
func (s *PGStore) UserByID(ctx context.Context, id uuid.UUID) (Principal, error) {
	u, err := s.q.GetUser(ctx, id)
	if err != nil {
		if db.IsNoRows(err) {
			return Principal{}, ErrUserNotFound
		}
		return Principal{}, fmt.Errorf("identity: buscar usuario: %w", err)
	}

	roles, err := s.q.ListUserPlatformRoles(ctx, id)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: roles de plataforma: %w", err)
	}
	perms, err := s.q.ListUserPlatformPermissions(ctx, id)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: permisos de plataforma: %w", err)
	}
	members, err := s.q.ListUserMembershipAccess(ctx, id)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: membresías: %w", err)
	}

	p := Principal{
		ID:          u.ID,
		Email:       u.Email,
		FullName:    u.FullName,
		IsActive:    u.IsActive,
		Permissions: access.NewSet(perms...),
		Memberships: make(map[uuid.UUID]Membership, len(members)),
	}
	for _, r := range roles {
		p.PlatformRoles = append(p.PlatformRoles, access.RoleRef{ID: r.RoleID.String(), Code: r.RoleCode, Name: r.RoleName})
	}
	for _, m := range members {
		p.Memberships[m.OrganizationID] = Membership{
			OrganizationID: m.OrganizationID,
			RoleID:         m.RoleID,
			RoleCode:       m.RoleCode,
			RoleName:       m.RoleName,
			Permissions:    access.NewSet(m.Permissions...),
		}
	}
	return p, nil
}

type principalKey struct{}

// FromContext devuelve el usuario puesto por RequireUser.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Current lee el usuario de la petición. Si no está, RequireUser no corrió: es
// un 401 (fallar cerrado) en vez de tratar la petición como si fuera de alguien.
func Current(c *echo.Context) (Principal, error) {
	p, ok := FromContext(c.Request().Context())
	if !ok {
		return Principal{}, apperr.Unauthorized("credenciales requeridas o inválidas")
	}
	return p, nil
}

// RequireUser exige que el token pertenezca a un usuario de negocio activo. Va
// después de auth.Middleware.
//
// 401 = "no sé quién eres" (token sin perfil); 403 = "sé quién eres y no
// puedes" (cuenta desactivada). Un usuario suspendido está identificado.
func RequireUser(s Store) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			claims, ok := auth.FromContext(c.Request().Context())
			if !ok {
				return auth.Unauthorized(c)
			}
			id, err := uuid.Parse(claims.Subject)
			if err != nil {
				return auth.Unauthorized(c)
			}
			p, err := s.UserByID(c.Request().Context(), id)
			if errors.Is(err, ErrUserNotFound) {
				return auth.Unauthorized(c)
			}
			if err != nil {
				return err
			}
			if !p.IsActive {
				return apperr.Forbidden("usuario inactivo")
			}
			req := c.Request()
			c.SetRequest(req.WithContext(context.WithValue(req.Context(), principalKey{}, p)))
			return next(c)
		}
	}
}

// Require exige un permiso de PLATAFORMA. Ser admin de una organización NO lo da:
// es la frontera entre el inquilino y el operador del servicio. Los permisos de
// organización se comprueban en el servicio, porque dependen de la organización
// que se toca (Principal.CanInOrg).
func Require(perm access.Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			p, err := Current(c)
			if err != nil {
				return err
			}
			if !p.Can(perm) {
				return apperr.Forbidden("no tienes permiso para esta acción (" + string(perm) + ")")
			}
			return next(c)
		}
	}
}
