package app_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
)

// Roles personalizados de una organización. La regla que gobierna todo: nadie
// otorga más de lo que tiene. Un admin de organización solo puede crear y asignar
// roles cuyos permisos ya tiene Y que sean delegables; sin eso se fabricaría un rol
// equivalente a "admin" y el rol de administrador se propagaría solo.

func roleReq(code, name string, perms ...string) map[string]any {
	if perms == nil {
		perms = []string{}
	}
	return map[string]any{"code": code, "name": name, "permissions": perms}
}

func orgRolesURL(o organization) string { return "/v1/organizations/" + o.ID.String() + "/roles" }

// crearRol crea un rol de organización y devuelve su id. Falla si no da 201.
func crearRol(t *testing.T, h *harness, actor person, o organization, code string, perms ...string) string {
	t.Helper()
	rec := h.do(http.MethodPost, orgRolesURL(o), roleReq(code, "Rol "+code, perms...), &actor)
	want(t, rec, http.StatusCreated)
	return jsonMap(t, rec)["id"].(string)
}

// codigosDeRoles devuelve los códigos de un listado de roles.
func codigosDeRoles(t *testing.T, h *harness, url string, as person) []string {
	t.Helper()
	rec := h.do(http.MethodGet, url, nil, &as)
	want(t, rec, http.StatusOK)
	var codes []string
	for _, r := range decode[[]map[string]any](t, rec) {
		codes = append(codes, r["code"].(string))
	}
	return codes
}

// orgConAdmin arma una organización con un admin y un empleado.
func orgConAdmin(h *harness, name string) (admin, empleado person, o organization) {
	admin, empleado, o = h.user(), h.user(), h.org(name)
	h.setPlan(o, "enterprise") // sin tope de miembros: estas pruebas no tratan de los límites del plan
	h.join(admin, o, "admin")
	h.join(empleado, o, "employee")
	return admin, empleado, o
}

