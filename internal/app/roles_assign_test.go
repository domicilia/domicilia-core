package app_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Asignar roles a los miembros de una organización, y que sus permisos se apliquen
// de verdad. La regla es la misma: nadie otorga más de lo que tiene.

func membersURL(o organization) string { return "/v1/organizations/" + o.ID.String() + "/members" }

func addMember(h *harness, actor person, o organization, user person, role string) int {
	return h.do(http.MethodPost, membersURL(o), map[string]string{"user_id": user.ID.String(), "role": role}, &actor).Code
}

func TestAsignarRolesPersonalizadosAMiembros(t *testing.T) {
	t.Run("se asigna por código o por id", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor", "org.members.read")
		m1, m2 := h.user(), h.user()

		rec := h.do(http.MethodPost, membersURL(o), map[string]string{"user_id": m1.ID.String(), "role": "supervisor"}, &admin)
		want(t, rec, http.StatusCreated)
		body := jsonMap(t, rec)
		if body["role"] != "supervisor" || body["role_id"] != id || body["role_name"] != "Rol supervisor" {
			t.Fatalf("miembro = %v", body)
		}

		rec = h.do(http.MethodPost, membersURL(o), map[string]string{"user_id": m2.ID.String(), "role": id}, &admin)
		want(t, rec, http.StatusCreated)
		if jsonMap(t, rec)["role"] != "supervisor" {
			t.Fatalf("asignado por id: %v", jsonMap(t, rec))
		}
	})

	t.Run("los permisos del rol se aplican de verdad, ni más ni menos", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		crearRol(t, h, admin, o, "supervisor", "org.members.read")
		crearRol(t, h, admin, o, "lector-de-roles", "org.roles.read")
		sup, lector := h.user(), h.user()
		h.join(sup, o, "supervisor")
		h.join(lector, o, "lector-de-roles")

		// supervisor: solo ve a los miembros.
		want(t, h.do(http.MethodGet, membersURL(o), nil, &sup), http.StatusOK)
		want(t, h.do(http.MethodGet, orgRolesURL(o), nil, &sup), http.StatusForbidden)
		want(t, h.do(http.MethodPost, membersURL(o), map[string]string{"user_id": h.user().ID.String(), "role": "employee"}, &sup), http.StatusForbidden)
		want(t, h.do(http.MethodDelete, membersURL(o)+"/"+lector.ID.String(), nil, &sup), http.StatusForbidden)
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("x1", "X"), &sup), http.StatusForbidden)

		// lector de roles: solo ve los roles.
		want(t, h.do(http.MethodGet, orgRolesURL(o), nil, &lector), http.StatusOK)
		want(t, h.do(http.MethodGet, membersURL(o), nil, &lector), http.StatusForbidden)

		// Y ambos siguen siendo miembros: ven su organización.
		want(t, h.do(http.MethodGet, "/v1/organizations/"+o.ID.String(), nil, &sup), http.StatusOK)
		want(t, h.do(http.MethodGet, "/v1/organizations/"+o.ID.String(), nil, &lector), http.StatusOK)
	})

	t.Run("un cambio de permisos del rol se nota en la siguiente petición", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor", "org.members.read")
		sup := h.user()
		h.join(sup, o, "supervisor")
		want(t, h.do(http.MethodGet, membersURL(o), nil, &sup), http.StatusOK)

		want(t, h.do(http.MethodPatch, orgRolesURL(o)+"/"+id, map[string]any{"permissions": []string{}}, &admin), http.StatusOK)
		want(t, h.do(http.MethodGet, membersURL(o), nil, &sup), http.StatusForbidden)
	})

	t.Run("GET /users/me muestra el rol y sus permisos en cada organización", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		crearRol(t, h, admin, o, "supervisor", "org.members.read", "org.roles.read")
		sup := h.user()
		h.join(sup, o, "supervisor")

		rec := h.do(http.MethodGet, "/v1/users/me", nil, &sup)
		want(t, rec, http.StatusOK)
		ms, _ := jsonMap(t, rec)["memberships"].([]any)
		if len(ms) != 1 {
			t.Fatalf("membresías = %v", ms)
		}
		m := ms[0].(map[string]any)
		perms, _ := m["permissions"].([]any)
		if m["role"] != "supervisor" || m["role_name"] != "Rol supervisor" || len(perms) != 2 || perms[0] != "org.members.read" || perms[1] != "org.roles.read" {
			t.Fatalf("membresía = %v", m)
		}
	})

	t.Run("un admin no asigna un rol que no es asignable por organización", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		sa := h.user(superadmin())
		rec := h.do(http.MethodPost, orgRolesURL(o), map[string]any{"code": "restringido", "name": "R", "permissions": []string{}, "org_assignable": false}, &sa)
		want(t, rec, http.StatusCreated)

		if got := addMember(h, admin, o, h.user(), "restringido"); got != http.StatusForbidden {
			t.Fatalf("un admin asignó un rol no asignable: %d", got)
		}
		if got := addMember(h, sa, o, h.user(), "restringido"); got != http.StatusCreated {
			t.Fatalf("el superadmin sí puede: %d", got)
		}
	})

	// Un rol personalizado con permisos de administración puede existir (lo arma el
	// superadmin). Quien lo tiene NO puede dar más de lo que tiene.
	t.Run("nadie asigna un rol con más permisos de los que tiene", func(t *testing.T) {
		h := newHarness(t)
		_, _, o := orgConAdmin(h, "Acme")
		sa := h.user(superadmin())
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("gestor", "Gestor", "org.members.manage",
			"org.inbox.read", "org.inbox.reply", "org.contacts.read", "org.contacts.manage", "org.catalog.read",
			"org.orders.read", "org.orders.manage"), &sa), http.StatusCreated)
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("co-admin", "Co-admin", "org.members.manage", "org.roles.manage"), &sa), http.StatusCreated)
		gestor := h.user()
		h.join(gestor, o, "gestor")

		// gestor administra miembros y tiene lo que tiene el rol employee, pero no org.roles.manage.
		if got := addMember(h, gestor, o, h.user(), "co-admin"); got != http.StatusForbidden {
			t.Fatalf("un gestor asignó un rol con permisos que no tiene: %d", got)
		}
		if got := addMember(h, gestor, o, h.user(), "employee"); got != http.StatusCreated {
			t.Fatalf("un gestor debe poder asignar employee: %d", got)
		}
		// Ni siquiera "admin": es de sistema y no asignable por organización.
		if got := addMember(h, gestor, o, h.user(), "admin"); got != http.StatusForbidden {
			t.Fatalf("un gestor asignó admin: %d", got)
		}
	})

	t.Run("un rol de otra organización, uno de plataforma o uno desconocido no se asignan", func(t *testing.T) {
		h := newHarness(t)
		a1, _, o1 := orgConAdmin(h, "Uno")
		a2, _, o2 := orgConAdmin(h, "Dos")
		idDeUno := crearRol(t, h, a1, o1, "supervisor")
		superadminID := str(t, `SELECT id::text FROM roles WHERE code = 'superadmin'`)
		u := h.user()

		for _, ref := range []string{"supervisor", idDeUno, "superadmin", superadminID, "delivery", "no-existe", uuid.NewString()} {
			if got := addMember(h, a2, o2, u, ref); got != http.StatusUnprocessableEntity {
				t.Errorf("asignar %q en otra organización = %d, quería 422", ref, got)
			}
		}
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, u.ID); n != 0 {
			t.Fatal("se creó una membresía pese al rechazo")
		}
	})

	t.Run("invitar con un rol personalizado", func(t *testing.T) {
		h := newHarness(t)
		admin, _, o := orgConAdmin(h, "Acme")
		crearRol(t, h, admin, o, "supervisor", "org.members.read")

		rec := h.do(http.MethodPost, membersURL(o)+"/invite", map[string]any{"email": "nuevo@ejemplo.com", "role": "supervisor"}, &admin)
		want(t, rec, http.StatusCreated)
		if jsonMap(t, rec)["role"] != "supervisor" {
			t.Fatalf("respuesta = %v", jsonMap(t, rec))
		}
		if got := str(t, `SELECT r.code FROM user_organizations uo JOIN roles r ON r.id = uo.role_id
			JOIN users u ON u.id = uo.user_id WHERE u.email = 'nuevo@ejemplo.com'`); got != "supervisor" {
			t.Fatalf("rol guardado = %q", got)
		}
	})
}

