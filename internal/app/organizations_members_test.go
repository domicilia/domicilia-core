package app_test

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// Una organización nunca se queda sin nadie que pueda administrar a sus miembros.

func TestUnaOrganizacionNoSeQuedaSinAdministrador(t *testing.T) {
	t.Run("el único admin no puede irse ni degradarse", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		empleado := h.user()
		h.join(empleado, o, "employee")

		want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+admin.ID.String()), nil, &admin), http.StatusConflict)
		want(t, h.do(http.MethodPatch, orgURL(o, "/members/"+admin.ID.String()), map[string]string{"role": "employee"}, &admin), http.StatusConflict)

		if got := str(t, `SELECT r.code FROM user_organizations uo JOIN roles r ON r.id = uo.role_id WHERE uo.user_id = $1`, admin.ID); got != "admin" {
			t.Fatalf("rol = %q: el rechazo debía dejarlo como estaba", got)
		}
		acts := auditActions(t, o)
		if contains(acts, "member.removed") || contains(acts, "member.role_changed") {
			t.Errorf("lo rechazado no debe auditarse: %v", acts)
		}
	})

	t.Run("tampoco el superadmin puede dejarla sin administrador", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+admin.ID.String()), nil, &sa), http.StatusConflict)
		want(t, h.do(http.MethodPatch, orgURL(o, "/members/"+admin.ID.String()), map[string]string{"role": "employee"}, &sa), http.StatusConflict)
	})

	t.Run("con otro admin, uno sí puede irse", func(t *testing.T) {
		h := newHarness(t)
		a1, o := orgAdmin(h, "Acme")
		a2 := h.user()
		h.join(a2, o, "admin")
		want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+a1.ID.String()), nil, &a1), http.StatusNoContent)
		// Ahora a2 es el único: ya no puede.
		want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+a2.ID.String()), nil, &a2), http.StatusConflict)
	})

	t.Run("un empleado sí puede salir aunque haya un solo admin", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		empleado := h.user()
		h.join(empleado, o, "employee")
		want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+empleado.ID.String()), nil, &admin), http.StatusNoContent)
	})

	t.Run("una organización sin administrador no queda bloqueada para siempre", func(t *testing.T) {
		// Creada por el operador y aún sin invitación aceptada: solo hay empleados.
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.org("Sin Admin")
		e1, e2 := h.user(), h.user()
		h.join(e1, o, "employee")
		h.join(e2, o, "employee")
		want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+e1.ID.String()), nil, &sa), http.StatusNoContent)
		// Y se le puede nombrar un administrador.
		want(t, h.do(http.MethodPatch, orgURL(o, "/members/"+e2.ID.String()), map[string]string{"role": "admin"}, &sa), http.StatusOK)
	})

	t.Run("ascender a otro y luego irse: el rol que conserva la administración cuenta", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		a1, o := orgAdmin(h, "Acme")
		nuevo := h.user()
		h.join(nuevo, o, "employee")
		want(t, h.do(http.MethodPatch, orgURL(o, "/members/"+nuevo.ID.String()), map[string]string{"role": "admin"}, &sa), http.StatusOK)
		want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+a1.ID.String()), nil, &a1), http.StatusNoContent)
	})

	// Prueba DETERMINISTA de que los cambios de miembros se serializan: otra conexión
	// sujeta el bloqueo de la organización y la petición debe esperar hasta que lo suelte.
	// (La carrera de más abajo no basta: sin el bloqueo, la ventana de tiempo es tan
	// estrecha que una mutación que lo quita sobrevive.) La clave es la misma que usa
	// LockOrganizationMembers en db/queries/organizations.sql.
	t.Run("cada cambio de miembros espera el bloqueo de su organización", func(t *testing.T) {
		const lockKey = `hashtextextended('org-members:' || $1::text, 0)`
		for name, call := range map[string]func(h *harness, admin, target person, o organization) int{
			"sacar": func(h *harness, admin, target person, o organization) int {
				return h.do(http.MethodDelete, orgURL(o, "/members/"+target.ID.String()), nil, &admin).Code
			},
			"cambiar de rol": func(h *harness, admin, target person, o organization) int {
				return h.do(http.MethodPatch, orgURL(o, "/members/"+target.ID.String()), map[string]string{"role": "employee"}, &admin).Code
			},
		} {
			t.Run(name, func(t *testing.T) {
				h := newHarness(t)
				a1, o := orgAdmin(h, "Acme")
				a2 := h.user()
				h.join(a2, o, "admin")

				conn, err := pool.Acquire(t.Context())
				must(t, err)
				defer conn.Release()
				_, err = conn.Exec(t.Context(), `SELECT pg_advisory_lock(`+lockKey+`)`, o.ID.String())
				must(t, err)

				done := make(chan int, 1)
				go func() { done <- call(h, a1, a2, o) }()
				select {
				case code := <-done:
					t.Fatalf("la petición terminó (%d) sin esperar el bloqueo de la organización", code)
				case <-time.After(500 * time.Millisecond):
				}

				_, err = conn.Exec(t.Context(), `SELECT pg_advisory_unlock(`+lockKey+`)`, o.ID.String())
				must(t, err)
				select {
				case code := <-done:
					if code != http.StatusNoContent && code != http.StatusOK {
						t.Fatalf("tras soltar el bloqueo la petición dio %d", code)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("la petición no avanzó al soltar el bloqueo")
				}
			})
		}
	})

	// Dos administradores que se echan el uno al otro a la vez: cada petición, vista por
	// separado, "deja a uno". Sin serializar, las dos pasarían y quedaría cero.
	t.Run("dos salidas simultáneas no dejan la organización vacía", func(t *testing.T) {
		for range 8 {
			h := newHarness(t)
			a1, o := orgAdmin(h, "Acme")
			a2 := h.user()
			h.join(a2, o, "admin")

			var wg sync.WaitGroup
			codes := make(chan int, 2)
			for _, actor := range []struct{ by, target person }{{a1, a2}, {a2, a1}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					by := actor.by
					codes <- h.do(http.MethodDelete, orgURL(o, "/members/"+actor.target.ID.String()), nil, &by).Code
				}()
			}
			wg.Wait()
			close(codes)
			ok := 0
			for c := range codes {
				if c == http.StatusNoContent {
					ok++
				}
			}
			if ok != 1 {
				t.Fatalf("salidas exitosas = %d, quería exactamente 1", ok)
			}
			if n := count(t, `SELECT count(*) FROM user_organizations WHERE organization_id = $1`, o.ID); n != 1 {
				t.Fatalf("miembros = %d: la organización se quedó sin administrador", n)
			}
		}
	})
}

