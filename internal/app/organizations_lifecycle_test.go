package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Alta, estados y listado de la plataforma. Lo que más se prueba es lo que debe
// RECHAZARSE: un fallo aquí no da un error visible, da un negocio suspendido que
// sigue operando, o uno ajeno que se ve.

func TestCrearOrganizacionCompleta(t *testing.T) {
	t.Run("un slug explícito se respeta y se normaliza a minúsculas", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		rec := h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Pizzería Norte", "slug": "  Pizza-Norte "}, &sa)
		want(t, rec, http.StatusCreated)
		if got := jsonMap(t, rec)["slug"]; got != "pizza-norte" {
			t.Fatalf("slug = %v", got)
		}
	})

	t.Run("el slug derivado quita los acentos", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		rec := h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Panadería Sol"}, &sa)
		want(t, rec, http.StatusCreated)
		if got := jsonMap(t, rec)["slug"]; got != "panaderia-sol" {
			t.Fatalf("slug = %v", got)
		}
	})

	t.Run("rechaza slugs inválidos, reservados o repetidos", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		h.org("Existente")
		for name, tc := range map[string]struct {
			slug string
			code int
		}{
			"con espacio":       {"mi tienda", http.StatusUnprocessableEntity},
			"con guion bajo":    {"mi_tienda", http.StatusUnprocessableEntity},
			"demasiado corto":   {"ab", http.StatusUnprocessableEntity},
			"demasiado largo":   {strings.Repeat("a", 64), http.StatusUnprocessableEntity},
			"guion al inicio":   {"-tienda", http.StatusUnprocessableEntity},
			"reservado":         {"platform", http.StatusUnprocessableEntity},
			"reservado (api)":   {"api", http.StatusUnprocessableEntity},
			"ya lo usa otra":    {"existente", http.StatusConflict},
			"acento en el slug": {"cafetería", http.StatusUnprocessableEntity},
		} {
			t.Run(name, func(t *testing.T) {
				rec := h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Nueva " + uuid.NewString()[:6], "slug": tc.slug}, &sa)
				want(t, rec, tc.code)
			})
		}
		if n := count(t, `SELECT count(*) FROM organizations`); n != 1 {
			t.Fatalf("organizaciones = %d: una alta rechazada dejó una organización", n)
		}
	})

	t.Run("el nombre es único sin distinguir mayúsculas", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		h.org("Mi Tienda")
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "MI TIENDA", "slug": "otra-tienda"}, &sa), http.StatusConflict)
	})

	t.Run("nace completa: ajustes, suscripción vigente y auditoría", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		rec := h.do(http.MethodPost, "/v1/organizations", map[string]any{
			"name": "Café Central", "plan_tier": "pro",
			"settings": map[string]any{"city": "Medellín", "timezone": "America/Bogota", "contact_phone": "300 123 4567"},
		}, &sa)
		want(t, rec, http.StatusCreated)
		body := jsonMap(t, rec)
		if body["plan_tier"] != "pro" || body["status"] != "active" || body["is_active"] != true {
			t.Fatalf("organización = %v", body)
		}
		id := body["id"].(string)

		if got := str(t, `SELECT city || '|' || contact_phone FROM organization_settings WHERE organization_id = $1`, id); got != "Medellín|3001234567" {
			t.Errorf("ajustes = %q", got)
		}
		if got := str(t, `SELECT plan_tier FROM organization_subscriptions WHERE organization_id = $1 AND ended_at IS NULL`, id); got != "pro" {
			t.Errorf("suscripción vigente = %q", got)
		}
		acts := strs(t, `SELECT action FROM audit_log WHERE organization_id = $1`, id)
		if len(acts) != 1 || acts[0] != "organization.created" {
			t.Errorf("auditoría = %v", acts)
		}
	})

	t.Run("rechaza un plan desconocido y ajustes inválidos, sin dejar nada", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		for name, body := range map[string]map[string]any{
			"plan":         {"name": "A", "plan_tier": "gold"},
			"zona horaria": {"name": "B", "settings": map[string]any{"timezone": "Marte/Olimpo"}},
			"correo admin": {"name": "C", "admin_email": "no-es-correo"},
		} {
			t.Run(name, func(t *testing.T) {
				want(t, h.do(http.MethodPost, "/v1/organizations", body, &sa), http.StatusUnprocessableEntity)
			})
		}
		if n := count(t, `SELECT count(*) FROM organizations`); n != 0 {
			t.Fatalf("organizaciones = %d", n)
		}
	})

	t.Run("con admin_email emite la invitación del primer administrador", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		rec := h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Con Dueno", "admin_email": "  Dueno@Ejemplo.com "}, &sa)
		want(t, rec, http.StatusCreated)
		inv, _ := jsonMap(t, rec)["admin_invitation"].(map[string]any)
		if inv == nil {
			t.Fatalf("falta admin_invitation: %v", jsonMap(t, rec))
		}
		if inv["email"] != "dueno@ejemplo.com" || inv["role"] != "admin" {
			t.Errorf("invitación = %v", inv)
		}
		if url, _ := inv["accept_url"].(string); !strings.HasPrefix(url, testAppURL+"/auth/accept-invite?token=") {
			t.Errorf("accept_url = %q", url)
		}
		if n := count(t, `SELECT count(*) FROM organization_invitations`); n != 1 {
			t.Errorf("invitaciones = %d", n)
		}
		id := jsonMap(t, rec)["id"].(string)
		acts := strs(t, `SELECT action FROM audit_log WHERE organization_id = $1 ORDER BY created_at, id`, id)
		if !contains(acts, "organization.created") || !contains(acts, "invitation.created") {
			t.Errorf("auditoría = %v", acts)
		}
	})

	t.Run("nombrar un administrador exige platform.roles.manage: sin él no se crea nada", func(t *testing.T) {
		h := newHarness(t)
		soloCrea := h.user()
		h.platformRole(soloCrea, "platform.organizations.create")

		// Crear sin administrador: puede.
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Sin Dueno"}, &soloCrea), http.StatusCreated)
		// Crear nombrando un administrador: no, y la organización no debe quedar creada.
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Con Dueno", "admin_email": "a@ejemplo.com"}, &soloCrea), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM organizations WHERE name = 'Con Dueno'`); n != 0 {
			t.Fatal("la alta rechazada dejó la organización creada")
		}
		if n := count(t, `SELECT count(*) FROM organization_invitations`); n != 0 {
			t.Fatal("la alta rechazada dejó una invitación")
		}
	})
}

