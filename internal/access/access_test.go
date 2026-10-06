package access_test

import (
	"strings"
	"testing"

	"github.com/domicilia/domicilia-core/internal/access"
)

func TestElCatalogoEsCoherente(t *testing.T) {
	seen := map[access.Permission]bool{}
	for _, d := range access.Catalog {
		if seen[d.Code] {
			t.Errorf("permiso repetido: %s", d.Code)
		}
		seen[d.Code] = true

		if d.Description == "" {
			t.Errorf("%s no tiene descripción", d.Code)
		}
		// El prefijo del código dice el alcance: platform.* o org.*.
		prefix := strings.SplitN(string(d.Code), ".", 2)[0]
		switch d.Scope {
		case access.ScopePlatform:
			if prefix != "platform" {
				t.Errorf("%s es de plataforma y su código no empieza por platform.", d.Code)
			}
		case access.ScopeOrganization:
			if prefix != "org" {
				t.Errorf("%s es de organización y su código no empieza por org.", d.Code)
			}
		default:
			t.Errorf("%s tiene un alcance desconocido: %q", d.Code, d.Scope)
		}
		// Un permiso de plataforma nunca es delegable: rige sobre TODA la plataforma.
		if d.Scope == access.ScopePlatform && d.Delegable {
			t.Errorf("%s es de plataforma y no puede ser delegable", d.Code)
		}
	}
}

// Ningún permiso de administración puede ser delegable: si lo fuera, un admin de
// organización podría fabricarse un rol equivalente a "admin" y saltarse la regla
// de que solo quien tiene platform.roles.manage nombra administradores.
func TestLosPermisosDeAdministracionNoSonDelegables(t *testing.T) {
	for _, p := range []access.Permission{access.OrgMembersManage, access.OrgRolesManage, access.OrgAuditRead} {
		d, ok := access.Lookup(string(p))
		if !ok {
			t.Fatalf("%s no está en el catálogo", p)
		}
		if d.Delegable {
			t.Errorf("%s no debe ser delegable", p)
		}
	}
}

func TestLookup(t *testing.T) {
	if d, ok := access.Lookup("org.members.read"); !ok || d.Scope != access.ScopeOrganization || !d.Delegable {
		t.Fatalf("Lookup(org.members.read) = %+v, %v", d, ok)
	}
	for _, bad := range []string{"", "org.inventado", "PLATFORM.overview.read", " org.members.read"} {
		if _, ok := access.Lookup(bad); ok {
			t.Errorf("Lookup(%q) no debía encontrar nada", bad)
		}
	}
}

func TestAllInScope(t *testing.T) {
	org := access.AllInScope(access.ScopeOrganization)
	if !org.Has(access.OrgRolesManage) || org.Has(access.PlatformRolesManage) {
		t.Fatalf("los de organización no deben incluir los de plataforma: %v", org.Sorted())
	}
	plat := access.AllInScope(access.ScopePlatform)
	if !plat.Has(access.PlatformRolesManage) || plat.Has(access.OrgRolesManage) {
		t.Fatalf("los de plataforma no deben incluir los de organización: %v", plat.Sorted())
	}
	if len(org)+len(plat) != len(access.Catalog) {
		t.Fatalf("%d + %d != %d: algún permiso no es de ningún alcance", len(org), len(plat), len(access.Catalog))
	}
}

func TestSet(t *testing.T) {
	a := access.NewSet("org.members.read", "org.members.manage")
	b := access.NewSet("org.members.read")

	if !a.Contains(b) {
		t.Error("{read, manage} debe contener a {read}")
	}
	if b.Contains(a) {
		t.Error("{read} no debe contener a {read, manage}")
	}
	if !a.Contains(access.NewSet()) {
		t.Error("todo conjunto contiene al vacío")
	}
	if access.NewSet().Contains(b) {
		t.Error("el vacío no contiene a {read}")
	}
	// Un conjunto nil (usuario sin roles) se comporta como el vacío, sin entrar en pánico.
	var none access.Set
	if none.Has(access.OrgMembersRead) || !a.Contains(none) {
		t.Error("un Set nil debía comportarse como vacío")
	}
	if got := strings.Join(a.Sorted(), ","); got != "org.members.manage,org.members.read" {
		t.Errorf("Sorted = %q", got)
	}
}

func TestValidCode(t *testing.T) {
	ok := []string{"ab", "supervisor", "jefe-de-turno", "call_center", "nivel2", "a" + strings.Repeat("b", 49)}
	bad := []string{
		"", "a", "1abc", "_abc", "-abc", "Abc", "ABC", "con espacio", "con.punto", "ñandú",
		"a" + strings.Repeat("b", 50), // 51 caracteres
	}
	for _, c := range ok {
		if !access.ValidCode(c) {
			t.Errorf("ValidCode(%q) debía ser válido", c)
		}
	}
	for _, c := range bad {
		if access.ValidCode(c) {
			t.Errorf("ValidCode(%q) debía ser inválido", c)
		}
	}
}
