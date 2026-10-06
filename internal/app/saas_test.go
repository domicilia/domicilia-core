package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// /v1/platform: los controles del operador sobre los inquilinos y los usuarios.
// Todo es solo para el superadmin (la frontera con los inquilinos se prueba en
// TestSoloElSuperadminEntraAlPanelDePlataforma).

func TestPanoramaDePlataforma(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())
	h.user()
	h.user(delivery())
	h.org("Una")
	h.org("Otra")

	rec := h.do(http.MethodGet, "/v1/platform/overview", nil, &sa)
	want(t, rec, http.StatusOK)

	body := jsonMap(t, rec)
	if body["total_users_count"] != float64(3) || body["delivery_users_count"] != float64(1) {
		t.Fatalf("contadores = %v / %v, quería 3 usuarios y 1 domiciliario", body["total_users_count"], body["delivery_users_count"])
	}
	if orgs, _ := body["organizations"].([]any); len(orgs) != 2 {
		t.Fatalf("organizaciones = %v, quería 2", body["organizations"])
	}
}

func TestPanoramaSinOrganizacionesDaListaVacia(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())
	rec := h.do(http.MethodGet, "/v1/platform/overview", nil, &sa)
	want(t, rec, http.StatusOK)
	if orgs, ok := jsonMap(t, rec)["organizations"].([]any); !ok || len(orgs) != 0 {
		t.Fatalf("organizations = %v, quería []", jsonMap(t, rec)["organizations"])
	}
}

func TestSuspenderYCambiarPlan(t *testing.T) {
	t.Run("suspender cierra el acceso de los miembros; reactivar lo abre", func(t *testing.T) {
		h := newHarness(t)
		sa, miembro, o := h.user(superadmin()), h.user(), h.org("Cliente")
		h.join(miembro, o, "admin")
		url := "/v1/organizations/" + o.ID.String()

		want(t, h.do(http.MethodGet, url, nil, &miembro), http.StatusOK)

		rec := h.do(http.MethodPatch, "/v1/platform/organizations/"+o.ID.String(), map[string]any{"is_active": false}, &sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["is_active"] != false {
			t.Fatalf("respuesta = %v", jsonMap(t, rec))
		}
		want(t, h.do(http.MethodGet, url, nil, &miembro), http.StatusForbidden)

		want(t, h.do(http.MethodPatch, "/v1/platform/organizations/"+o.ID.String(), map[string]any{"is_active": true}, &sa), http.StatusOK)
		want(t, h.do(http.MethodGet, url, nil, &miembro), http.StatusOK)
	})

	t.Run("cambia el plan y un cuerpo sin campos no cambia nada", func(t *testing.T) {
		h := newHarness(t)
		sa, o := h.user(superadmin()), h.org("")
		url := "/v1/platform/organizations/" + o.ID.String()

		rec := h.do(http.MethodPatch, url, map[string]any{"plan_tier": "pro"}, &sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["plan_tier"] != "pro" {
			t.Fatalf("respuesta = %v", jsonMap(t, rec))
		}

		rec = h.do(http.MethodPatch, url, map[string]any{}, &sa)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["plan_tier"] != "pro" || body["is_active"] != true {
			t.Fatalf("un PATCH vacío cambió algo: %v", body)
		}
	})

	t.Run("un admin de la organización no puede suspenderse ni cambiarse el plan", func(t *testing.T) {
		h := newHarness(t)
		admin, o := h.user(), h.org("")
		h.join(admin, o, "admin")
		want(t, h.do(http.MethodPatch, "/v1/platform/organizations/"+o.ID.String(), map[string]any{"plan_tier": "enterprise"}, &admin), http.StatusForbidden)
		if got := str(t, `SELECT plan_tier FROM organizations WHERE id = $1`, o.ID); got != "starter" {
			t.Fatalf("plan_tier = %q: un inquilino se cambió el plan", got)
		}
	})

	t.Run("valida el plan y la existencia", func(t *testing.T) {
		h := newHarness(t)
		sa, o := h.user(superadmin()), h.org("")
		want(t, h.do(http.MethodPatch, "/v1/platform/organizations/"+o.ID.String(), map[string]any{"plan_tier": ""}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, "/v1/platform/organizations/"+o.ID.String(), map[string]any{"plan_tier": strings.Repeat("p", 51)}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, "/v1/platform/organizations/"+uuid.NewString(), map[string]any{"is_active": false}, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodPatch, "/v1/platform/organizations/no-es-uuid", map[string]any{}, &sa), http.StatusUnprocessableEntity)
	})
}

func TestMarcarDomiciliario(t *testing.T) {
	t.Run("marca y desmarca", func(t *testing.T) {
		h := newHarness(t)
		sa, u := h.user(superadmin()), h.user()
		url := "/v1/platform/users/" + u.ID.String() + "/delivery"

		rec := h.do(http.MethodPatch, url, map[string]any{"is_delivery": true}, &sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["is_delivery"] != true || !hasRole(t, u.ID, "delivery") {
			t.Fatal("no quedó marcado como domiciliario")
		}
		want(t, h.do(http.MethodPatch, url, map[string]any{"is_delivery": false}, &sa), http.StatusOK)
		if hasRole(t, u.ID, "delivery") {
			t.Fatal("no se desmarcó")
		}
	})

	t.Run("solo el superadmin", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodPatch, "/v1/platform/users/"+u.ID.String()+"/delivery", map[string]any{"is_delivery": true}, &u), http.StatusForbidden)
		if hasRole(t, u.ID, "delivery") {
			t.Fatal("un usuario se marcó a sí mismo como domiciliario")
		}
	})

	t.Run("is_delivery es obligatorio y el usuario debe existir", func(t *testing.T) {
		h := newHarness(t)
		sa, u := h.user(superadmin()), h.user()
		want(t, h.do(http.MethodPatch, "/v1/platform/users/"+u.ID.String()+"/delivery", map[string]any{}, &sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, "/v1/platform/users/"+uuid.NewString()+"/delivery", map[string]any{"is_delivery": true}, &sa), http.StatusNotFound)
	})
}
