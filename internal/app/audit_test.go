package app_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// La auditoría de roles: quién cambió qué y cuándo. Se escribe en la MISMA
// transacción que el cambio, así que no puede quedarse atrás ni registrar algo que
// no ocurrió.

type entrada struct {
	Action         string         `json:"action"`
	ActorID        *string        `json:"actor_id"`
	TargetUserID   *string        `json:"target_user_id"`
	OrganizationID *string        `json:"organization_id"`
	RoleCode       *string        `json:"role_code"`
	Detail         map[string]any `json:"detail"`
}

// auditoria devuelve las entradas de un listado de auditoría y su total.
func auditoria(t *testing.T, h *harness, path string, as person) ([]entrada, float64) {
	t.Helper()
	rec := h.do(http.MethodGet, path, nil, &as)
	want(t, rec, http.StatusOK)
	body := decode[struct {
		Items []entrada `json:"items"`
		Total float64   `json:"total"`
	}](t, rec)
	if body.Items == nil {
		t.Fatal("items debe ser una lista y no null")
	}
	return body.Items, body.Total
}

func acciones(es []entrada) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Action
	}
	return out
}

func contar(es []entrada, action string) int {
	n := 0
	for _, e := range es {
		if e.Action == action {
			n++
		}
	}
	return n
}

