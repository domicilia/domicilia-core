// Package roles es la plataforma de roles y permisos: qué roles existen, qué
// permisos da cada uno, a quién se asignan y quién cambió qué.
//
// Hay dos alcances. Los roles de PLATAFORMA (superadmin, domiciliario, cliente y los
// que se creen) se asignan a un usuario y rigen en toda la plataforma. Los roles de
// ORGANIZACIÓN (admin, empleado y los personalizados de cada organización) se dan a
// un miembro y rigen dentro de esa organización.
//
// La regla que gobierna todo: nadie otorga más de lo que tiene. Un admin de
// organización solo puede crear y asignar roles cuyos permisos ya tiene y que sean
// delegables; sin eso se fabricaría un rol equivalente a "admin" y el rol de
// administrador se propagaría solo.
package roles

import (
	"errors"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound       = errors.New("roles: no encontrado")
	ErrDuplicate      = errors.New("roles: código repetido")
	ErrSystemRole     = errors.New("roles: es un rol de sistema")
	ErrInUse          = errors.New("roles: en uso")
	ErrLastSuperadmin = errors.New("roles: es el último superadmin activo")
)

// Role es un rol con sus permisos.
type Role struct {
	ID             uuid.UUID    `json:"id"`
	Code           string       `json:"code"`
	Name           string       `json:"name"`
	Description    *string      `json:"description"`
	Scope          access.Scope `json:"scope"`
	OrganizationID *uuid.UUID   `json:"organization_id"`
	IsSystem       bool         `json:"is_system"`
	// OrgAssignable: un admin de organización puede asignarlo.
	OrgAssignable bool     `json:"org_assignable"`
	Permissions   []string `json:"permissions"`
}

// PermissionSet devuelve los permisos del rol como conjunto.
func (r Role) PermissionSet() access.Set { return access.NewSet(r.Permissions...) }

// NewRole son los datos para crear un rol personalizado.
type NewRole struct {
	Code           string
	Name           string
	Description    *string
	Scope          access.Scope
	OrganizationID uuid.UUID // uuid.Nil: rol de plataforma
	OrgAssignable  bool
	Permissions    []string
}

// Patch son los cambios a un rol personalizado. Un campo nil no se toca.
type Patch struct {
	Name          *string
	Description   *string
	OrgAssignable *bool
	Permissions   *[]string
}

// CreateInput es el cuerpo para crear un rol.
type CreateInput struct {
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	Permissions   []string `json:"permissions"`
	OrgAssignable *bool    `json:"org_assignable"`
}

// UpdateInput es el cuerpo para editar un rol.
type UpdateInput struct {
	Name          *string   `json:"name"`
	Description   *string   `json:"description"`
	Permissions   *[]string `json:"permissions"`
	OrgAssignable *bool     `json:"org_assignable"`
}

// PlatformRoles es la respuesta de asignar o quitar un rol de plataforma.
type PlatformRoles struct {
	UserID uuid.UUID        `json:"user_id"`
	Roles  []access.RoleRef `json:"roles"`
}

// Límites de las columnas.
const (
	maxName        = 100
	maxDescription = 500
)