func TestUnaOrganizacionSuspendidaNoSeAdministra(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())
	admin, o := orgAdmin(h, "Acme")
	empleado, nuevo := h.user(), h.user()
	h.join(empleado, o, "employee")
	want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)

	want(t, h.do(http.MethodPost, orgURL(o, "/members"), map[string]string{"user_id": nuevo.ID.String(), "role": "employee"}, &admin), http.StatusForbidden)
	want(t, h.do(http.MethodPatch, orgURL(o, "/members/"+empleado.ID.String()), map[string]string{"role": "employee"}, &admin), http.StatusForbidden)
	want(t, h.do(http.MethodDelete, orgURL(o, "/members/"+empleado.ID.String()), nil, &admin), http.StatusForbidden)
	want(t, h.do(http.MethodPost, orgURL(o, "/members/invite"), map[string]string{"email": "n@ejemplo.com", "role": "employee"}, &admin), http.StatusForbidden)
	if n := count(t, `SELECT count(*) FROM user_organizations WHERE organization_id = $1`, o.ID); n != 2 {
		t.Fatalf("miembros = %d: una organización suspendida cambió de miembros", n)
	}
	// El operador sí, para poder ordenar lo que suspendió.
	want(t, h.do(http.MethodPost, orgURL(o, "/members"), map[string]string{"user_id": nuevo.ID.String(), "role": "employee"}, &sa), http.StatusCreated)
}

// La sesión (/users/me) trae el estado y el plan de cada organización, para que la
// interfaz sepa qué mostrar sin otra llamada; una archivada no entra.
func TestLaSesionTraeEstadoYPlanDeSusOrganizaciones(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())
	u := h.user()
	activa, suspendida, archivada := h.org("Activa"), h.orgStatus("Suspendida", "suspended"), h.orgStatus("Archivada", "archived")
	h.setPlan(activa, "pro")
	for _, o := range []organization{activa, suspendida, archivada} {
		h.join(u, o, "employee")
	}

	rec := h.do(http.MethodGet, "/v1/users/me", nil, &u)
	want(t, rec, http.StatusOK)
	got := map[string]string{}
	for _, m := range jsonMap(t, rec)["memberships"].([]any) {
		mm := m.(map[string]any)
		got[mm["organization_name"].(string)] = mm["organization_status"].(string) + "/" + mm["plan_tier"].(string)
	}
	if len(got) != 2 || got["Activa"] != "active/pro" || got["Suspendida"] != "suspended/starter" {
		t.Fatalf("memberships = %v, quería Activa y Suspendida (la archivada no entra)", got)
	}

	// El operador sí ve la archivada en el detalle del usuario.
	rec = h.do(http.MethodGet, "/v1/platform/users/"+u.ID.String(), nil, &sa)
	want(t, rec, http.StatusOK)
	if n := len(jsonMap(t, rec)["memberships"].([]any)); n != 3 {
		t.Fatalf("el detalle de plataforma trae %d membresías, quería 3", n)
	}
}
