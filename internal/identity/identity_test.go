package identity_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
)

// Las decisiones de acceso son consultas en memoria sobre el Principal. Aquí se
// prueban sin base de datos: lo que más importa es lo que debe RECHAZARSE.

var (
	orgA = uuid.MustParse("00000000-0000-4000-8000-00000000000a")
	orgB = uuid.MustParse("00000000-0000-4000-8000-00000000000b")
)

func principal(platformRoles []string, platformPerms []string, memberships map[uuid.UUID]identity.Membership) identity.Principal {
	p := identity.Principal{
		ID: uuid.New(), IsActive: true,
		Permissions: access.NewSet(platformPerms...),
		Memberships: memberships,
	}
	for _, r := range platformRoles {
		p.PlatformRoles = append(p.PlatformRoles, access.RoleRef{Code: r})
	}
	return p
}

func member(orgID uuid.UUID, code string, perms ...string) identity.Membership {
	return identity.Membership{OrganizationID: orgID, RoleCode: code, Permissions: access.NewSet(perms...)}
}

func TestPrincipalSinRolesNoPuedeNada(t *testing.T) {
	var p identity.Principal // el valor cero: usuario sin roles ni membresías
	if p.Can(access.PlatformOverviewRead) || p.IsSuperadmin() || p.HasRole("customer") {
		t.Fatal("un usuario sin roles no debe tener ningún poder")
	}
	if p.IsMember(orgA) || p.CanInOrg(orgA, access.OrgMembersRead) {
		t.Fatal("un usuario sin membresías no pertenece a ninguna organización")
	}
	if len(p.PermissionsInOrg(orgA)) != 0 {
		t.Fatal("no debe tener permisos en ninguna organización")
	}
}

func TestPermisosDeOrganizacionSonPorOrganizacion(t *testing.T) {
	// Admin en A, empleado en B: la misma persona, dos roles.
	p := principal(nil, nil, map[uuid.UUID]identity.Membership{
		orgA: member(orgA, "admin", "org.members.manage", "org.members.read"),
		orgB: member(orgB, "employee"),
	})

	if !p.CanInOrg(orgA, access.OrgMembersManage) {
		t.Error("admin de A debe poder administrar miembros de A")
	}
	if p.CanInOrg(orgB, access.OrgMembersManage) {
		t.Error("ser admin de A NO da permisos en B")
	}
	if p.CanInOrg(orgA, access.OrgRolesManage) {
		t.Error("solo tiene los permisos de su rol, no todos los de organización")
	}
	if !p.IsMember(orgA) || !p.IsMember(orgB) {
		t.Error("es miembro de las dos")
	}
	if p.IsMember(uuid.New()) {
		t.Error("no es miembro de una organización cualquiera")
	}
	if got := p.PermissionsInOrg(orgB); len(got) != 0 {
		t.Errorf("el empleado de B no tiene permisos: %v", got.Sorted())
	}
}

// Los permisos de plataforma nunca se filtran hacia una organización ni al revés:
// ser admin de una organización no da nada en la plataforma.
func TestPlataformaYOrganizacionNoSeMezclan(t *testing.T) {
	admin := principal(nil, nil, map[uuid.UUID]identity.Membership{
		orgA: member(orgA, "admin", "org.members.manage", "org.roles.manage"),
	})
	if admin.Can(access.PlatformRolesManage) || admin.Can(access.PlatformOrganizationsAccessAll) || admin.IsSuperadmin() {
		t.Fatal("un admin de organización no tiene permisos de plataforma")
	}

	// Un permiso de plataforma consultado como si fuera de organización no se cumple,
	// ni siquiera para quien puede acceder a todas las organizaciones.
	sa := principal([]string{"superadmin"}, access.AllInScope(access.ScopePlatform).Sorted(), nil)
	if sa.CanInOrg(orgA, access.PlatformRolesManage) {
		t.Fatal("CanInOrg solo responde por permisos de organización")
	}
}

func TestQuienAccedeATodasLasOrganizaciones(t *testing.T) {
	sa := principal([]string{"superadmin"}, access.AllInScope(access.ScopePlatform).Sorted(), nil)

	if !sa.IsSuperadmin() || !sa.HasRole("superadmin") {
		t.Fatal("debe reconocerse como superadmin")
	}
	// Sin ser miembro de nada, actúa con todos los permisos en cualquier organización.
	if !sa.IsMember(orgA) || !sa.IsMember(uuid.New()) {
		t.Error("el operador accede a cualquier organización")
	}
	for _, d := range access.Catalog {
		if d.Scope == access.ScopeOrganization && !sa.CanInOrg(orgA, d.Code) {
			t.Errorf("el operador debe tener %s en cualquier organización", d.Code)
		}
	}
	if got := sa.PermissionsInOrg(orgA); len(got) != len(access.AllInScope(access.ScopeOrganization)) {
		t.Errorf("PermissionsInOrg = %v, quería todos los de organización", got.Sorted())
	}

	// Un rol de plataforma a medida SIN access_all no entra a las organizaciones.
	soporte := principal([]string{"soporte"}, []string{"platform.users.read"}, nil)
	if soporte.IsMember(orgA) || soporte.CanInOrg(orgA, access.OrgMembersRead) {
		t.Fatal("un rol a medida sin access_all no debe acceder a organizaciones ajenas")
	}
}
