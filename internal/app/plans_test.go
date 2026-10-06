package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Planes: qué puede usar cada organización, y quién y cómo cambia el plan. El
// servidor es quien decide; el frontend solo oculta la interfaz.

func features(t *testing.T, h *harness, o organization, as person) []string {
	t.Helper()
	rec := h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &as)
	want(t, rec, http.StatusOK)
	var out []string
	for _, f := range jsonMap(t, rec)["features"].([]any) {
		out = append(out, f.(string))
	}
	return out
}

func TestCatalogoDePlanes(t *testing.T) {
	h := newHarness(t)
	want(t, h.do(http.MethodGet, "/v1/plans", nil, nil), http.StatusUnauthorized)

	u := h.user()
	rec := h.do(http.MethodGet, "/v1/plans", nil, &u)
	want(t, rec, http.StatusOK)
	body := jsonMap(t, rec)
	if n := len(body["plans"].([]any)); n != 4 {
		t.Fatalf("planes = %d, quería 4", n)
	}
	if n := len(body["features"].([]any)); n != len(plans.Features) {
		t.Fatalf("funciones = %d, quería %d", n, len(plans.Features))
	}
	first := body["plans"].([]any)[0].(map[string]any)
	if first["tier"] != "starter" || first["price_monthly_cop"] != float64(0) {
		t.Errorf("primer plan = %v", first)
	}
}

func TestFuncionesEfectivasDeUnaOrganizacion(t *testing.T) {
	t.Run("cada plan incluye lo suyo y lo de los anteriores", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		for tier, wantIn := range map[string]map[string]bool{
			"starter":    {"inbox_24h": true, "template_sync": false, "campaigns_bulk": false, "multi_inbox": false},
			"pro":        {"inbox_24h": true, "template_sync": true, "campaigns_bulk": false, "multi_inbox": false},
			"outreach":   {"inbox_24h": true, "template_sync": true, "campaigns_bulk": true, "multi_inbox": false},
			"enterprise": {"inbox_24h": true, "template_sync": true, "campaigns_bulk": true, "multi_inbox": true},
		} {
			h.setPlan(o, tier)
			got := features(t, h, o, admin)
			for f, on := range wantIn {
				if contains(got, f) != on {
					t.Errorf("plan %s: %s incluida = %v, quería %v", tier, f, !on, on)
				}
			}
		}
	})

	t.Run("cualquier miembro las ve, un extraño no", func(t *testing.T) {
		h := newHarness(t)
		_, o := orgAdmin(h, "Acme")
		empleado, extrano := h.user(), h.user()
		h.join(empleado, o, "employee")
		want(t, h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &empleado), http.StatusOK)
		want(t, h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &extrano), http.StatusForbidden)
	})

	t.Run("trae el uso frente a los límites del plan", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.setPlan(o, "pro")
		rec := h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &admin)
		want(t, rec, http.StatusOK)
		usage := jsonMap(t, rec)["usage"].(map[string]any)
		if usage["members"] != float64(1) || usage["max_members"] != float64(10) || usage["pending_invitations"] != float64(0) {
			t.Fatalf("uso = %v", usage)
		}
		h.setPlan(o, "enterprise")
		usage = jsonMap(t, h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &admin))["usage"].(map[string]any)
		if usage["max_members"] != float64(-1) {
			t.Errorf("enterprise no tiene tope: max_members = %v, quería -1", usage["max_members"])
		}
	})

	t.Run("una suspendida se cierra a sus miembros, una archivada no existe para ellos", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/archive"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, h.do(http.MethodGet, orgURL(o, "/entitlements"), nil, &admin), http.StatusNotFound)
	})

	t.Run("una organización inexistente da 403 a quien no es superadmin", func(t *testing.T) {
		h := newHarness(t)
		u, sa := h.user(), h.user(superadmin())
		want(t, h.do(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/entitlements", nil, &u), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/entitlements", nil, &sa), http.StatusNotFound)
	})
}