func TestCicloDeVidaDeUnaOrganizacion(t *testing.T) {
	t.Run("suspender exige motivo, cierra a los miembros y queda auditado", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")

		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "   "}, &sa), http.StatusUnprocessableEntity)

		rec := h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "impago de septiembre"}, &sa)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["status"] != "suspended" || body["is_active"] != false || body["status_reason"] != "impago de septiembre" {
			t.Fatalf("organización = %v", body)
		}

		// Los miembros quedan fuera de todo, incluida la administración.
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodGet, orgURL(o, "/members"), nil, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodGet, orgURL(o, "/settings"), nil, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodPost, orgURL(o, "/invitations"), map[string]any{"email": "x@ejemplo.com", "role": "employee"}, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodPost, orgURL(o, "/members"), map[string]any{"user_id": h.user().ID.String(), "role": "employee"}, &admin), http.StatusForbidden)
		// El operador sí entra: debe poder inspeccionar lo que suspendió.
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &sa), http.StatusOK)

		if acts := auditActions(t, o); !contains(acts, "organization.suspended") {
			t.Errorf("auditoría = %v", acts)
		}
		if got := str(t, `SELECT detail->>'reason' FROM audit_log WHERE action = 'organization.suspended'`); got != "impago de septiembre" {
			t.Errorf("motivo auditado = %q", got)
		}
	})

	t.Run("reactivar devuelve el acceso", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		rec := h.do(http.MethodPost, platformOrgURL(o, "/reactivate"), map[string]any{}, &sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["status"] != "active" {
			t.Fatalf("organización = %v", jsonMap(t, rec))
		}
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &admin), http.StatusOK)
		if acts := auditActions(t, o); !contains(acts, "organization.reactivated") {
			t.Errorf("auditoría = %v", acts)
		}
	})

	t.Run("las transiciones inválidas dan 409 y no cambian nada", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.org("")
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/reactivate"), map[string]any{}, &sa), http.StatusConflict) // ya está activa
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/restore"), map[string]any{}, &sa), http.StatusConflict)    // no está archivada
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "a"}, &sa), http.StatusOK)
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "b"}, &sa), http.StatusConflict) // ya suspendida
		if got := str(t, `SELECT status_reason FROM organizations WHERE id = $1`, o.ID); got != "a" {
			t.Errorf("un intento rechazado cambió el motivo: %q", got)
		}
		if n := len(strs(t, `SELECT id::text FROM audit_log WHERE organization_id = $1`, o.ID)); n != 1 {
			t.Errorf("entradas de auditoría = %d: lo rechazado no debe auditarse", n)
		}
	})

	t.Run("una archivada solo sale del archivo restaurándola", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.orgStatus("", "archived")
		// Suspender o reactivar una archivada la sacaría del archivo sin que nadie lo decida.
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusConflict)
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/reactivate"), map[string]any{}, &sa), http.StatusConflict)
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/archive"), map[string]any{"reason": "x"}, &sa), http.StatusConflict)
		if got := str(t, `SELECT status FROM organizations WHERE id = $1`, o.ID); got != "archived" {
			t.Fatalf("estado = %q", got)
		}
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/restore"), map[string]any{}, &sa), http.StatusOK)
	})

	t.Run("archivar: para un miembro la organización deja de existir, para el operador no", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")

		want(t, h.do(http.MethodPost, platformOrgURL(o, "/archive"), map[string]any{}, &sa), http.StatusUnprocessableEntity) // sin motivo
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/archive"), map[string]any{"reason": "cerró el negocio"}, &sa), http.StatusOK)

		// Un miembro recibe 404, no 403: no se le confirma que existe.
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &admin), http.StatusNotFound)
		want(t, h.do(http.MethodGet, "/v1/organizations/by-slug/"+o.Slug, nil, &admin), http.StatusNotFound)
		want(t, h.do(http.MethodGet, orgURL(o, "/members"), nil, &admin), http.StatusNotFound)
		if got := orgNames(t, h, admin); len(got) != 0 {
			t.Errorf("un miembro sigue viendo la archivada en su listado: %v", got)
		}
		if got := orgNames(t, h, sa); len(got) != 0 {
			t.Errorf("el listado corriente del operador no debe incluir archivadas: %v", got)
		}

		// El operador la lee, con filtro de estado la lista, y no la modifica: primero se restaura.
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &sa), http.StatusOK)
		rec := h.do(http.MethodGet, "/v1/platform/organizations?status=archived", nil, &sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["total"] != float64(1) {
			t.Errorf("listado archivadas = %v", jsonMap(t, rec))
		}
		want(t, h.do(http.MethodPost, orgURL(o, "/invitations"), map[string]any{"email": "x@ejemplo.com", "role": "employee"}, &sa), http.StatusConflict)
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": "Otro"}, &sa), http.StatusConflict)
	})

	t.Run("restaurar deja la organización SUSPENDIDA, no activa", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/archive"), map[string]any{"reason": "x"}, &sa), http.StatusOK)

		rec := h.do(http.MethodPost, platformOrgURL(o, "/restore"), map[string]any{}, &sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["status"] != "suspended" {
			t.Fatalf("tras restaurar debía quedar suspendida: %v", jsonMap(t, rec))
		}
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &admin), http.StatusForbidden)

		want(t, h.do(http.MethodPost, platformOrgURL(o, "/reactivate"), map[string]any{}, &sa), http.StatusOK)
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &admin), http.StatusOK)
		if acts := auditActions(t, o); !contains(acts, "organization.restored") || !contains(acts, "organization.archived") {
			t.Errorf("auditoría = %v", acts)
		}
	})

	t.Run("archivar no libera el nombre ni el slug", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.org("Acme")
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/archive"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Acme"}, &sa), http.StatusConflict)
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]any{"name": "Otra", "slug": o.Slug}, &sa), http.StatusConflict)
	})

	t.Run("solo el operador cambia el estado", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		extrano := h.user()
		for _, action := range []string{"suspend", "reactivate", "archive", "restore"} {
			for _, who := range []person{admin, extrano} {
				want(t, h.do(http.MethodPost, platformOrgURL(o, "/"+action), map[string]any{"reason": "x"}, &who), http.StatusForbidden)
			}
		}
		if got := str(t, `SELECT status FROM organizations WHERE id = $1`, o.ID); got != "active" {
			t.Fatalf("estado = %q: alguien sin permiso lo cambió", got)
		}
	})

	t.Run("una organización inexistente da 404 al operador", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		url := "/v1/platform/organizations/" + uuid.NewString()
		want(t, h.do(http.MethodPost, url+"/suspend", map[string]any{"reason": "x"}, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodPost, "/v1/platform/organizations/no-es-uuid/suspend", map[string]any{"reason": "x"}, &sa), http.StatusUnprocessableEntity)
	})
}

