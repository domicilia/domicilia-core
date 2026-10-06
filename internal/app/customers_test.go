package app_test

import (
	"net/http"
	"strings"
	"testing"
)

// customers es un dominio propio: el perfil de cliente y su alta. Sus reglas ya
// están cubiertas en users_test.go (a través de /v1/users); aquí, lo que es propio.

// nuevoCliente da de alta a un cliente por la API y devuelve su token.
func nuevoCliente(t *testing.T, h *harness, email string, body map[string]any) (string, person) {
	t.Helper()
	id := h.authIdentity()
	tok := tokenConCorreo(t, id, email)
	want(t, h.doToken(http.MethodPost, "/v1/users", body, tok), http.StatusCreated)
	return tok, person{ID: id, Email: email}
}

func TestElAltaDeClienteLeDaElRolCustomer(t *testing.T) {
	h := newHarness(t)
	tok, c := nuevoCliente(t, h, "cliente@ejemplo.com", map[string]any{"phone": "3001112222"})

	if !hasRole(t, c.ID, "customer") {
		t.Fatal("el cliente no recibió el rol customer")
	}
	if hasRole(t, c.ID, "superadmin") || hasRole(t, c.ID, "delivery") {
		t.Fatal("un cliente no debe nacer con otros roles")
	}
	me := jsonMap(t, h.doToken(http.MethodGet, "/v1/users/me", nil, tok))
	roles, _ := me["roles"].([]any)
	if len(roles) != 1 || roles[0].(map[string]any)["code"] != "customer" {
		t.Fatalf("roles = %v", roles)
	}
	if perms, ok := me["permissions"].([]any); !ok || len(perms) != 0 {
		t.Fatalf("un cliente no tiene permisos de plataforma: %v", me["permissions"])
	}
	// La respuesta del alta también los trae.
	id2 := h.authIdentity()
	rec := h.doToken(http.MethodPost, "/v1/users", map[string]any{}, tokenConCorreo(t, id2, "otro@ejemplo.com"))
	want(t, rec, http.StatusCreated)
	if r, _ := jsonMap(t, rec)["roles"].([]any); len(r) != 1 {
		t.Fatalf("roles en la respuesta del alta = %v", r)
	}
}

func TestPerfilDeClienteEnSuPropioRecurso(t *testing.T) {
	t.Run("GET y PATCH /customers/me", func(t *testing.T) {
		h := newHarness(t)
		tok, _ := nuevoCliente(t, h, "cliente@ejemplo.com", map[string]any{"phone": "3000000000", "default_address": "Cra 1"})

		rec := h.doToken(http.MethodGet, "/v1/customers/me", nil, tok)
		want(t, rec, http.StatusOK)
		if p := jsonMap(t, rec); p["phone"] != "3000000000" || p["default_address"] != "Cra 1" {
			t.Fatalf("perfil = %v", p)
		}

		rec = h.doToken(http.MethodPatch, "/v1/customers/me", map[string]any{"default_address": "Calle 9"}, tok)
		want(t, rec, http.StatusOK)
		if p := jsonMap(t, rec); p["phone"] != "3000000000" || p["default_address"] != "Calle 9" {
			t.Fatalf("un campo ausente no se toca: %v", p)
		}
	})

	t.Run("quien no es cliente recibe 404", func(t *testing.T) {
		h := newHarness(t)
		staff := h.user()
		want(t, h.do(http.MethodGet, "/v1/customers/me", nil, &staff), http.StatusNotFound)
		want(t, h.do(http.MethodPatch, "/v1/customers/me", map[string]any{"phone": "3001"}, &staff), http.StatusNotFound)
		if n := count(t, `SELECT count(*) FROM customers WHERE user_id = $1`, staff.ID); n != 0 {
			t.Fatal("editar un perfil inexistente no debe crearlo")
		}
	})

	t.Run("exige credenciales y valida los largos", func(t *testing.T) {
		h := newHarness(t)
		tok, _ := nuevoCliente(t, h, "cliente@ejemplo.com", map[string]any{})
		want(t, h.do(http.MethodGet, "/v1/customers/me", nil, nil), http.StatusUnauthorized)
		want(t, h.doToken(http.MethodPatch, "/v1/customers/me", map[string]any{"phone": strings.Repeat("9", 51)}, tok), http.StatusUnprocessableEntity)
		want(t, h.doToken(http.MethodPatch, "/v1/customers/me", map[string]any{"default_address": strings.Repeat("a", 501)}, tok), http.StatusUnprocessableEntity)
	})
}

// PATCH /users/me escribe el nombre y el perfil de cliente en dos pasos: si el
// perfil es inválido, el nombre NO debe haberse cambiado.
func TestUnPerfilInvalidoNoDejaElNombreAMedias(t *testing.T) {
	h := newHarness(t)
	tok, c := nuevoCliente(t, h, "cliente@ejemplo.com", map[string]any{"full_name": "Ana"})

	rec := h.doToken(http.MethodPatch, "/v1/users/me", map[string]any{"full_name": "Ana María", "phone": strings.Repeat("9", 51)}, tok)
	want(t, rec, http.StatusUnprocessableEntity)
	if got := str(t, `SELECT full_name FROM users WHERE id = $1`, c.ID); got != "Ana" {
		t.Fatalf("full_name = %q: se cambió pese a que el perfil era inválido", got)
	}
}