func TestCambiarElPlan(t *testing.T) {
	t.Run("abre una suscripción nueva, cierra la anterior y audita con el motivo", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.org("")
		rec := h.do(http.MethodPut, platformOrgURL(o, "/plan"), map[string]any{"plan_tier": "pro", "reason": "pagó el plan anual"}, &sa)
		want(t, rec, http.StatusOK)
		if body := jsonMap(t, rec); body["plan_tier"] != "pro" || body["ended_at"] != nil || body["reason"] != "pagó el plan anual" {
			t.Fatalf("suscripción = %v", body)
		}
		want(t, h.do(http.MethodPut, platformOrgURL(o, "/plan"), map[string]any{"plan_tier": "outreach"}, &sa), http.StatusOK)

		// Una sola vigente, igual al plan de la organización.
		if n := count(t, `SELECT count(*) FROM organization_subscriptions WHERE organization_id = $1 AND ended_at IS NULL`, o.ID); n != 1 {
			t.Fatalf("suscripciones vigentes = %d, quería 1", n)
		}
		if got := str(t, `SELECT o.plan_tier || ':' || s.plan_tier FROM organizations o
			JOIN organization_subscriptions s ON s.organization_id = o.id AND s.ended_at IS NULL WHERE o.id = $1`, o.ID); got != "outreach:outreach" {
			t.Errorf("plan y suscripción vigente = %q", got)
		}
		if got := strings.Join(strs(t, `SELECT plan_tier FROM organization_subscriptions WHERE organization_id = $1 ORDER BY started_at`, o.ID), ","); got != "starter,pro,outreach" {
			t.Errorf("historial = %q, quería starter,pro,outreach", got)
		}
		if got := str(t, `SELECT detail->>'from' || '>' || (detail->>'to') || ':' || coalesce(detail->>'reason', '')
			FROM audit_log WHERE organization_id = $1 AND action = 'organization.plan_changed' ORDER BY created_at, id LIMIT 1`, o.ID); got != "starter>pro:pagó el plan anual" {
			t.Errorf("auditoría = %q", got)
		}
	})

	t.Run("el historial lo lee quien tiene org.billing.read, con la vigente primero", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		empleado := h.user()
		h.join(empleado, o, "employee")
		want(t, h.do(http.MethodPut, platformOrgURL(o, "/plan"), map[string]any{"plan_tier": "pro"}, &sa), http.StatusOK)

		rec := h.do(http.MethodGet, orgURL(o, "/subscription"), nil, &admin)
		want(t, rec, http.StatusOK)
		hist := decode[[]map[string]any](t, rec)
		if len(hist) != 2 || hist[0]["plan_tier"] != "pro" || hist[0]["ended_at"] != nil {
			t.Fatalf("historial = %v", hist)
		}
		want(t, h.do(http.MethodGet, orgURL(o, "/subscription"), nil, &empleado), http.StatusForbidden)
	})

	t.Run("rechaza lo inválido y no cambia nada", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.org("")
		url := platformOrgURL(o, "/plan")
		want(t, h.do(http.MethodPut, url, map[string]any{"plan_tier": "gold"}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPut, url, map[string]any{}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPut, url, map[string]any{"plan_tier": "pro", "reason": string(make([]byte, 501))}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPut, url, map[string]any{"plan_tier": "starter"}, &sa), http.StatusConflict) // ya lo tiene
		want(t, h.do(http.MethodPut, "/v1/platform/organizations/"+uuid.NewString()+"/plan", map[string]any{"plan_tier": "pro"}, &sa), http.StatusNotFound)
		if got := str(t, `SELECT plan_tier FROM organizations WHERE id = $1`, o.ID); got != "starter" {
			t.Fatalf("plan = %q", got)
		}
		if n := len(auditActions(t, o)); n != 0 {
			t.Errorf("entradas de auditoría = %d: lo rechazado no debe auditarse", n)
		}
	})

	t.Run("una organización archivada no cambia de plan", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.orgStatus("", "archived")
		want(t, h.do(http.MethodPut, platformOrgURL(o, "/plan"), map[string]any{"plan_tier": "pro"}, &sa), http.StatusConflict)
	})

	t.Run("nadie cambia su propio plan", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.setPlan(o, "starter")
		want(t, h.do(http.MethodPut, platformOrgURL(o, "/plan"), map[string]any{"plan_tier": "enterprise"}, &admin), http.StatusForbidden)
		if got := str(t, `SELECT plan_tier FROM organizations WHERE id = $1`, o.ID); got != "starter" {
			t.Fatalf("plan = %q: un inquilino se lo cambió", got)
		}
	})

	t.Run("no se baja a un plan cuyo tope de miembros ya se excede", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		_, o := orgAdmin(h, "Acme") // enterprise
		for range 4 {
			h.join(h.user(), o, "employee") // 5 miembros en total
		}
		want(t, h.do(http.MethodPut, platformOrgURL(o, "/plan"), map[string]any{"plan_tier": "starter"}, &sa), http.StatusConflict)
		if got := str(t, `SELECT plan_tier FROM organizations WHERE id = $1`, o.ID); got != "enterprise" {
			t.Fatalf("plan = %q", got)
		}
		want(t, h.do(http.MethodPut, platformOrgURL(o, "/plan"), map[string]any{"plan_tier": "pro"}, &sa), http.StatusOK) // pro admite 10
	})
}