func TestListadoDeLaPlataforma(t *testing.T) {
	seed := func(h *harness) {
		h.org("Pizza Norte")
		h.org("Pizza Sur")
		h.org("Panadería 100% Pan") // con un % literal en el nombre
		h.orgStatus("Café Cerrado", "suspended")
		arch := h.orgStatus("Archivada SA", "archived")
		h.setPlan(arch, "pro")
	}

	t.Run("busca por nombre o slug, sin distinguir mayúsculas", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		seed(h)
		rec := h.do(http.MethodGet, "/v1/platform/organizations?q=PIZZA", nil, &sa)
		want(t, rec, http.StatusOK)
		if body := jsonMap(t, rec); body["total"] != float64(2) {
			t.Fatalf("q=PIZZA: %v", body)
		}
		rec = h.do(http.MethodGet, "/v1/platform/organizations?q=pizza-sur", nil, &sa)
		if body := jsonMap(t, rec); body["total"] != float64(1) {
			t.Fatalf("q por slug: %v", body)
		}
	})

	t.Run("un comodín de LIKE se busca literal, no como comodín", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		seed(h)
		rec := h.do(http.MethodGet, "/v1/platform/organizations?q=%25", nil, &sa) // q="%"
		want(t, rec, http.StatusOK)
		if body := jsonMap(t, rec); body["total"] != float64(1) {
			t.Fatalf("q=%%: total = %v, quería solo la que tiene un %% en el nombre", body["total"])
		}
		rec = h.do(http.MethodGet, "/v1/platform/organizations?q=_", nil, &sa)
		if body := jsonMap(t, rec); body["total"] != float64(0) {
			t.Fatalf("q=_: total = %v, quería 0", body["total"])
		}
	})

	t.Run("filtra por estado y por plan", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		seed(h)
		for url, wantTotal := range map[string]float64{
			"/v1/platform/organizations":                             5,
			"/v1/platform/organizations?status=active":               3,
			"/v1/platform/organizations?status=suspended":            1,
			"/v1/platform/organizations?status=archived":             1,
			"/v1/platform/organizations?plan_tier=pro":               1,
			"/v1/platform/organizations?plan_tier=starter":           4,
			"/v1/platform/organizations?status=active&q=pizza":       2,
			"/v1/platform/organizations?status=archived&q=pizza":     0,
			"/v1/platform/organizations?plan_tier=enterprise":        0,
			"/v1/platform/organizations?q=no-existe":                 0,
			"/v1/platform/organizations?status=active&plan_tier=pro": 0,
		} {
			rec := h.do(http.MethodGet, url, nil, &sa)
			want(t, rec, http.StatusOK)
			if got := jsonMap(t, rec)["total"]; got != wantTotal {
				t.Errorf("%s: total = %v, quería %v", url, got, wantTotal)
			}
		}
	})

	t.Run("pagina y ordena por nombre", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		seed(h)
		rec := h.do(http.MethodGet, "/v1/platform/organizations?limit=2&offset=1", nil, &sa)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		items := body["items"].([]any)
		if len(items) != 2 || body["total"] != float64(5) || body["limit"] != float64(2) || body["offset"] != float64(1) {
			t.Fatalf("página = %v", body)
		}
		first := items[0].(map[string]any)["name"].(string)
		if first != "Café Cerrado" { // orden: Archivada SA, Café Cerrado, Panadería..., Pizza Norte, Pizza Sur
			t.Errorf("primer elemento de la página 2 = %q", first)
		}
		want(t, h.do(http.MethodGet, "/v1/platform/organizations?limit=0", nil, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodGet, "/v1/platform/organizations?limit=999", nil, &sa), http.StatusUnprocessableEntity)
	})

	t.Run("valida los filtros", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		want(t, h.do(http.MethodGet, "/v1/platform/organizations?status=borrada", nil, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodGet, "/v1/platform/organizations?plan_tier=gold", nil, &sa), http.StatusUnprocessableEntity)
	})

	t.Run("solo quien accede a todas las organizaciones", func(t *testing.T) {
		h := newHarness(t)
		admin, _ := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodGet, "/v1/platform/organizations", nil, &admin), http.StatusForbidden)
	})
}

