package app_test

import (
	"context"
	"slices"
	"testing"

	"github.com/domicilia/domicilia-core/internal/access"
)

// Invariantes del modelo de roles: lo que NUNCA debe dejar de cumplirse, sin
// importar qué migraciones o qué código se agreguen después.

// El catálogo de permisos lo define el código (los que las rutas exigen) y la base
// lo guarda (para la FK de role_permissions). Si se agrega un permiso en un lado y
// no en el otro, o con otros atributos, esta prueba falla.
func TestElCatalogoDeLaBaseCoincideConElCodigo(t *testing.T) {
	newHarness(t)
	rows, err := pool.Query(context.Background(), `SELECT code, scope, delegable FROM permissions ORDER BY code`)
	must(t, err)
	defer rows.Close()

	inDB := map[string]access.Def{}
	for rows.Next() {
		var code, scope string
		var delegable bool
		must(t, rows.Scan(&code, &scope, &delegable))
		inDB[code] = access.Def{Code: access.Permission(code), Scope: access.Scope(scope), Delegable: delegable}
	}
	must(t, rows.Err())

	for _, d := range access.Catalog {
		got, ok := inDB[string(d.Code)]
		if !ok {
			t.Errorf("el permiso %s está en el código pero no en la base: falta una migración que lo inserte", d.Code)
			continue
		}
		if got.Scope != d.Scope || got.Delegable != d.Delegable {
			t.Errorf("%s difiere: base = {%s, delegable=%v}, código = {%s, delegable=%v}", d.Code, got.Scope, got.Delegable, d.Scope, d.Delegable)
		}
		delete(inDB, string(d.Code))
	}
	for code := range inDB {
		t.Errorf("el permiso %s está en la base pero no en el código: ninguna ruta lo exige", code)
	}
}

func rolePermissions(t *testing.T, code, scope string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT rp.permission_code FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		WHERE r.code = $1 AND r.scope = $2 AND r.organization_id IS NULL ORDER BY rp.permission_code`, code, scope)
	must(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		must(t, rows.Scan(&p))
		out = append(out, p)
	}
	must(t, rows.Err())
	return out
}

// El superadmin tiene todos los permisos de plataforma: toda migración que agregue
// uno debe otorgárselo, o el operador no podría usar la función nueva. Los de
// organización los cubre platform.organizations.access_all.
func TestElSuperadminTieneTodosLosPermisosDePlataforma(t *testing.T) {
	newHarness(t)
	got := rolePermissions(t, access.RoleSuperadmin, "platform")

	want := access.AllInScope(access.ScopePlatform).Sorted()
	if !slices.Equal(got, want) {
		t.Fatalf("permisos del superadmin:\n  base   = %v\n  código = %v", got, want)
	}
}

func TestLosRolesDeSistemaTienenLosPermisosEsperados(t *testing.T) {
	newHarness(t)

	// admin: todo lo de organización, y nada de plataforma.
	wantAdmin := access.AllInScope(access.ScopeOrganization).Sorted()
	if got := rolePermissions(t, access.RoleAdmin, "organization"); !slices.Equal(got, wantAdmin) {
		t.Errorf("permisos de admin:\n  base = %v\n  quería = %v", got, wantAdmin)
	}
	// El empleado (call center) atiende la bandeja, gestiona contactos, consulta el catálogo (para
	// ayudar a un cliente a pedir) y gestiona los pedidos que llegan (aceptar, despachar...); no
	// administra nada más.
	wantEmployee := []string{
		"org.catalog.read", "org.contacts.manage", "org.contacts.read",
		"org.inbox.read", "org.inbox.reply", "org.orders.manage", "org.orders.read",
	}
	if got := rolePermissions(t, access.RoleEmployee, "organization"); !slices.Equal(got, wantEmployee) {
		t.Errorf("permisos de employee: base = %v, quería = %v", got, wantEmployee)
	}
	// delivery y customer no tienen permisos todavía.
	for _, r := range []struct{ code, scope string }{
		{access.RoleDelivery, "platform"}, {access.RoleCustomer, "platform"},
	} {
		if got := rolePermissions(t, r.code, r.scope); len(got) != 0 {
			t.Errorf("%s no debía tener permisos (aún), tiene %v", r.code, got)
		}
	}
}

// Un rol de sistema de organización nunca puede llevar permisos de plataforma, ni
// al revés: el alcance del rol y el de sus permisos deben coincidir.
func TestElAlcanceDeLosRolesCoincideConElDeSusPermisos(t *testing.T) {
	newHarness(t)
	var n int
	must(t, pool.QueryRow(context.Background(), `
		SELECT count(*) FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id JOIN permissions p ON p.code = rp.permission_code
		WHERE r.scope <> p.scope`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d asignaciones rol-permiso con alcances distintos", n)
	}
}

func TestLosCincoRolesDeSistemaExisten(t *testing.T) {
	newHarness(t)
	want := map[string]string{
		"superadmin": "platform", "delivery": "platform", "customer": "platform",
		"admin": "organization", "employee": "organization",
	}
	rows, err := pool.Query(context.Background(), `SELECT code, scope FROM roles WHERE is_system ORDER BY code`)
	must(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var code, scope string
		must(t, rows.Scan(&code, &scope))
		got[code] = scope
	}
	must(t, rows.Err())
	if len(got) != len(want) {
		t.Fatalf("roles de sistema = %v, quería %v", got, want)
	}
	for c, s := range want {
		if got[c] != s {
			t.Errorf("rol %s: alcance %q, quería %q", c, got[c], s)
		}
	}
}