func TestLosCambiosDeRolesQuedanAuditados(t *testing.T) {
	t.Run("crear, asignar y quitar un rol de plataforma", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "soporte", "platform.users.read")
		u := conRolDePlataforma(t, h, sa, "soporte")
		want(t, h.do(http.MethodDelete, grantURL(u)+"/soporte", nil, &sa), http.StatusOK)

		es, total := auditoria(t, h, "/v1/platform/audit", sa)
		if total != 3 || contar(es, "role.created") != 1 || contar(es, "platform_role.granted") != 1 || contar(es, "platform_role.revoked") != 1 {
			t.Fatalf("acciones = %v", acciones(es))
		}
		// La más reciente primero.
		if es[0].Action != "platform_role.revoked" {
			t.Fatalf("el orden debe ser del más reciente al más antiguo: %v", acciones(es))
		}
		for _, e := range es {
			if e.ActorID == nil || *e.ActorID != sa.ID.String() {
				t.Errorf("%s sin el actor correcto: %v", e.Action, e.ActorID)
			}
		}
		granted := es[1]
		if granted.TargetUserID == nil || *granted.TargetUserID != u.ID.String() || granted.RoleCode == nil || *granted.RoleCode != "soporte" {
			t.Fatalf("entrada = %+v", granted)
		}
	})

	t.Run("asignar dos veces el mismo rol se audita una sola vez", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "soporte")
		u := h.user()
		darRol(t, h, sa, u, "soporte")
		darRol(t, h, sa, u, "soporte")
		want(t, h.do(http.MethodDelete, grantURL(u)+"/soporte", nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodDelete, grantURL(u)+"/soporte", nil, &sa), http.StatusOK) // ya no lo tenía

		es, _ := auditoria(t, h, "/v1/platform/audit?user_id="+u.ID.String(), sa)
		if contar(es, "platform_role.granted") != 1 || contar(es, "platform_role.revoked") != 1 {
			t.Fatalf("acciones = %v: lo que no cambia nada no se audita", acciones(es))
		}
	})

	t.Run("una operación rechazada no deja rastro", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "soporte")
		want(t, h.do(http.MethodPost, platformRolesURL, roleReq("soporte", "Repetido"), &sa), http.StatusConflict)
		want(t, h.do(http.MethodDelete, grantURL(sa)+"/superadmin", nil, &sa), http.StatusConflict)

		es, total := auditoria(t, h, "/v1/platform/audit", sa)
		if total != 1 || contar(es, "role.created") != 1 {
			t.Fatalf("acciones = %v: solo la creación efectiva debía quedar", acciones(es))
		}
	})

	t.Run("miembros: sumar, invitar, cambiar de rol y sacar", func(t *testing.T) {
		h := newHarness(t)
		admin, empleado, o := orgConAdmin(h, "Acme")
		crearRol(t, h, admin, o, "supervisor", "org.members.read")
		nuevo := h.user()
		want(t, h.do(http.MethodPost, membersURL(o), map[string]string{"user_id": nuevo.ID.String(), "role": "employee"}, &admin), http.StatusCreated)
		want(t, h.do(http.MethodPost, membersURL(o)+"/invite", map[string]string{"email": "inv@ejemplo.com", "role": "employee"}, &admin), http.StatusCreated)
		want(t, h.do(http.MethodPatch, membersURL(o)+"/"+empleado.ID.String(), map[string]string{"role": "supervisor"}, &admin), http.StatusOK)
		want(t, h.do(http.MethodDelete, membersURL(o)+"/"+nuevo.ID.String(), nil, &admin), http.StatusNoContent)

		es, _ := auditoria(t, h, "/v1/organizations/"+o.ID.String()+"/audit", admin)
		for _, a := range []string{"role.created", "member.added", "member.invited", "member.role_changed", "member.removed"} {
			if contar(es, a) != 1 {
				t.Errorf("falta la acción %s en %v", a, acciones(es))
			}
		}
		for _, e := range es {
			if e.Action == "member.role_changed" {
				if e.Detail["from"] != "employee" || e.Detail["to"] != "supervisor" {
					t.Errorf("el cambio de rol debe registrar de dónde a dónde: %v", e.Detail)
				}
			}
			if e.OrganizationID == nil || *e.OrganizationID != o.ID.String() {
				t.Errorf("%s sin la organización: %+v", e.Action, e)
			}
		}
	})

	t.Run("activar y desactivar cuentas", func(t *testing.T) {
		h := newHarness(t)
		sa, u := h.user(superadmin()), h.user()
		want(t, h.do(http.MethodPatch, activeURL(u), map[string]any{"is_active": false}, &sa), http.StatusOK)
		want(t, h.do(http.MethodPatch, activeURL(u), map[string]any{"is_active": true}, &sa), http.StatusOK)
		es, _ := auditoria(t, h, "/v1/platform/audit?user_id="+u.ID.String(), sa)
		if contar(es, "user.deactivated") != 1 || contar(es, "user.activated") != 1 {
			t.Fatalf("acciones = %v", acciones(es))
		}
	})

	t.Run("aprobar un domiciliario audita el rol delivery", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusOK)

		es, _ := auditoria(t, h, "/v1/platform/audit?action=platform_role.granted", sa)
		if len(es) != 1 || es[0].RoleCode == nil || *es[0].RoleCode != "delivery" || es[0].Detail["via"] != "driver-application" {
			t.Fatalf("entradas = %+v", es)
		}
	})

	t.Run("el rol borrado deja su código en la auditoría", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := crearRolDePlataforma(t, h, sa, "efimero")
		want(t, h.do(http.MethodDelete, platformRolesURL+"/"+id, nil, &sa), http.StatusNoContent)

		es, _ := auditoria(t, h, "/v1/platform/audit", sa)
		if contar(es, "role.created") != 1 || contar(es, "role.deleted") != 1 {
			t.Fatalf("acciones = %v", acciones(es))
		}
		for _, e := range es {
			if e.RoleCode == nil || *e.RoleCode != "efimero" {
				t.Errorf("%s perdió el código del rol borrado: %+v", e.Action, e)
			}
		}
	})
}

