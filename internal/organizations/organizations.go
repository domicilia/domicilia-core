// Package organizations son los inquilinos (tenants) de la plataforma: su ciclo de
// vida (activa, suspendida, archivada), su perfil y ajustes, sus miembros y las
// invitaciones para sumar más. Aquí viven las reglas de pertenencia: quién puede ver
// una organización, quién administra a sus miembros y qué pasa cuando está suspendida.
//
// QUÉ puede hacer cada miembro lo deciden los permisos de su rol (paquete `roles`);
// este paquete solo pregunta "¿tiene este permiso en esta organización?". Qué
// incluye el plan de cada una lo decide el paquete `plans`. El diseño completo está
// en docs/organizaciones.md.
package organizations

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/audit"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound      = errors.New("organizations: no encontrado")
	ErrDuplicate     = errors.New("organizations: nombre o slug repetido")
	ErrAlreadyMember = errors.New("organizations: ya es miembro")
	ErrEmailTaken    = errors.New("organizations: correo ya registrado")
	// ErrLastManager: el cambio dejaría a la organización sin nadie que pueda
	// administrar a sus miembros.
	ErrLastManager = errors.New("organizations: es el último administrador")
	// ErrInvalidTransition: el estado actual no permite la operación pedida.
	ErrInvalidTransition = errors.New("organizations: transición de estado no permitida")

	// Invitaciones. Caducada, revocada, aceptada o inexistente se reportan igual
	// (ErrInvitationNotFound): quien tiene un token viejo no averigua por qué falló.
	ErrInvitationNotFound = errors.New("organizations: invitación no encontrada")
	ErrEmailMismatch      = errors.New("organizations: la invitación es para otro correo")
	ErrOrgNotAcceptable   = errors.New("organizations: la organización no admite miembros nuevos")
	ErrUserInactive       = errors.New("organizations: la cuenta está desactivada")
)

// Status es el estado de una organización.
type Status string

// Estados. Ver docs/organizaciones.md.
const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusArchived  Status = "archived"
)

// Operation es un cambio de estado con los estados desde los que se puede pedir. No
// basta con validar el estado de destino: "suspender" una organización archivada
// la sacaría del archivo sin que nadie lo hubiera decidido, y "restaurar" una activa
// la suspendería.
type Operation struct {
	// Label es el verbo, para los mensajes.
	Label string
	// From son los estados desde los que se puede aplicar.
	From []Status
	To   Status
	// Action es lo que queda en la auditoría.
	Action audit.Action
}

// Operaciones del ciclo de vida. Ver docs/organizaciones.md.
var (
	OpSuspend    = Operation{"suspender", []Status{StatusActive}, StatusSuspended, audit.OrganizationSuspended}
	OpReactivate = Operation{"reactivar", []Status{StatusSuspended}, StatusActive, audit.OrganizationReactivated}
	OpArchive    = Operation{"archivar", []Status{StatusActive, StatusSuspended}, StatusArchived, audit.OrganizationArchived}
	// OpRestore deja la organización SUSPENDIDA, no activa: reactivarla es otra decisión.
	OpRestore = Operation{"restaurar", []Status{StatusArchived}, StatusSuspended, audit.OrganizationRestored}
)

// CanApply dice si la operación se puede pedir desde ese estado.
func (o Operation) CanApply(from Status) bool { return slices.Contains(o.From, from) }

// Organization es un inquilino.
type Organization struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description *string   `json:"description"`
	Status      Status    `json:"status"`
	// IsActive es status == active. Se conserva porque el frontend lo lee.
	IsActive     bool      `json:"is_active"`
	StatusReason *string   `json:"status_reason"`
	PlanTier     string    `json:"plan_tier"`
	CreatedAt    time.Time `json:"created_at"`
}

// Member es la pertenencia de un usuario a una organización. Role es el CÓDIGO del
// rol (admin, employee o uno personalizado de la organización).
type Member struct {
	UserID         uuid.UUID `json:"user_id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Role           string    `json:"role"`
	RoleID         uuid.UUID `json:"role_id"`
	RoleName       string    `json:"role_name"`
}

// InvitedMember es un miembro recién invitado, con la contraseña temporal que se
// generó para su cuenta nueva.
//
// OBSOLETO: la contraseña temporal solo existe mientras no haya proveedor de correo
// en GoTrue. El camino vigente son las invitaciones con token (invitations.go).
type InvitedMember struct {
	Member
	TemporaryPassword string `json:"temporary_password"`
}

// PublicOrganization es lo que se muestra sin iniciar sesión: la cara del negocio
// para quien va a pedir. Nada de lo interno (estado, plan, contacto legal).
type PublicOrganization struct {
	Name          string        `json:"name"`
	Slug          string        `json:"slug"`
	Description   *string       `json:"description"`
	LogoURL       *string       `json:"logo_url"`
	City          *string       `json:"city"`
	Timezone      string        `json:"timezone"`
	Locale        string        `json:"locale"`
	Currency      string        `json:"currency"`
	BusinessHours BusinessHours `json:"business_hours"`
	// OpenNow es null si el negocio no configuró horario.
	OpenNow *bool `json:"open_now"`
}

// PublicOrganizationSummary es una fila del directorio público — la app de
// cliente eligiendo con cuál organización pedir. Deliberadamente más liviana
// que PublicOrganization: sin horario ni open_now, que solo se calculan para
// el detalle de una organización ya elegida.
type PublicOrganizationSummary struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	LogoURL     *string `json:"logo_url"`
	City        *string `json:"city"`
}

// Límites de las columnas.
const (
	maxName        = 255
	maxDescription = 500
	maxFullName    = 255
	maxReason      = 500
)