func TestCrearRolesPersonalizadosDeOrganizacion(t *testing.T) {
	t.Run("un admin de organización crea un rol con permisos delegables", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")

		rec := h.do(http.MethodPost, orgRolesURL(o),
			map[string]any{"code": "supervisor", "name": "Supervisor", "description": "Ve al equipo", "permissions": []string{"org.members.read"}}, &admin)
		want(t, rec, http.StatusCreated)

		r := jsonMap(t, rec)
		if r["code"] != "supervisor" || r["scope"] != "organization" || r["organization_id"] != o.ID.String() ||
			r["is_system"] != false || r["org_assignable"] != true || r["description"] != "Ve al equipo" {
			t.Fatalf("rol = %v", r)
		}
		if perms, _ := r["permissions"].([]any); len(perms) != 1 || perms[0] != "org.members.read" {
			t.Fatalf("permissions = %v", r["permissions"])
		}
	})

	t.Run("un rol sin permisos es válido (un observador)", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		rec := h.do(http.MethodPost, orgRolesURL(o), roleReq("observador", "Observador"), &admin)
		want(t, rec, http.StatusCreated)
		if perms, ok := jsonMap(t, rec)["permissions"].([]any); !ok || len(perms) != 0 {
			t.Fatalf("permissions = %v, quería [] y no null", jsonMap(t, rec)["permissions"])
		}
	})

	t.Run("un empleado y un extraño no pueden crear roles", func(t *testing.T) {
		h := newHarness(t)
		_, empleado, o := orgConAdmin(h, "Acme")
		extrano := h.user()
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("x1", "X"), &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("x1", "X"), &extrano), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM roles WHERE NOT is_system`); n != 0 {
			t.Fatalf("se crearon %d roles pese al 403", n)
		}
	})

	t.Run("una organización inexistente da 403 a quien no es superadmin", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		rec := h.do(http.MethodPost, "/v1/organizations/"+uuid.NewString()+"/roles", roleReq("x1", "X"), &u)
		want(t, rec, http.StatusForbidden)
	})

	// Sin esta regla un admin se fabrica un rol equivalente a "admin" y el rol de
	// administrador se propaga solo.
	t.Run("no puede otorgar permisos de administración: no son delegables", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		for _, perm := range []string{"org.members.manage", "org.roles.manage", "org.audit.read"} {
			rec := h.do(http.MethodPost, orgRolesURL(o), roleReq("clon-admin", "Clon", perm), &admin)
			want(t, rec, http.StatusForbidden)
		}
		if n := count(t, `SELECT count(*) FROM roles WHERE code = 'clon-admin'`); n != 0 {
			t.Fatal("se creó un rol con permisos de administración")
		}
	})

	t.Run("rechaza permisos de plataforma y permisos que no existen", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("x1", "X", "platform.roles.manage"), &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("x1", "X", "org.inventado"), &admin), http.StatusUnprocessableEntity)
	})

	t.Run("valida código y nombre", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		tests := []struct {
			name string
			body map[string]any
		}{
			{"código con mayúsculas", roleReq("Supervisor", "S")},
			{"código de un carácter", roleReq("a", "S")},
			{"código con espacio", roleReq("con espacio", "S")},
			{"código que empieza con número", roleReq("1abc", "S")},
			{"código vacío", roleReq("", "S")},
			{"nombre vacío", roleReq("valido", "  ")},
			{"nombre demasiado largo", roleReq("valido", strings.Repeat("n", 101))},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				want(t, h.do(http.MethodPost, orgRolesURL(o), tc.body, &admin), http.StatusUnprocessableEntity)
			})
		}
	})

	t.Run("no admite el código de un rol de sistema ni repetir uno propio", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		// Un rol "admin" personalizado se confundiría con el de sistema al asignarlo por código.
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("admin", "Falso admin"), &admin), http.StatusConflict)
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("employee", "Falso empleado"), &admin), http.StatusConflict)
		crearRol(t, h, admin, o, "supervisor")
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("supervisor", "Otro"), &admin), http.StatusConflict)
	})

	t.Run("el mismo código puede existir en organizaciones distintas", func(t *testing.T) {
		h := newHarness(t)
		a1, _, o1 := orgConAdmin(h, "Uno")
		a2, _, o2 := orgConAdmin(h, "Dos")
		crearRol(t, h, a1, o1, "supervisor")
		crearRol(t, h, a2, o2, "supervisor")
	})

	t.Run("el superadmin puede armar un rol con permisos de administración", func(t *testing.T) {
		h := newHarness(t)
		sa, _, o := h.user(superadmin()), h.user(), h.org("Acme")
		rec := h.do(http.MethodPost, orgRolesURL(o), roleReq("co-admin", "Co-admin", "org.members.manage", "org.members.read"), &sa)
		want(t, rec, http.StatusCreated)
	})

	t.Run("una organización inexistente da 404 al superadmin", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		want(t, h.do(http.MethodPost, "/v1/organizations/"+uuid.NewString()+"/roles", roleReq("x1", "X"), &sa), http.StatusNotFound)
	})

	t.Run("los permisos repetidos se colapsan y quedan ordenados", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		rec := h.do(http.MethodPost, orgRolesURL(o), roleReq("orden", "Orden", "org.roles.read", "org.members.read", "org.members.read"), &admin)
		want(t, rec, http.StatusCreated)
		perms, _ := jsonMap(t, rec)["permissions"].([]any)
		if len(perms) != 2 || perms[0] != "org.members.read" || perms[1] != "org.roles.read" {
			t.Fatalf("permissions = %v", perms)
		}
	})
}

func TestListarRolesDeUnaOrganizacion(t *testing.T) {
	t.Run("incluye los de sistema y los propios, y no los de otra organización", func(t *testing.T) {
		h := newHarness(t)
		a1, _, o1 := orgConAdmin(h, "Uno")
		a2, _, o2 := orgConAdmin(h, "Dos")
		crearRol(t, h, a1, o1, "supervisor")
		crearRol(t, h, a2, o2, "otro-rol")

		codes := codigosDeRoles(t, h, orgRolesURL(o1), a1)
		for _, c := range []string{"admin", "employee", "supervisor"} {
			if !slices.Contains(codes, c) {
				t.Errorf("falta %q en %v", c, codes)
			}
		}
		if slices.Contains(codes, "otro-rol") {
			t.Fatalf("el listado de Uno muestra un rol de Dos: %v", codes)
		}
		// Los roles de plataforma no se ofrecen para una organización.
		for _, c := range []string{"superadmin", "delivery", "customer"} {
			if slices.Contains(codes, c) {
				t.Errorf("un rol de plataforma (%s) aparece entre los de la organización", c)
			}
		}
	})

	t.Run("un empleado (sin org.roles.read) y un extraño reciben 403", func(t *testing.T) {
		h := newHarness(t)
		_, empleado, o := orgConAdmin(h, "Acme")
		extrano := h.user()
		want(t, h.do(http.MethodGet, orgRolesURL(o), nil, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodGet, orgRolesURL(o), nil, &extrano), http.StatusForbidden)
	})

	t.Run("cada rol trae sus permisos", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		rec := h.do(http.MethodGet, orgRolesURL(o), nil, &admin)
		want(t, rec, http.StatusOK)
		for _, r := range decode[[]map[string]any](t, rec) {
			perms, ok := r["permissions"].([]any)
			if !ok {
				t.Fatalf("el rol %v no trae permissions como lista", r["code"])
			}
			if want := len(access.AllInScope(access.ScopeOrganization)); r["code"] == "admin" && len(perms) != want {
				t.Errorf("admin debe tener los %d permisos de organización, tiene %v", want, perms)
			}
			if r["code"] == "employee" && len(perms) != 7 {
				t.Errorf("employee debe atender la bandeja, gestionar contactos, consultar el catálogo y gestionar pedidos (7 permisos), tiene %v", perms)
			}
		}
	})
}

func TestEditarYBorrarRolesDeOrganizacion(t *testing.T) {
	t.Run("un admin edita nombre y permisos de un rol propio", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor", "org.members.read")

		rec := h.do(http.MethodPatch, orgRolesURL(o)+"/"+id,
			map[string]any{"name": "Supervisor de turno", "permissions": []string{"org.roles.read"}}, &admin)
		want(t, rec, http.StatusOK)
		r := jsonMap(t, rec)
		if r["name"] != "Supervisor de turno" {
			t.Fatalf("name = %v", r["name"])
		}
		if perms, _ := r["permissions"].([]any); len(perms) != 1 || perms[0] != "org.roles.read" {
			t.Fatalf("los permisos se reemplazan por los nuevos, no se suman: %v", perms)
		}
		// El código no cambia: es lo que otras cosas referencian.
		if r["code"] != "supervisor" {
			t.Fatalf("code = %v", r["code"])
		}
	})

	t.Run("un cuerpo vacío no cambia nada", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor", "org.members.read")
		rec := h.do(http.MethodPatch, orgRolesURL(o)+"/"+id, map[string]any{}, &admin)
		want(t, rec, http.StatusOK)
		if perms, _ := jsonMap(t, rec)["permissions"].([]any); len(perms) != 1 {
			t.Fatalf("un PATCH vacío borró los permisos: %v", perms)
		}
	})

	t.Run("al editar tampoco puede otorgar permisos de administración", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor", "org.members.read")
		rec := h.do(http.MethodPatch, orgRolesURL(o)+"/"+id, map[string]any{"permissions": []string{"org.members.manage"}}, &admin)
		want(t, rec, http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
			WHERE r.code = 'supervisor' AND rp.permission_code = 'org.members.manage'`); n != 0 {
			t.Fatal("el rol quedó con un permiso de administración pese al 403")
		}
	})

	t.Run("los roles de sistema no se editan ni se borran", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		adminRoleID := str(t, `SELECT id::text FROM roles WHERE code = 'admin' AND is_system`)

		want(t, h.do(http.MethodPatch, orgRolesURL(o)+"/"+adminRoleID, map[string]any{"name": "Otro"}, &admin), http.StatusConflict)
		want(t, h.do(http.MethodPatch, orgRolesURL(o)+"/"+adminRoleID, map[string]any{"permissions": []string{}}, &admin), http.StatusConflict)
		want(t, h.do(http.MethodDelete, orgRolesURL(o)+"/"+adminRoleID, nil, &admin), http.StatusConflict)
		if n := count(t, `SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.code = 'admin'`); n != len(access.AllInScope(access.ScopeOrganization)) {
			t.Fatalf("el rol admin tiene %d permisos, debía seguir con todos los de organización", n)
		}
	})

	t.Run("no se puede tocar el rol de otra organización", func(t *testing.T) {
		h := newHarness(t)
		a1, _, o1 := orgConAdmin(h, "Uno")
		a2, _, o2 := orgConAdmin(h, "Dos")
		idDeUno := crearRol(t, h, a1, o1, "supervisor")

		// Por la ruta de su propia organización: el rol no existe en ese ámbito.
		want(t, h.do(http.MethodPatch, orgRolesURL(o2)+"/"+idDeUno, map[string]any{"name": "Robado"}, &a2), http.StatusNotFound)
		want(t, h.do(http.MethodDelete, orgRolesURL(o2)+"/"+idDeUno, nil, &a2), http.StatusNotFound)
		// Por la ruta de la otra: no es miembro.
		want(t, h.do(http.MethodPatch, orgRolesURL(o1)+"/"+idDeUno, map[string]any{"name": "Robado"}, &a2), http.StatusForbidden)
		if got := str(t, `SELECT name FROM roles WHERE id = $1`, idDeUno); got != "Rol supervisor" {
			t.Fatalf("name = %q: el rol fue modificado por otra organización", got)
		}
	})

	t.Run("un empleado no puede editar ni borrar roles", func(t *testing.T) {
		h := newHarness(t)
		admin, empleado, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor")
		want(t, h.do(http.MethodPatch, orgRolesURL(o)+"/"+id, map[string]any{"name": "X"}, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodDelete, orgRolesURL(o)+"/"+id, nil, &empleado), http.StatusForbidden)
	})

	t.Run("un rol en uso no se borra; libre, sí", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor")
		miembro := h.user()
		want(t, h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members",
			map[string]string{"user_id": miembro.ID.String(), "role": "supervisor"}, &admin), http.StatusCreated)

		want(t, h.do(http.MethodDelete, orgRolesURL(o)+"/"+id, nil, &admin), http.StatusConflict)

		want(t, h.do(http.MethodDelete, "/v1/organizations/"+o.ID.String()+"/members/"+miembro.ID.String(), nil, &admin), http.StatusNoContent)
		want(t, h.do(http.MethodDelete, orgRolesURL(o)+"/"+id, nil, &admin), http.StatusNoContent)
		if n := count(t, `SELECT count(*) FROM roles WHERE id = $1`, id); n != 0 {
			t.Fatal("el rol no se borró")
		}
	})

	t.Run("un rol inexistente da 404 y un id mal formado 422", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		want(t, h.do(http.MethodPatch, orgRolesURL(o)+"/"+uuid.NewString(), map[string]any{"name": "X"}, &admin), http.StatusNotFound)
		want(t, h.do(http.MethodDelete, orgRolesURL(o)+"/no-es-uuid", nil, &admin), http.StatusUnprocessableEntity)
	})

	t.Run("al borrar una organización se borran sus roles personalizados y no los de sistema", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		crearRol(t, h, admin, o, "supervisor")
		// La membresía referencia roles con ON DELETE RESTRICT: se quitan antes.
		if _, err := pool.Exec(t.Context(), `DELETE FROM user_organizations WHERE organization_id = $1`, o.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `DELETE FROM organizations WHERE id = $1`, o.ID); err != nil {
			t.Fatal(err)
		}
		if n := count(t, `SELECT count(*) FROM roles WHERE code = 'supervisor'`); n != 0 {
			t.Fatal("el rol personalizado sobrevivió a su organización")
		}
		if n := count(t, `SELECT count(*) FROM roles WHERE is_system`); n != 5 {
			t.Fatalf("roles de sistema = %d, debían seguir siendo 5", n)
		}
	})
}