func TestElPatchDelPanelSigueFuncionandoYEsIdempotente(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())
	o := h.org("")
	url := platformOrgURL(o, "")

	// Suspender dos veces por el PATCH viejo no es un error (el frontend actual lo hace así).
	want(t, h.do(http.MethodPatch, url, map[string]any{"is_active": false}, &sa), http.StatusOK)
	rec := h.do(http.MethodPatch, url, map[string]any{"is_active": false}, &sa)
	want(t, rec, http.StatusOK)
	if jsonMap(t, rec)["is_active"] != false {
		t.Fatalf("organización = %v", jsonMap(t, rec))
	}
	// Cambia plan y estado en una sola petición, y deja constancia en la auditoría.
	rec = h.do(http.MethodPatch, url, map[string]any{"is_active": true, "plan_tier": "outreach"}, &sa)
	want(t, rec, http.StatusOK)
	if body := jsonMap(t, rec); body["is_active"] != true || body["plan_tier"] != "outreach" {
		t.Fatalf("organización = %v", body)
	}
	acts := auditActions(t, o)
	for _, a := range []string{"organization.suspended", "organization.reactivated", "organization.plan_changed"} {
		if !contains(acts, a) {
			t.Errorf("falta %s en la auditoría %v", a, acts)
		}
	}
	// Un plan que ya tiene no es un error ni una entrada de auditoría.
	before := len(auditActions(t, o))
	want(t, h.do(http.MethodPatch, url, map[string]any{"plan_tier": "outreach"}, &sa), http.StatusOK)
	if after := len(auditActions(t, o)); after != before {
		t.Errorf("pedir el plan actual auditó %d entradas nuevas", after-before)
	}
}
