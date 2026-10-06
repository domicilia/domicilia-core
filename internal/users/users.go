// Package users es la cuenta de negocio de quien ya tiene identidad en GoTrue: el
// perfil de sesión (/users/me), el alta de clientes y la gestión de usuarios que
// hace el operador de la plataforma (listar, ver, activar y desactivar).
//
// La contraseña no vive aquí: la posee auth-domicilia. Los ROLES tampoco: son de
// `roles`; aquí solo se muestran junto al usuario. Lo específico de ser cliente es
// de `customers`.
package users

import (
	"errors"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/customers"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound       = errors.New("users: no encontrado")
	ErrEmailTaken     = errors.New("users: correo ya registrado")
	ErrLastSuperadmin = errors.New("users: es el último superadmin activo")
)

// User es el usuario de negocio tal como lo ve la API. is_general_admin e
// is_delivery se derivan de los roles (siguen en la respuesta para no cambiar el
// contrato con el frontend); la fuente de verdad es `roles`.
type User struct {
	ID             uuid.UUID        `json:"id"`
	Email          string           `json:"email"`
	FullName       *string          `json:"full_name"`
	IsActive       bool             `json:"is_active"`
	IsGeneralAdmin bool             `json:"is_general_admin"`
	IsDelivery     bool             `json:"is_delivery"`
	Roles          []access.RoleRef `json:"roles"`
}

// NewUser arma un User derivando los indicadores de sus roles de plataforma.
func NewUser(id uuid.UUID, email string, fullName *string, active bool, roles []access.RoleRef) User {
	if roles == nil {
		roles = []access.RoleRef{}
	}
	u := User{ID: id, Email: email, FullName: fullName, IsActive: active, Roles: roles}
	for _, r := range roles {
		switch r.Code {
		case access.RoleSuperadmin:
			u.IsGeneralAdmin = true
		case access.RoleDelivery:
			u.IsDelivery = true
		}
	}
	return u
}

// MembershipBrief resume a qué organización pertenece el usuario y con qué rol.
// role es el código del rol (admin, employee o uno personalizado).
type MembershipBrief struct {
	OrganizationID   uuid.UUID `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	OrganizationSlug string    `json:"organization_slug"`
	// OrganizationStatus (active, suspended, archived) y PlanTier dejan a la interfaz
	// decidir qué mostrar sin otra llamada: una organización suspendida no se abre.
	OrganizationStatus string    `json:"organization_status"`
	PlanTier           string    `json:"plan_tier"`
	Role               string    `json:"role"`
	RoleID             uuid.UUID `json:"role_id"`
	RoleName           string    `json:"role_name"`
	// Permissions son los permisos que ese rol da en esa organización (solo en /me).
	Permissions []string `json:"permissions,omitempty"`
}

// Me es la sesión del usuario: sus datos, sus roles y permisos de plataforma, sus
// organizaciones con el rol en cada una, y su perfil de cliente si lo tiene.
type Me struct {
	User
	// Permissions son los permisos de plataforma que dan sus roles.
	Permissions     []string           `json:"permissions"`
	Memberships     []MembershipBrief  `json:"memberships"`
	CustomerProfile *customers.Profile `json:"customer_profile"`
}

// Detail es un usuario visto por el operador de la plataforma.
type Detail struct {
	User
	Memberships     []MembershipBrief  `json:"memberships"`
	CustomerProfile *customers.Profile `json:"customer_profile"`
}

// ProfileInput son los datos que el usuario edita de sí mismo. Un campo ausente (o
// null) no se toca.
type ProfileInput struct {
	FullName       *string `json:"full_name"`
	Phone          *string `json:"phone"`
	DefaultAddress *string `json:"default_address"`
}

// ListFilter acota el listado de usuarios. Los campos vacíos no filtran.
type ListFilter struct {
	// Search busca en correo y nombre, sin distinguir mayúsculas.
	Search string
	// Role casa con el código de un rol de plataforma o de organización.
	Role           string
	OrganizationID *uuid.UUID
	IsActive       *bool
	Limit          int
	Offset         int
}

const maxFullName = 255
