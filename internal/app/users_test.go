package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// POST /v1/users es, en la práctica, el signup de clientes: el staff entra por
// invitación y los domis por aprobación, ninguno pasa por aquí. Por eso crea
// también el perfil de cliente.

// tokenConCorreo firma el token que GoTrue emitiría para una cuenta recién creada.
func tokenConCorreo(t *testing.T, id uuid.UUID, email string) string {
	t.Helper()
	return signToken(t, testSecret, id.String(), func(c jwt.MapClaims) { c["email"] = email })
}

func TestProvisionarPerfil(t *testing.T) {
	t.Run("exige credenciales", func(t *testing.T) {
		h := newHarness(t)
		want(t, h.do(http.MethodPost, "/v1/users", map[string]any{"full_name": "Cliente Nuevo"}, nil), http.StatusUnauthorized)
	})

	t.Run("crea el usuario y el perfil de cliente", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()

		rec := h.doToken(http.MethodPost, "/v1/users", map[string]any{
			"full_name": "Cliente Nuevo", "phone": "3001112222", "default_address": "Calle Falsa 123",
		}, tokenConCorreo(t, id, "cliente@ejemplo.com"))
		want(t, rec, http.StatusCreated)

		body := jsonMap(t, rec)
		if body["email"] != "cliente@ejemplo.com" || body["id"] != id.String() || body["full_name"] != "Cliente Nuevo" {
			t.Fatalf("respuesta = %v", body)
		}
		if body["is_general_admin"] != false || body["is_delivery"] != false || body["is_active"] != true {
			t.Fatalf("un cliente nuevo no debe nacer con privilegios: %v", body)
		}
		if got := str(t, `SELECT phone FROM customers WHERE user_id = $1`, id); got != "3001112222" {
			t.Fatalf("phone = %q", got)
		}
		if got := str(t, `SELECT default_address FROM customers WHERE user_id = $1`, id); got != "Calle Falsa 123" {
			t.Fatalf("default_address = %q", got)
		}
	})

	// El id y el correo salen del TOKEN, nunca del cuerpo: si no, cualquiera
	// podría crearse un perfil con el id de otro (hallazgo crítico de la
	// auditoría de la Fase 5, cerrado en la Fase 6).
	t.Run("usa el id y el correo del token, nunca los del cuerpo", func(t *testing.T) {
		h := newHarness(t)
		id, ajeno := h.authIdentity(), h.authIdentity()

		rec := h.doToken(http.MethodPost, "/v1/users", map[string]any{
			"id": ajeno.String(), "email": "otro@ejemplo.com", "is_general_admin": true, "is_delivery": true,
		}, tokenConCorreo(t, id, "yo@ejemplo.com"))
		want(t, rec, http.StatusCreated)

		body := jsonMap(t, rec)
		if body["id"] != id.String() || body["email"] != "yo@ejemplo.com" {
			t.Fatalf("tomó datos del cuerpo: %v", body)
		}
		if body["is_general_admin"] != false || body["is_delivery"] != false {
			t.Fatalf("el cuerpo no debe poder darse privilegios: %v", body)
		}
		if n := count(t, `SELECT count(*) FROM users WHERE id = $1`, ajeno); n != 0 {
			t.Fatal("se creó un perfil con el id del cuerpo")
		}
	})

	t.Run("acepta un cuerpo vacío", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()
		want(t, h.doToken(http.MethodPost, "/v1/users", nil, tokenConCorreo(t, id, "vacio@ejemplo.com")), http.StatusCreated)
	})

	t.Run("no se puede provisionar dos veces", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()
		tok := tokenConCorreo(t, id, "cliente2@ejemplo.com")
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{}, tok), http.StatusCreated)
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{}, tok), http.StatusConflict)
	})

	t.Run("un token sin correo no puede crear perfil", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{}, signToken(t, testSecret, id.String(), nil)), http.StatusUnprocessableEntity)
	})

	t.Run("no permite un correo que ya tiene otro perfil", func(t *testing.T) {
		h := newHarness(t)
		existente := h.user(withEmail("ocupado@ejemplo.com"))
		nuevo := h.authIdentity()
		rec := h.doToken(http.MethodPost, "/v1/users", map[string]any{}, tokenConCorreo(t, nuevo, existente.Email))
		want(t, rec, http.StatusConflict)
	})

	t.Run("valida los largos", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()
		largo := strings.Repeat("a", 501)
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{"default_address": largo}, tokenConCorreo(t, id, "x@ejemplo.com")), http.StatusUnprocessableEntity)
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{"phone": strings.Repeat("9", 51)}, tokenConCorreo(t, id, "x@ejemplo.com")), http.StatusUnprocessableEntity)
	})
}

