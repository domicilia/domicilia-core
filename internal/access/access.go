// Package access es el modelo de permisos: qué permisos existen y cómo se
// combinan. No toca la base de datos ni HTTP; lo consumen identity (para armar el
// acceso de cada petición) y los dominios (para decidir).
//
// El catálogo de permisos lo define ESTE código, porque son los que las rutas
// exigen: agregar un permiso es agregar una constante aquí y una migración que lo
// inserte en la tabla `permissions` (y se lo otorgue a superadmin). Una prueba
// comprueba que ambos coinciden. Lo que sí es dato, y se crea en runtime, son los
// roles: un conjunto de permisos con nombre.
package access

import "slices"

// Permission es un permiso, con la forma "<dominio>.<recurso>.<acción>".
type Permission string

// Scope dice dónde rige un permiso o un rol.
type Scope string

// Alcances.
const (
	// ScopePlatform rige sobre toda la plataforma (el operador del servicio).
	ScopePlatform Scope = "platform"
	// ScopeOrganization rige dentro de una organización.
	ScopeOrganization Scope = "organization"
)

// Permisos de plataforma.
const (
	PlatformOverviewRead        Permission = "platform.overview.read"
	PlatformOrganizationsCreate Permission = "platform.organizations.create"
	PlatformOrganizationsManage Permission = "platform.organizations.manage"
	// PlatformOrganizationsAccessAll: actuar como miembro con todos los permisos en
	// cualquier organización, incluso suspendida (el operador debe poder inspeccionar
	// lo que suspendió).
	PlatformOrganizationsAccessAll Permission = "platform.organizations.access_all"
	PlatformUsersRead              Permission = "platform.users.read"
	PlatformUsersManage            Permission = "platform.users.manage"
	PlatformRolesRead              Permission = "platform.roles.read"
	PlatformRolesManage            Permission = "platform.roles.manage"
	PlatformDriversReview          Permission = "platform.drivers.review"
	PlatformAuditRead              Permission = "platform.audit.read"
)

// Permisos de organización.
const (
	OrgMembersRead   Permission = "org.members.read"
	OrgMembersManage Permission = "org.members.manage"
	OrgRolesRead     Permission = "org.roles.read"
	OrgRolesManage   Permission = "org.roles.manage"
	OrgAuditRead     Permission = "org.audit.read"

	OrgSettingsRead   Permission = "org.settings.read"
	OrgSettingsManage Permission = "org.settings.manage"
	OrgBillingRead    Permission = "org.billing.read"

	OrgInboxRead      Permission = "org.inbox.read"
	OrgInboxReply     Permission = "org.inbox.reply"
	OrgInboxManage    Permission = "org.inbox.manage"
	OrgContactsRead   Permission = "org.contacts.read"
	OrgContactsManage Permission = "org.contacts.manage"

	OrgCatalogRead   Permission = "org.catalog.read"
	OrgCatalogManage Permission = "org.catalog.manage"

	OrgOrdersRead   Permission = "org.orders.read"
	OrgOrdersManage Permission = "org.orders.manage"

	OrgPaymentsRead Permission = "org.payments.read"

	OrgPromotionsRead   Permission = "org.promotions.read"
	OrgPromotionsManage Permission = "org.promotions.manage"
)

// Códigos de los roles de sistema. Los de organización (admin, employee) coinciden
// con los valores del enum que reemplazaron, para no cambiar el contrato con el
// frontend.
const (
	RoleSuperadmin = "superadmin"
	RoleDelivery   = "delivery"
	RoleCustomer   = "customer"
	RoleAdmin      = "admin"
	RoleEmployee   = "employee"
)

// Def describe un permiso del catálogo.
type Def struct {
	Code        Permission `json:"code"`
	Scope       Scope      `json:"scope"`
	Description string     `json:"description"`
	// Delegable: puede ir en un rol personalizado creado por un admin de
	// organización. Los de administración no lo son: si lo fueran, un admin podría
	// fabricarse un rol equivalente a "admin" y saltarse la regla de que solo
	// quien tiene PlatformRolesManage nombra administradores.
	Delegable bool `json:"delegable"`
}