func TestConsultarLaAuditoria(t *testing.T) {
	t.Run("filtra por acción, usuario y organización, y pagina", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		a1, _, o1 := orgConAdmin(h, "Uno")
		a2, _, o2 := orgConAdmin(h, "Dos")
		crearRol(t, h, a1, o1, "r1")
		crearRol(t, h, a2, o2, "r2")
		crearRol(t, h, a2, o2, "r3")
		u := h.user()
		want(t, h.do(http.MethodPost, membersURL(o2), map[string]string{"user_id": u.ID.String(), "role": "employee"}, &a2), http.StatusCreated)

		todo, total := auditoria(t, h, "/v1/platform/audit", sa)
		if total != 4 || len(todo) != 4 {
			t.Fatalf("total = %v", total)
		}
		if es, _ := auditoria(t, h, "/v1/platform/audit?action=role.created", sa); len(es) != 3 {
			t.Errorf("action=role.created: %v", acciones(es))
		}
		if es, _ := auditoria(t, h, "/v1/platform/audit?organization_id="+o2.ID.String(), sa); len(es) != 3 {
			t.Errorf("organization_id=o2: %v", acciones(es))
		}
		if es, _ := auditoria(t, h, "/v1/platform/audit?user_id="+u.ID.String(), sa); len(es) != 1 || es[0].Action != "member.added" {
			t.Errorf("user_id: %v", acciones(es))
		}
		pag, total := auditoria(t, h, "/v1/platform/audit?limit=2&offset=1", sa)
		if len(pag) != 2 || total != 4 {
			t.Errorf("página = %d, total = %v", len(pag), total)
		}
		want(t, h.do(http.MethodGet, "/v1/platform/audit?limit=0", nil, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodGet, "/v1/platform/audit?user_id=no-es-uuid", nil, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodGet, "/v1/platform/audit?organization_id=no-es-uuid", nil, &sa), http.StatusUnprocessableEntity)
	})

	t.Run("la de toda la plataforma exige platform.audit.read", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, _, _ := orgConAdmin(h, "Acme")
		crearRolDePlataforma(t, h, sa, "auditor", "platform.audit.read")
		auditor := conRolDePlataforma(t, h, sa, "auditor")

		want(t, h.do(http.MethodGet, "/v1/platform/audit", nil, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/platform/audit", nil, nil), http.StatusUnauthorized)
		want(t, h.do(http.MethodGet, "/v1/platform/audit", nil, &auditor), http.StatusOK)
		// Ver la auditoría no da poder de cambiar nada.
		want(t, h.do(http.MethodGet, "/v1/platform/users", nil, &auditor), http.StatusForbidden)
	})

	t.Run("la de una organización: solo la suya, y solo con org.audit.read", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		a1, e1, o1 := orgConAdmin(h, "Uno")
		a2, _, o2 := orgConAdmin(h, "Dos")
		crearRol(t, h, a1, o1, "de-uno")
		crearRol(t, h, a2, o2, "de-dos")
		extrano := h.user()

		es, total := auditoria(t, h, "/v1/organizations/"+o1.ID.String()+"/audit", a1)
		if total != 1 || len(es) != 1 || es[0].RoleCode == nil || *es[0].RoleCode != "de-uno" {
			t.Fatalf("Uno ve %v: solo debía ver lo suyo", es)
		}
		want(t, h.do(http.MethodGet, "/v1/organizations/"+o1.ID.String()+"/audit", nil, &e1), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/organizations/"+o1.ID.String()+"/audit", nil, &extrano), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/organizations/"+o1.ID.String()+"/audit", nil, &a2), http.StatusForbidden)
		if es, _ := auditoria(t, h, "/v1/organizations/"+o2.ID.String()+"/audit", sa); len(es) != 1 {
			t.Errorf("el superadmin debía ver la de Dos: %v", es)
		}
		want(t, h.do(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/audit", nil, &sa), http.StatusNotFound)
	})

	t.Run("un filtro organization_id no deja ver otra organización", func(t *testing.T) {
		// Por la ruta de Uno se ignora cualquier organization_id de la consulta.
		h := newHarness(t)
		a1, _, o1 := orgConAdmin(h, "Uno")
		a2, _, o2 := orgConAdmin(h, "Dos")
		crearRol(t, h, a1, o1, "de-uno")
		crearRol(t, h, a2, o2, "de-dos")
		es, _ := auditoria(t, h, "/v1/organizations/"+o1.ID.String()+"/audit?organization_id="+o2.ID.String(), a1)
		for _, e := range es {
			if e.OrganizationID == nil || *e.OrganizationID != o1.ID.String() {
				t.Fatalf("se filtró la auditoría de otra organización: %+v", e)
			}
		}
	})
}

func TestElAltaDeSuperadminPorLineaDeComandosQuedaAuditada(t *testing.T) {
	h := newHarness(t)
	if _, err := superadminService(h).Ensure(t.Context(), "ana@ejemplo.com", pwSuperadmin, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	must(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log
		WHERE action = 'platform_role.granted' AND role_code = 'superadmin' AND detail->>'via' = 'create-superadmin' AND actor_id IS NULL`).Scan(&n))
	if n != 1 {
		t.Fatalf("entradas de auditoría del alta = %d, quería 1 (sin actor y con via=create-superadmin)", n)
	}
}