func TestPerfilPropio(t *testing.T) {
	t.Run("GET /me incluye el perfil de cliente", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()
		tok := tokenConCorreo(t, id, "cliente3@ejemplo.com")
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{"phone": "3009998888"}, tok), http.StatusCreated)

		rec := h.doToken(http.MethodGet, "/v1/users/me", nil, tok)
		want(t, rec, http.StatusOK)
		perfil, _ := jsonMap(t, rec)["customer_profile"].(map[string]any)
		if perfil["phone"] != "3009998888" || perfil["default_address"] != nil {
			t.Fatalf("customer_profile = %v", perfil)
		}
	})

	t.Run("un staff no tiene perfil de cliente (customer_profile es null)", func(t *testing.T) {
		h := newHarness(t)
		staff := h.user()
		rec := h.do(http.MethodGet, "/v1/users/me", nil, &staff)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if v, present := body["customer_profile"]; !present || v != nil {
			t.Fatalf("customer_profile = %v (presente=%v), quería null", v, present)
		}
	})

	t.Run("GET /me lista las organizaciones con nombre, slug y rol", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		a, b := h.org("Panadería Sol"), h.org("Zapatería Luna")
		h.join(u, a, "admin")
		h.join(u, b, "employee")
		h.join(h.user(), h.org("Ajena"), "admin")

		rec := h.do(http.MethodGet, "/v1/users/me", nil, &u)
		want(t, rec, http.StatusOK)
		ms, _ := jsonMap(t, rec)["memberships"].([]any)
		if len(ms) != 2 {
			t.Fatalf("membresías = %v, quería 2", ms)
		}
		first := ms[0].(map[string]any)
		if first["organization_name"] != "Panadería Sol" || first["organization_slug"] != a.Slug || first["role"] != "admin" || first["organization_id"] != a.ID.String() {
			t.Fatalf("primera membresía = %v", first)
		}
	})

	t.Run("GET /me de alguien sin organizaciones da una lista vacía, no null", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		rec := h.do(http.MethodGet, "/v1/users/me", nil, &u)
		want(t, rec, http.StatusOK)
		if ms, ok := jsonMap(t, rec)["memberships"].([]any); !ok || len(ms) != 0 {
			t.Fatalf("memberships = %v, quería []", jsonMap(t, rec)["memberships"])
		}
	})

	t.Run("PATCH /me actualiza el teléfono del cliente", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()
		tok := tokenConCorreo(t, id, "cliente4@ejemplo.com")
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{"phone": "3000000000", "default_address": "Cra 1"}, tok), http.StatusCreated)

		want(t, h.doToken(http.MethodPatch, "/v1/users/me", map[string]any{"phone": "3001231234"}, tok), http.StatusOK)

		perfil, _ := jsonMap(t, h.doToken(http.MethodGet, "/v1/users/me", nil, tok))["customer_profile"].(map[string]any)
		if perfil["phone"] != "3001231234" {
			t.Fatalf("phone = %v", perfil["phone"])
		}
		if perfil["default_address"] != "Cra 1" {
			t.Fatalf("un campo ausente no se toca, pero default_address = %v", perfil["default_address"])
		}
	})

	t.Run("PATCH /me no falla para un staff sin perfil de cliente", func(t *testing.T) {
		h := newHarness(t)
		staff := h.user()
		rec := h.do(http.MethodPatch, "/v1/users/me", map[string]any{"full_name": "Nombre Nuevo", "phone": "3001112222"}, &staff)
		want(t, rec, http.StatusOK)
		if got := jsonMap(t, rec)["full_name"]; got != "Nombre Nuevo" {
			t.Fatalf("full_name = %v", got)
		}
		if n := count(t, `SELECT count(*) FROM customers WHERE user_id = $1`, staff.ID); n != 0 {
			t.Fatal("PATCH /me no debe crear un perfil de cliente que no existía")
		}
	})

	// Un campo en null (o ausente) significa "no lo toques": no se puede vaciar
	// un dato mandándolo en null. Comportamiento heredado de domicilia-api.
	t.Run("PATCH /me con null o sin campos no borra nada", func(t *testing.T) {
		h := newHarness(t)
		id := h.authIdentity()
		tok := tokenConCorreo(t, id, "cliente5@ejemplo.com")
		want(t, h.doToken(http.MethodPost, "/v1/users", map[string]any{"full_name": "Ana", "phone": "3000000000"}, tok), http.StatusCreated)

		rec := h.doToken(http.MethodPatch, "/v1/users/me", `{"full_name": null, "phone": null}`, tok)
		want(t, rec, http.StatusOK)
		want(t, h.doToken(http.MethodPatch, "/v1/users/me", nil, tok), http.StatusOK)

		if got := str(t, `SELECT full_name FROM users WHERE id = $1`, id); got != "Ana" {
			t.Fatalf("full_name = %q, no debía cambiar", got)
		}
		if got := str(t, `SELECT phone FROM customers WHERE user_id = $1`, id); got != "3000000000" {
			t.Fatalf("phone = %q, no debía cambiar", got)
		}
	})

	t.Run("PATCH /me no permite darse privilegios", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		h.do(http.MethodPatch, "/v1/users/me", map[string]any{"is_general_admin": true, "is_delivery": true, "is_active": false}, &u)
		if hasRole(t, u.ID, "superadmin") || hasRole(t, u.ID, "delivery") {
			t.Fatal("PATCH /me permitió cambiar privilegios")
		}
		if !flag(t, `SELECT is_active FROM users WHERE id = $1`, u.ID) {
			t.Fatal("PATCH /me permitió desactivarse")
		}
	})

	t.Run("PATCH /me valida los largos", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodPatch, "/v1/users/me", map[string]any{"full_name": strings.Repeat("a", 256)}, &u), http.StatusUnprocessableEntity)
	})
}

