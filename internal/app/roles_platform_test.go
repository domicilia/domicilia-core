package app_test

import (
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
)

// La plataforma de roles: catálogo, roles de plataforma a medida, su asignación a
// usuarios y las protecciones para que la plataforma no se quede sin operador.

const (
	platformRolesURL = "/v1/platform/roles"
	usersURL         = "/v1/platform/users/"
)

func grantURL(u person) string { return usersURL + u.ID.String() + "/roles" }

// crearRolDePlataforma crea un rol de plataforma como superadmin y devuelve su id.
func crearRolDePlataforma(t *testing.T, h *harness, sa person, code string, perms ...string) string {
	t.Helper()
	rec := h.do(http.MethodPost, platformRolesURL, roleReq(code, "Rol "+code, perms...), &sa)
	want(t, rec, http.StatusCreated)
	return jsonMap(t, rec)["id"].(string)
}

// darRol asigna un rol de plataforma por la API y exige 200.
func darRol(t *testing.T, h *harness, as person, target person, role string) {
	t.Helper()
	want(t, h.do(http.MethodPost, grantURL(target), map[string]string{"role": role}, &as), http.StatusOK)
}

// conRolDePlataforma crea un usuario y le da un rol de plataforma personalizado.
func conRolDePlataforma(t *testing.T, h *harness, sa person, role string) person {
	t.Helper()
	u := h.user()
	darRol(t, h, sa, u, role)
	return u
}

func TestCatalogoYListadoDeRoles(t *testing.T) {
	t.Run("el catálogo de permisos lo ve quien tiene platform.roles.read", func(t *testing.T) {
		h := newHarness(t)
		sa, adminOrg := h.user(superadmin()), h.user()
		h.join(adminOrg, h.org("Acme"), "admin")

		rec := h.do(http.MethodGet, "/v1/platform/permissions", nil, &sa)
		want(t, rec, http.StatusOK)
		defs := decode[[]map[string]any](t, rec)
		if len(defs) != len(access.Catalog) {
			t.Fatalf("permisos = %d, catálogo = %d", len(defs), len(access.Catalog))
		}
		want(t, h.do(http.MethodGet, "/v1/platform/permissions", nil, &adminOrg), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/platform/permissions", nil, nil), http.StatusUnauthorized)
	})

	t.Run("el listado de roles trae los de sistema y filtra por alcance y organización", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, _, o := orgConAdmin(h, "Acme")
		crearRol(t, h, admin, o, "supervisor")
		crearRolDePlataforma(t, h, sa, "soporte")

		all := codigosDeRoles(t, h, platformRolesURL, sa)
		for _, c := range []string{"superadmin", "delivery", "customer", "admin", "employee", "supervisor", "soporte"} {
			if !slices.Contains(all, c) {
				t.Errorf("falta %q en %v", c, all)
			}
		}
		plat := codigosDeRoles(t, h, platformRolesURL+"?scope=platform", sa)
		if slices.Contains(plat, "admin") || slices.Contains(plat, "supervisor") || !slices.Contains(plat, "soporte") {
			t.Errorf("scope=platform = %v", plat)
		}
		orgs := codigosDeRoles(t, h, platformRolesURL+"?scope=organization", sa)
		if slices.Contains(orgs, "superadmin") || !slices.Contains(orgs, "supervisor") {
			t.Errorf("scope=organization = %v", orgs)
		}
		deAcme := codigosDeRoles(t, h, platformRolesURL+"?organization_id="+o.ID.String(), sa)
		if !slices.Equal(deAcme, []string{"supervisor"}) {
			t.Errorf("organization_id = %v, quería solo los personalizados de esa organización", deAcme)
		}
		want(t, h.do(http.MethodGet, platformRolesURL+"?scope=inventado", nil, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodGet, platformRolesURL+"?organization_id=no-es-uuid", nil, &sa), http.StatusUnprocessableEntity)
	})

	t.Run("un solo rol, con sus permisos", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := str(t, `SELECT id::text FROM roles WHERE code = 'superadmin'`)
		rec := h.do(http.MethodGet, platformRolesURL+"/"+id, nil, &sa)
		want(t, rec, http.StatusOK)
		perms, _ := jsonMap(t, rec)["permissions"].([]any)
		if len(perms) != len(access.AllInScope(access.ScopePlatform)) {
			t.Fatalf("permisos del superadmin = %d", len(perms))
		}
		want(t, h.do(http.MethodGet, platformRolesURL+"/"+uuid.NewString(), nil, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodGet, platformRolesURL+"/no-es-uuid", nil, &sa), http.StatusUnprocessableEntity)
	})

	t.Run("sin platform.roles.read no se ve nada", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodGet, platformRolesURL, nil, &u), http.StatusForbidden)
		want(t, h.do(http.MethodGet, platformRolesURL+"/"+uuid.NewString(), nil, &u), http.StatusForbidden)
	})
}