func TestExcepcionesPorFuncion(t *testing.T) {
	t.Run("enciende una función que el plan no incluye y la quita", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		h.setPlan(o, "starter")
		url := platformOrgURL(o, "/feature-overrides/campaigns_bulk")

		if contains(features(t, h, o, admin), "campaigns_bulk") {
			t.Fatal("starter no incluye campañas")
		}
		want(t, h.do(http.MethodPut, url, map[string]any{"enabled": true, "reason": "prueba de 30 días"}, &sa), http.StatusOK)
		if !contains(features(t, h, o, admin), "campaigns_bulk") {
			t.Fatal("la excepción debía encender la función")
		}
		want(t, h.do(http.MethodDelete, url, nil, &sa), http.StatusNoContent)
		if contains(features(t, h, o, admin), "campaigns_bulk") {
			t.Fatal("al quitar la excepción la función debía volver a depender del plan")
		}
		acts := auditActions(t, o)
		if !contains(acts, "organization.feature_override_set") || !contains(acts, "organization.feature_override_cleared") {
			t.Errorf("auditoría = %v", acts)
		}
	})

	t.Run("apaga una función que el plan sí incluye", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme") // enterprise
		want(t, h.do(http.MethodPut, platformOrgURL(o, "/feature-overrides/ai_copilot"), map[string]any{"enabled": false}, &sa), http.StatusOK)
		if contains(features(t, h, o, admin), "ai_copilot") {
			t.Fatal("la excepción debía apagar la función")
		}
	})

	t.Run("volver a fijarla reemplaza la anterior", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		o := h.org("")
		url := platformOrgURL(o, "/feature-overrides/csat")
		want(t, h.do(http.MethodPut, url, map[string]any{"enabled": true}, &sa), http.StatusOK)
		want(t, h.do(http.MethodPut, url, map[string]any{"enabled": false, "reason": "cambió de idea"}, &sa), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM organization_feature_overrides WHERE organization_id = $1`, o.ID); n != 1 {
			t.Fatalf("excepciones = %d, quería 1", n)
		}
	})

	t.Run("rechaza lo inválido", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		base := platformOrgURL(o, "/feature-overrides/")
		want(t, h.do(http.MethodPut, base+"no_existe", map[string]any{"enabled": true}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPut, base+"csat", map[string]any{}, &sa), http.StatusUnprocessableEntity) // falta enabled
		want(t, h.do(http.MethodDelete, base+"no_existe", nil, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodDelete, base+"csat", nil, &sa), http.StatusNotFound) // no había excepción
		want(t, h.do(http.MethodPut, "/v1/platform/organizations/"+uuid.NewString()+"/feature-overrides/csat", map[string]any{"enabled": true}, &sa), http.StatusNotFound)
		// Un administrador de la organización no se enciende funciones.
		want(t, h.do(http.MethodPut, base+"csat", map[string]any{"enabled": true}, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodDelete, base+"csat", nil, &admin), http.StatusForbidden)
	})
}

// Require es la puerta que cada endpoint premium debe llamar en su servicio.
func TestRequireBloqueaLasFuncionesQueElPlanNoIncluye(t *testing.T) {
	h := newHarness(t)
	svc := plans.NewService(plans.NewRepository(pool))
	sa := h.user(superadmin())
	o := h.org("")
	ctx := context.Background()

	kind := func(err error) string {
		switch {
		case err == nil:
			return "ok"
		case apperr.Is(err, apperr.KindPaymentRequired):
			return "402"
		case apperr.Is(err, apperr.KindForbidden):
			return "403"
		case apperr.Is(err, apperr.KindNotFound):
			return "404"
		}
		return err.Error()
	}

	if got := kind(svc.Require(ctx, o.ID, plans.Inbox24h)); got != "ok" {
		t.Errorf("starter usa el inbox: %s", got)
	}
	if got := kind(svc.Require(ctx, o.ID, plans.CampaignsBulk)); got != "402" {
		t.Errorf("starter no incluye campañas: %s (debía ser 402 Payment Required)", got)
	}
	// Subir el plan la habilita.
	h.setPlan(o, "outreach")
	if got := kind(svc.Require(ctx, o.ID, plans.CampaignsBulk)); got != "ok" {
		t.Errorf("outreach incluye campañas: %s", got)
	}
	// Una excepción manda sobre el plan, en los dos sentidos.
	want(t, h.do(http.MethodPut, platformOrgURL(o, "/feature-overrides/campaigns_bulk"), map[string]any{"enabled": false}, &sa), http.StatusOK)
	if got := kind(svc.Require(ctx, o.ID, plans.CampaignsBulk)); got != "402" {
		t.Errorf("con la excepción apagada: %s", got)
	}
	want(t, h.do(http.MethodPut, platformOrgURL(o, "/feature-overrides/multi_inbox"), map[string]any{"enabled": true}, &sa), http.StatusOK)
	if got := kind(svc.Require(ctx, o.ID, plans.MultiInbox)); got != "ok" {
		t.Errorf("con la excepción encendida: %s", got)
	}
	// Una organización que no está activa no usa ninguna función.
	want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
	if got := kind(svc.Require(ctx, o.ID, plans.Inbox24h)); got != "403" {
		t.Errorf("suspendida: %s, quería 403", got)
	}
	if got := kind(svc.Require(ctx, uuid.New(), plans.Inbox24h)); got != "404" {
		t.Errorf("inexistente: %s, quería 404", got)
	}
}

func TestElLimiteDeMiembrosDelPlan(t *testing.T) {
	membersURL := func(o organization) string { return orgURL(o, "/members") }
	addRole := func(h *harness, admin person, o organization) int {
		return h.do(http.MethodPost, membersURL(o), map[string]string{"user_id": h.user().ID.String(), "role": "employee"}, &admin).Code
	}

	t.Run("starter admite 3 miembros: el cuarto da 402", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.setPlan(o, "starter")
		if got := addRole(h, admin, o); got != http.StatusCreated {
			t.Fatalf("el 2.º miembro: %d", got)
		}
		if got := addRole(h, admin, o); got != http.StatusCreated {
			t.Fatalf("el 3.º miembro: %d", got)
		}
		if got := addRole(h, admin, o); got != http.StatusPaymentRequired {
			t.Fatalf("el 4.º miembro: %d, quería 402", got)
		}
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE organization_id = $1`, o.ID); n != 3 {
			t.Fatalf("miembros = %d, quería 3", n)
		}
		// Subir el plan lo habilita.
		h.setPlan(o, "pro")
		if got := addRole(h, admin, o); got != http.StatusCreated {
			t.Fatalf("con pro: %d", got)
		}
	})

	t.Run("las invitaciones pendientes ocupan lugar; reemplazar una no", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.setPlan(o, "starter")
		h.join(h.user(), o, "employee") // 2 miembros
		inv := func(email string) int {
			return h.do(http.MethodPost, orgURL(o, "/invitations"), map[string]string{"email": email, "role": "employee"}, &admin).Code
		}
		if got := inv("a@ejemplo.com"); got != http.StatusCreated { // 2 + 1 = 3
			t.Fatalf("1.ª invitación: %d", got)
		}
		if got := inv("a@ejemplo.com"); got != http.StatusCreated { // reemplaza: no ocupa lugar nuevo
			t.Fatalf("reinvitar al mismo correo: %d, no debía pasarse del tope", got)
		}
		if got := inv("b@ejemplo.com"); got != http.StatusPaymentRequired { // 2 + 1 + 1 = 4
			t.Fatalf("2.ª invitación: %d, quería 402", got)
		}
	})

	t.Run("la invitación con contraseña temporal (obsoleta) también respeta el tope", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.setPlan(o, "starter")
		h.join(h.user(), o, "employee")
		h.join(h.user(), o, "employee") // 3 miembros
		want(t, h.do(http.MethodPost, membersURL(o)+"/invite", map[string]string{"email": "n@ejemplo.com", "role": "employee"}, &admin), http.StatusPaymentRequired)
		if h.idp.createdCount() != 0 {
			t.Error("no debía crear la cuenta en GoTrue si el plan no admite más miembros")
		}
	})
}