func TestCambiarElRolDeUnMiembro(t *testing.T) {
	t.Run("un admin pasa a un empleado a un rol personalizado", func(t *testing.T) {
		h := newHarness(t)
		admin, empleado, o := orgConAdmin(h, "Acme")
		id := crearRol(t, h, admin, o, "supervisor", "org.members.read")

		rec := h.do(http.MethodPatch, membersURL(o)+"/"+empleado.ID.String(), map[string]string{"role": "supervisor"}, &admin)
		want(t, rec, http.StatusOK)
		if body := jsonMap(t, rec); body["role"] != "supervisor" || body["role_id"] != id || body["user_id"] != empleado.ID.String() {
			t.Fatalf("miembro = %v", body)
		}
		// Ahora tiene sus permisos.
		want(t, h.do(http.MethodGet, membersURL(o), nil, &empleado), http.StatusOK)
	})

	t.Run("un admin no puede subir a alguien a admin; el superadmin sí", func(t *testing.T) {
		h := newHarness(t)
		admin, empleado, o := orgConAdmin(h, "Acme")
		sa := h.user(superadmin())

		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+empleado.ID.String(), map[string]string{"role": "admin"}, &admin), http.StatusForbidden)
		if got := str(t, `SELECT r.code FROM user_organizations uo JOIN roles r ON r.id = uo.role_id WHERE uo.user_id = $1`, empleado.ID); got != "employee" {
			t.Fatalf("rol = %q: el admin escaló a alguien pese al 403", got)
		}
		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+empleado.ID.String(), map[string]string{"role": "admin"}, &sa), http.StatusOK)
	})

	t.Run("un empleado y un extraño no pueden cambiar roles", func(t *testing.T) {
		h := newHarness(t)
		admin, empleado, o := orgConAdmin(h, "Acme")
		extrano := h.user()
		crearRol(t, h, admin, o, "supervisor")
		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+admin.ID.String(), map[string]string{"role": "employee"}, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+empleado.ID.String(), map[string]string{"role": "supervisor"}, &extrano), http.StatusForbidden)
	})

	t.Run("quien no es miembro da 404; sin rol o con rol desconocido, 422", func(t *testing.T) {
		h := newHarness(t)
		admin, empleado, o := orgConAdmin(h, "Acme")
		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+h.user().ID.String(), map[string]string{"role": "employee"}, &admin), http.StatusNotFound)
		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+empleado.ID.String(), map[string]string{}, &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+empleado.ID.String(), map[string]string{"role": "no-existe"}, &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, membersURL(o)+"/no-es-uuid", map[string]string{"role": "employee"}, &admin), http.StatusUnprocessableEntity)
	})
}