func TestRolesDePlataformaAMedida(t *testing.T) {
	t.Run("el superadmin crea un rol con permisos de plataforma", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		rec := h.do(http.MethodPost, platformRolesURL, map[string]any{
			"code": "soporte", "name": "Soporte", "permissions": []string{"platform.users.read", "platform.roles.read"},
		}, &sa)
		want(t, rec, http.StatusCreated)
		r := jsonMap(t, rec)
		if r["scope"] != "platform" || r["organization_id"] != nil || r["is_system"] != false {
			t.Fatalf("rol = %v", r)
		}
	})

	t.Run("valida alcance, código y repetidos", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("x1", "X", "org.members.read"), &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("Mal Codigo", "X"), &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("superadmin", "Falso"), &sa), http.StatusConflict)
		crearRolDePlataforma(t, h, sa, "soporte")
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("soporte", "Otro"), &sa), http.StatusConflict)
	})

	t.Run("solo con platform.roles.manage", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "solo-lectura", "platform.roles.read")
		lector := conRolDePlataforma(t, h, sa, "solo-lectura")
		normal := h.user()
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("x1", "X"), &lector), http.StatusForbidden)
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("x1", "X"), &normal), http.StatusForbidden)
	})

	t.Run("nadie otorga permisos de plataforma que no tiene", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "gestor-roles", "platform.roles.manage", "platform.roles.read")
		gestor := conRolDePlataforma(t, h, sa, "gestor-roles")

		// Tiene roles.manage, pero no users.manage: no puede armar un rol que lo dé.
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("mas-poder", "Más poder", "platform.users.manage"), &gestor), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM roles WHERE code = 'mas-poder'`); n != 0 {
			t.Fatal("se creó un rol con permisos que el creador no tiene")
		}
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("igual", "Igual", "platform.roles.read"), &gestor), http.StatusCreated)
	})

	t.Run("se edita y se borra; los de sistema no", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := crearRolDePlataforma(t, h, sa, "soporte", "platform.users.read")

		rec := h.do(http.MethodPatch, platformRolesURL+"/"+id, map[string]any{"name": "Soporte N1", "permissions": []string{"platform.roles.read"}}, &sa)
		want(t, rec, http.StatusOK)
		if r := jsonMap(t, rec); r["name"] != "Soporte N1" {
			t.Fatalf("rol = %v", r)
		}

		superadminID := str(t, `SELECT id::text FROM roles WHERE code = 'superadmin'`)
		want(t, h.do(http.MethodPatch, platformRolesURL+"/"+superadminID, map[string]any{"permissions": []string{}}, &sa), http.StatusConflict)
		want(t, h.do(http.MethodDelete, platformRolesURL+"/"+superadminID, nil, &sa), http.StatusConflict)

		want(t, h.do(http.MethodDelete, platformRolesURL+"/"+id, nil, &sa), http.StatusNoContent)
		want(t, h.do(http.MethodDelete, platformRolesURL+"/"+id, nil, &sa), http.StatusNotFound)
	})

	t.Run("un rol de organización no se toca por la ruta de plataforma", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, _, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor")
		want(t, h.do(http.MethodPatch, platformRolesURL+"/"+id, map[string]any{"name": "X"}, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodDelete, platformRolesURL+"/"+id, nil, &sa), http.StatusNotFound)
	})

	t.Run("un rol asignado no se borra", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := crearRolDePlataforma(t, h, sa, "soporte")
		u := conRolDePlataforma(t, h, sa, "soporte")

		want(t, h.do(http.MethodDelete, platformRolesURL+"/"+id, nil, &sa), http.StatusConflict)
		want(t, h.do(http.MethodDelete, grantURL(u)+"/soporte", nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodDelete, platformRolesURL+"/"+id, nil, &sa), http.StatusNoContent)
	})
}

func TestAsignarRolesDePlataforma(t *testing.T) {
	t.Run("un rol a medida da exactamente sus permisos", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "soporte", "platform.users.read")
		u := h.user()

		rec := h.do(http.MethodPost, grantURL(u), map[string]string{"role": "soporte"}, &sa)
		want(t, rec, http.StatusOK)
		roles, _ := jsonMap(t, rec)["roles"].([]any)
		if len(roles) != 1 || roles[0].(map[string]any)["code"] != "soporte" {
			t.Fatalf("roles = %v", roles)
		}

		want(t, h.do(http.MethodGet, "/v1/platform/users", nil, &u), http.StatusOK)           // lo que da su rol
		want(t, h.do(http.MethodGet, "/v1/platform/overview", nil, &u), http.StatusForbidden) // lo que no
		want(t, h.do(http.MethodGet, "/v1/platform/roles", nil, &u), http.StatusForbidden)    // lo que no
		want(t, h.do(http.MethodPatch, usersURL+u.ID.String()+"/active", map[string]any{"is_active": false}, &u), http.StatusForbidden)

		// /users/me lo refleja, sin volverlo superadmin.
		me := jsonMap(t, h.do(http.MethodGet, "/v1/users/me", nil, &u))
		if me["is_general_admin"] != false {
			t.Fatalf("un rol a medida no es superadmin: %v", me)
		}
		perms, _ := me["permissions"].([]any)
		if len(perms) != 1 || perms[0] != "platform.users.read" {
			t.Fatalf("permissions = %v", perms)
		}
	})

	t.Run("quitar el rol quita los permisos en la siguiente petición", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "soporte", "platform.users.read")
		u := conRolDePlataforma(t, h, sa, "soporte")
		want(t, h.do(http.MethodGet, "/v1/platform/users", nil, &u), http.StatusOK)

		want(t, h.do(http.MethodDelete, grantURL(u)+"/soporte", nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodGet, "/v1/platform/users", nil, &u), http.StatusForbidden)
	})

	t.Run("asignar dos veces es idempotente; quitar uno que no tiene, también", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "soporte")
		u := h.user()
		darRol(t, h, sa, u, "soporte")
		darRol(t, h, sa, u, "soporte")
		if n := count(t, `SELECT count(*) FROM user_platform_roles WHERE user_id = $1`, u.ID); n != 1 {
			t.Fatalf("asignaciones = %d, quería 1", n)
		}
		want(t, h.do(http.MethodDelete, grantURL(u)+"/soporte", nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodDelete, grantURL(u)+"/soporte", nil, &sa), http.StatusOK)
	})

	t.Run("se puede dar más de un rol y también por id", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		idSoporte := crearRolDePlataforma(t, h, sa, "soporte")
		u := h.user()
		darRol(t, h, sa, u, idSoporte)
		darRol(t, h, sa, u, "delivery")
		rec := h.do(http.MethodPost, grantURL(u), map[string]string{"role": "customer"}, &sa)
		want(t, rec, http.StatusOK)
		if roles, _ := jsonMap(t, rec)["roles"].([]any); len(roles) != 3 {
			t.Fatalf("roles = %v, quería 3", roles)
		}
	})

	t.Run("valida el rol y el usuario", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		u := h.user()
		want(t, h.do(http.MethodPost, grantURL(u), map[string]string{}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, grantURL(u), map[string]string{"role": "no-existe"}, &sa), http.StatusUnprocessableEntity)
		// admin es un rol de ORGANIZACIÓN: no se da como rol de plataforma.
		want(t, h.do(http.MethodPost, grantURL(u), map[string]string{"role": "admin"}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, usersURL+uuid.NewString()+"/roles", map[string]string{"role": "delivery"}, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodPost, usersURL+"no-es-uuid/roles", map[string]string{"role": "delivery"}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodDelete, grantURL(u)+"/no-existe", nil, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodDelete, grantURL(u)+"/admin", nil, &sa), http.StatusNotFound)
	})

	t.Run("solo con platform.roles.manage", func(t *testing.T) {
		h := newHarness(t)
		normal, u := h.user(), h.user()
		adminOrg := h.user()
		h.join(adminOrg, h.org("Acme"), "admin")
		for _, as := range []*person{&normal, &adminOrg} {
			want(t, h.do(http.MethodPost, grantURL(u), map[string]string{"role": "delivery"}, as), http.StatusForbidden)
			want(t, h.do(http.MethodDelete, grantURL(u)+"/delivery", nil, as), http.StatusForbidden)
		}
		want(t, h.do(http.MethodPost, grantURL(u), map[string]string{"role": "delivery"}, nil), http.StatusUnauthorized)
	})

	// La misma regla que en organizaciones: nadie otorga (ni quita) más de lo que tiene.
	t.Run("nadie otorga ni quita un rol con más permisos de los que tiene", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "gestor-roles", "platform.roles.manage", "platform.roles.read")
		gestor := conRolDePlataforma(t, h, sa, "gestor-roles")
		otro := h.user()

		// No puede darse ni dar el superadmin: tiene menos permisos que él.
		want(t, h.do(http.MethodPost, grantURL(gestor), map[string]string{"role": "superadmin"}, &gestor), http.StatusForbidden)
		want(t, h.do(http.MethodPost, grantURL(otro), map[string]string{"role": "superadmin"}, &gestor), http.StatusForbidden)
		if hasRole(t, gestor.ID, "superadmin") || hasRole(t, otro.ID, "superadmin") {
			t.Fatal("un gestor de roles se volvió superadmin: escalada de privilegios")
		}
		// Ni quitarle el rol a un superadmin.
		want(t, h.do(http.MethodDelete, grantURL(sa)+"/superadmin", nil, &gestor), http.StatusForbidden)
		if !hasRole(t, sa.ID, "superadmin") {
			t.Fatal("un gestor de roles le quitó el rol a un superadmin")
		}
		// Sí puede dar roles que no tienen permisos que él no tenga.
		want(t, h.do(http.MethodPost, grantURL(otro), map[string]string{"role": "delivery"}, &gestor), http.StatusOK)
		want(t, h.do(http.MethodPost, grantURL(otro), map[string]string{"role": "gestor-roles"}, &gestor), http.StatusOK)
	})
}

func TestPromoverASuperadminUsaElModeloDeRoles(t *testing.T) {
	t.Run("PATCH /users/{id}/promote da el rol superadmin y es idempotente", func(t *testing.T) {
		h := newHarness(t)
		sa, objetivo := h.user(superadmin()), h.user()
		url := "/v1/users/" + objetivo.ID.String() + "/promote"
		want(t, h.do(http.MethodPatch, url, nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodPatch, url, nil, &sa), http.StatusOK)
		if !hasRole(t, objetivo.ID, "superadmin") {
			t.Fatal("no quedó con el rol superadmin")
		}
		if n := count(t, `SELECT count(*) FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
			WHERE upr.user_id = $1 AND r.code = 'superadmin'`, objetivo.ID); n != 1 {
			t.Fatalf("asignaciones = %d, quería 1", n)
		}
		// Y desde ese momento puede usar el panel.
		want(t, h.do(http.MethodGet, "/v1/platform/overview", nil, &objetivo), http.StatusOK)
	})

	t.Run("un gestor de roles no puede promover: tiene menos permisos que el superadmin", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "gestor-roles", "platform.roles.manage")
		gestor := conRolDePlataforma(t, h, sa, "gestor-roles")
		want(t, h.do(http.MethodPatch, "/v1/users/"+gestor.ID.String()+"/promote", nil, &gestor), http.StatusForbidden)
		if hasRole(t, gestor.ID, "superadmin") {
			t.Fatal("un gestor se promovió a sí mismo")
		}
	})
}

// La plataforma no puede quedarse sin un superadmin activo: nadie podría crear
// organizaciones ni arreglarlo por la API.
func TestLaPlataformaNoSeQuedaSinSuperadmin(t *testing.T) {
	t.Run("nadie se quita a sí mismo el rol de superadmin", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		want(t, h.do(http.MethodDelete, grantURL(sa)+"/superadmin", nil, &sa), http.StatusConflict)
		if !hasRole(t, sa.ID, "superadmin") {
			t.Fatal("se quitó el rol a sí mismo")
		}
	})

	t.Run("un superadmin puede quitarle el rol a otro mientras quede alguno", func(t *testing.T) {
		h := newHarness(t)
		sa1, sa2 := h.user(superadmin()), h.user(superadmin())
		want(t, h.do(http.MethodDelete, grantURL(sa2)+"/superadmin", nil, &sa1), http.StatusOK)
		if hasRole(t, sa2.ID, "superadmin") || !hasRole(t, sa1.ID, "superadmin") {
			t.Fatal("no se quitó el rol correcto")
		}
	})

	t.Run("dos superadmins que se quitan el rol a la vez: queda exactamente uno", func(t *testing.T) {
		h := newHarness(t)
		sa1, sa2 := h.user(superadmin()), h.user(superadmin())

		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i, pair := range [][2]person{{sa1, sa2}, {sa2, sa1}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				actor, target := pair[0], pair[1]
				codes[i] = h.do(http.MethodDelete, grantURL(target)+"/superadmin", nil, &actor).Code
			}()
		}
		wg.Wait()

		// Uno gana (200). El otro, o llegó tarde y ya no tiene permisos (403), o fue
		// frenado por la protección del último superadmin (409). Nunca los dos 200.
		ok := 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusForbidden, http.StatusConflict:
			default:
				t.Fatalf("códigos = %v: un resultado inesperado", codes)
			}
		}
		if ok != 1 {
			t.Fatalf("códigos = %v, quería exactamente un 200", codes)
		}
		if n := count(t, `SELECT count(*) FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
			JOIN users u ON u.id = upr.user_id WHERE r.code = 'superadmin' AND u.is_active`); n != 1 {
			t.Fatalf("superadmins activos = %d, quería 1: la plataforma quedó sin operador o con un fantasma", n)
		}
	})
}
