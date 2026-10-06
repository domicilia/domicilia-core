package app_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// POST /v1/organizations/{id}/members/invite: el único signup de staff que
// existe, y nunca es público. auth-domicilia no corre en las pruebas (lo simula
// fakeIDP): lo que importa aquí es la lógica del core — permisos, duplicados y
// qué pasa cuando algo falla a la mitad.

func invitar(h *harness, o organization, as *person, body map[string]any) int {
	return h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members/invite", body, as).Code
}

func TestInvitarMiembro(t *testing.T) {
	t.Run("exige credenciales", func(t *testing.T) {
		h := newHarness(t)
		got := invitar(h, h.org(""), nil, map[string]any{"email": "nuevo@ejemplo.com", "role": "employee"})
		if got != http.StatusUnauthorized {
			t.Fatalf("código = %d, quería 401", got)
		}
	})

	t.Run("un extraño a la organización recibe 403 y no se crea ninguna cuenta", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		got := invitar(h, h.org(""), &u, map[string]any{"email": "nuevo@ejemplo.com", "role": "employee"})
		if got != http.StatusForbidden {
			t.Fatalf("código = %d, quería 403", got)
		}
		if n := h.idp.createdCount(); n != 0 {
			t.Fatalf("se crearon %d cuentas en GoTrue pese al 403", n)
		}
	})

	t.Run("un admin de organización invita a un empleado", func(t *testing.T) {
		h := newHarness(t)
		admin, o := h.user(), h.org("")
		h.join(admin, o, "admin")

		rec := h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members/invite",
			map[string]any{"email": "nuevo@ejemplo.com", "full_name": "Nuevo", "role": "employee"}, &admin)
		want(t, rec, http.StatusCreated)

		body := jsonMap(t, rec)
		if body["role"] != "employee" || body["organization_id"] != o.ID.String() {
			t.Fatalf("respuesta = %v", body)
		}
		if pw, _ := body["temporary_password"].(string); len(pw) < 12 {
			t.Fatalf("temporary_password = %q, quería una contraseña de verdad", pw)
		}

		// El perfil de negocio y la membresía existen, con el id de la cuenta de GoTrue.
		id := body["user_id"].(string)
		if got := str(t, `SELECT email FROM users WHERE id = $1`, id); got != "nuevo@ejemplo.com" {
			t.Fatalf("email guardado = %q", got)
		}
		if got := str(t, `SELECT full_name FROM users WHERE id = $1`, id); got != "Nuevo" {
			t.Fatalf("full_name guardado = %q", got)
		}
		if n := count(t, `SELECT count(*) FROM user_organizations uo JOIN roles r ON r.id = uo.role_id WHERE uo.user_id = $1 AND uo.organization_id = $2 AND r.code = 'employee'`, id, o.ID); n != 1 {
			t.Fatal("no se creó la membresía")
		}
		if uid := uuid.MustParse(id); hasRole(t, uid, "superadmin") || hasRole(t, uid, "delivery") {
			t.Fatal("un invitado no debe nacer con privilegios de plataforma ni de reparto")
		}
	})

	// Misma regla que AddMember: solo el superadmin asigna el rol admin.
	t.Run("un admin de organización no puede invitar con rol admin", func(t *testing.T) {
		h := newHarness(t)
		adminOrg, o := h.user(), h.org("")
		h.join(adminOrg, o, "admin")

		got := invitar(h, o, &adminOrg, map[string]any{"email": "nuevo@ejemplo.com", "role": "admin"})
		if got != http.StatusForbidden {
			t.Fatalf("código = %d, quería 403", got)
		}
		if n := h.idp.createdCount(); n != 0 {
			t.Fatalf("se crearon %d cuentas en GoTrue pese al 403", n)
		}
	})

	t.Run("el superadmin sí puede invitar con rol admin", func(t *testing.T) {
		h := newHarness(t)
		sa, o := h.user(superadmin()), h.org("")
		rec := h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members/invite",
			map[string]any{"email": "nuevo-admin@ejemplo.com", "role": "admin"}, &sa)
		want(t, rec, http.StatusCreated)
		if got := jsonMap(t, rec)["role"]; got != "admin" {
			t.Fatalf("role = %v", got)
		}
	})

	t.Run("rechaza un correo ya registrado sin tocar GoTrue", func(t *testing.T) {
		h := newHarness(t)
		admin, o := h.user(), h.org("")
		h.join(admin, o, "admin")
		existente := h.user(withEmail("repetido@ejemplo.com"))

		got := invitar(h, o, &admin, map[string]any{"email": existente.Email, "role": "employee"})
		if got != http.StatusConflict {
			t.Fatalf("código = %d, quería 409", got)
		}
		if n := h.idp.createdCount(); n != 0 {
			t.Fatalf("se crearon %d cuentas en GoTrue pese al 409", n)
		}
	})

	t.Run("el correo se normaliza a minúsculas, como hace GoTrue", func(t *testing.T) {
		// Sin normalizar, "Ana@X.com" y "ana@x.com" serían dos personas para la
		// base y una sola para GoTrue.
		h := newHarness(t)
		sa, o := h.user(superadmin()), h.org("")
		h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members/invite",
			map[string]any{"email": "  Ana.Perez@Ejemplo.COM ", "role": "employee"}, &sa)

		if n := count(t, `SELECT count(*) FROM users WHERE email = 'ana.perez@ejemplo.com'`); n != 1 {
			t.Fatal("el correo no quedó en minúsculas")
		}
		// Y por lo mismo, no se puede invitar dos veces al "mismo" correo.
		got := invitar(h, o, &sa, map[string]any{"email": "ANA.PEREZ@ejemplo.com", "role": "employee"})
		if got != http.StatusConflict {
			t.Fatalf("segunda invitación = %d, quería 409", got)
		}
	})

	t.Run("valida la entrada antes de tocar GoTrue", func(t *testing.T) {
		h := newHarness(t)
		sa, o := h.user(superadmin()), h.org("")
		tests := []struct {
			name string
			body map[string]any
		}{
			{"sin correo", map[string]any{"role": "employee"}},
			{"correo mal formado", map[string]any{"email": "no-es-un-correo", "role": "employee"}},
			{"correo con nombre y corchetes", map[string]any{"email": "Ana <ana@ejemplo.com>", "role": "employee"}},
			{"dominio reservado (.test)", map[string]any{"email": "ana@ejemplo.test", "role": "employee"}},
			{"sin rol", map[string]any{"email": "ana@ejemplo.com"}},
			{"rol desconocido", map[string]any{"email": "ana@ejemplo.com", "role": "dueño"}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := invitar(h, o, &sa, tc.body); got != http.StatusUnprocessableEntity {
					t.Fatalf("código = %d, quería 422", got)
				}
			})
		}
		if n := h.idp.createdCount(); n != 0 {
			t.Fatalf("se crearon %d cuentas en GoTrue con entrada inválida", n)
		}
	})

	t.Run("una organización inexistente da 404 al superadmin", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		rec := h.do(http.MethodPost, "/v1/organizations/"+uuid.NewString()+"/members/invite",
			map[string]any{"email": "nuevo@ejemplo.com", "role": "employee"}, &sa)
		want(t, rec, http.StatusNotFound)
	})

	t.Run("si GoTrue falla responde 502 sin exponer su detalle y no deja nada a medias", func(t *testing.T) {
		h := newHarness(t)
		sa, o := h.user(superadmin()), h.org("")
		h.idp.createErr = errors.New(`auth-domicilia: 500 {"msg":"password=hunter2 conexión a 10.0.0.5"}`)

		rec := h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members/invite",
			map[string]any{"email": "nuevo@ejemplo.com", "role": "employee"}, &sa)
		want(t, rec, http.StatusBadGateway)
		if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Fatalf("el detalle interno de GoTrue llegó al cliente: %s", rec.Body.String())
		}
		if n := count(t, `SELECT count(*) FROM users WHERE email = 'nuevo@ejemplo.com'`); n != 0 {
			t.Fatal("se creó un perfil pese al fallo de GoTrue")
		}
	})

	// Si guardar el perfil falla DESPUÉS de crear la cuenta, la cuenta debe
	// borrarse: si no, el correo queda ocupado en GoTrue y no se puede reintentar.
	t.Run("si falla guardar el perfil, la cuenta recién creada se deshace", func(t *testing.T) {
		h := newHarness(t)
		sa, o := h.user(superadmin()), h.org("")
		yaTienePerfil := h.user()
		// GoTrue "devuelve" un id que ya tiene perfil: guardar choca.
		h.idp.forceID = &yaTienePerfil.ID

		rec := h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members/invite",
			map[string]any{"email": "nuevo@ejemplo.com", "role": "employee"}, &sa)
		want(t, rec, http.StatusConflict)

		if got := h.idp.deletedIDs(); len(got) != 1 || got[0] != yaTienePerfil.ID {
			t.Fatalf("cuentas deshechas = %v, quería exactamente la recién creada", got)
		}
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE organization_id = $1`, o.ID); n != 0 {
			t.Fatal("quedó una membresía a medias")
		}
	})
}