func TestPromoverASuperadmin(t *testing.T) {
	t.Run("solo un superadmin promueve", func(t *testing.T) {
		h := newHarness(t)
		u, objetivo := h.user(), h.user()
		want(t, h.do(http.MethodPatch, "/v1/users/"+objetivo.ID.String()+"/promote", nil, &u), http.StatusForbidden)
		if hasRole(t, objetivo.ID, "superadmin") {
			t.Fatal("se promovió pese al 403")
		}
	})

	t.Run("un usuario no puede promoverse a sí mismo", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodPatch, "/v1/users/"+u.ID.String()+"/promote", nil, &u), http.StatusForbidden)
	})

	t.Run("el superadmin promueve", func(t *testing.T) {
		h := newHarness(t)
		sa, objetivo := h.user(superadmin()), h.user()
		rec := h.do(http.MethodPatch, "/v1/users/"+objetivo.ID.String()+"/promote", nil, &sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["is_general_admin"] != true {
			t.Fatalf("respuesta = %v", jsonMap(t, rec))
		}
		if !hasRole(t, objetivo.ID, "superadmin") {
			t.Fatal("no se guardó la promoción")
		}
	})

	t.Run("un usuario inexistente da 404 y un id mal formado 422", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		want(t, h.do(http.MethodPatch, "/v1/users/"+uuid.NewString()+"/promote", nil, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodPatch, "/v1/users/no-es-uuid/promote", nil, &sa), http.StatusUnprocessableEntity)
	})
}