// Catalog es el catálogo completo. Debe coincidir con la tabla `permissions`.
var Catalog = []Def{
	{PlatformOverviewRead, ScopePlatform, "Ver el panorama de la plataforma", false},
	{PlatformOrganizationsCreate, ScopePlatform, "Crear organizaciones", false},
	{PlatformOrganizationsManage, ScopePlatform, "Suspender/reactivar organizaciones y cambiar su plan", false},
	{PlatformOrganizationsAccessAll, ScopePlatform, "Actuar como miembro con todos los permisos en cualquier organización, incluso suspendida", false},
	{PlatformUsersRead, ScopePlatform, "Listar y ver usuarios de la plataforma", false},
	{PlatformUsersManage, ScopePlatform, "Activar y desactivar cuentas", false},
	{PlatformRolesRead, ScopePlatform, "Ver roles y permisos", false},
	{PlatformRolesManage, ScopePlatform, "Asignar roles de plataforma, crear roles y asignar cualquier rol de organización", false},
	{PlatformDriversReview, ScopePlatform, "Revisar las postulaciones de domiciliarios", false},
	{PlatformAuditRead, ScopePlatform, "Ver la auditoría de toda la plataforma", false},
	{OrgMembersRead, ScopeOrganization, "Ver los miembros de la organización", true},
	{OrgMembersManage, ScopeOrganization, "Sumar, invitar, cambiar de rol y sacar miembros", false},
	{OrgRolesRead, ScopeOrganization, "Ver los roles de la organización", true},
	{OrgRolesManage, ScopeOrganization, "Crear, editar y borrar roles de la organización", false},
	{OrgAuditRead, ScopeOrganization, "Ver la auditoría de la organización", false},
	{OrgSettingsRead, ScopeOrganization, "Ver los ajustes del negocio (datos de contacto, horarios, zona horaria)", true},
	{OrgSettingsManage, ScopeOrganization, "Editar el perfil y los ajustes del negocio", false},
	{OrgBillingRead, ScopeOrganization, "Ver el plan y el historial de suscripción", false},
	{OrgInboxRead, ScopeOrganization, "Ver las bandejas, las conversaciones y los mensajes", true},
	{OrgInboxReply, ScopeOrganization, "Responder, asignar, cerrar y reabrir conversaciones", true},
	{OrgInboxManage, ScopeOrganization, "Conectar, editar y desconectar las bandejas de WhatsApp", false},
	{OrgContactsRead, ScopeOrganization, "Ver los contactos", true},
	{OrgContactsManage, ScopeOrganization, "Crear y editar contactos", true},
	{OrgCatalogRead, ScopeOrganization, "Ver categorías, productos, variantes y modificadores", true},
	{OrgCatalogManage, ScopeOrganization, "Crear y editar categorías, productos, variantes y modificadores", true},
	{OrgOrdersRead, ScopeOrganization, "Ver los pedidos de la organización", true},
	{OrgOrdersManage, ScopeOrganization, "Aceptar, rechazar y cambiar el estado de los pedidos", true},
	{OrgPaymentsRead, ScopeOrganization, "Ver los pagos de los pedidos de la organización", false},
	{OrgPromotionsRead, ScopeOrganization, "Ver las promociones de la organización", true},
	{OrgPromotionsManage, ScopeOrganization, "Crear y editar promociones", true},
}

// Lookup busca un permiso del catálogo por código.
func Lookup(code string) (Def, bool) {
	for _, d := range Catalog {
		if string(d.Code) == code {
			return d, true
		}
	}
	return Def{}, false
}

// Set es un conjunto de permisos.
type Set map[Permission]struct{}

// NewSet arma un conjunto desde códigos.
func NewSet(codes ...string) Set {
	s := make(Set, len(codes))
	for _, c := range codes {
		s[Permission(c)] = struct{}{}
	}
	return s
}

// AllInScope devuelve todos los permisos del catálogo de un alcance.
func AllInScope(scope Scope) Set {
	s := Set{}
	for _, d := range Catalog {
		if d.Scope == scope {
			s[d.Code] = struct{}{}
		}
	}
	return s
}

// Has dice si el conjunto incluye el permiso.
func (s Set) Has(p Permission) bool {
	_, ok := s[p]
	return ok
}

// Contains dice si s incluye TODOS los permisos de other.
func (s Set) Contains(other Set) bool {
	for p := range other {
		if !s.Has(p) {
			return false
		}
	}
	return true
}

// Sorted devuelve los códigos ordenados (para respuestas y pruebas estables).
func (s Set) Sorted() []string {
	out := make([]string, 0, len(s))
	for p := range s {
		out = append(out, string(p))
	}
	slices.Sort(out)
	return out
}

// RoleRef es la referencia mínima a un rol, para mostrarlo junto a un usuario.
type RoleRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// ValidCode dice si un código de rol tiene la forma permitida (minúsculas,
// dígitos, guion y guion bajo; empieza con letra; 2 a 50 caracteres). Es la misma
// regla que la restricción roles_code_format de la base.
func ValidCode(code string) bool {
	if len(code) < 2 || len(code) > 50 {
		return false
	}
	for i, r := range code {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}
