package app_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// /v1/driver-applications: nunca hay signup directo para domis. Aplicar es
// público pero no crea ninguna cuenta, solo una solicitud pendiente.

var solicitud = map[string]any{
	"full_name":    "Juan Pérez",
	"email":        "juan.perez@ejemplo.com",
	"phone":        "3001234567",
	"vehicle_type": "moto",
}

// postular envía la solicitud pública y devuelve su id.
func postular(t *testing.T, h *harness) string {
	t.Helper()
	rec := h.do(http.MethodPost, "/v1/driver-applications", solicitud, nil)
	want(t, rec, http.StatusCreated)
	return jsonMap(t, rec)["id"].(string)
}

func TestSolicitarSerDomi(t *testing.T) {
	t.Run("no exige credenciales y queda pendiente", func(t *testing.T) {
		h := newHarness(t)
		rec := h.do(http.MethodPost, "/v1/driver-applications", solicitud, nil)
		want(t, rec, http.StatusCreated)
		if got := jsonMap(t, rec)["status"]; got != "pending" {
			t.Fatalf("status = %v", got)
		}
	})

	t.Run("no crea ninguna cuenta: aplicar no es registrarse", func(t *testing.T) {
		h := newHarness(t)
		postular(t, h)
		if n := count(t, `SELECT count(*) FROM users`); n != 0 {
			t.Fatalf("se crearon %d usuarios al solo postularse", n)
		}
		if n := h.idp.createdCount(); n != 0 {
			t.Fatalf("se crearon %d cuentas en GoTrue al solo postularse", n)
		}
	})

	t.Run("guarda el correo en minúsculas", func(t *testing.T) {
		h := newHarness(t)
		body := map[string]any{"full_name": "Ana", "email": "ANA@Ejemplo.com", "phone": "3001234567", "vehicle_type": "bici"}
		want(t, h.do(http.MethodPost, "/v1/driver-applications", body, nil), http.StatusCreated)
		if n := count(t, `SELECT count(*) FROM driver_applications WHERE email = 'ana@ejemplo.com'`); n != 1 {
			t.Fatal("el correo no quedó en minúsculas")
		}
	})

	t.Run("valida la entrada", func(t *testing.T) {
		h := newHarness(t)
		mod := func(k string, v any) map[string]any {
			m := map[string]any{}
			for kk, vv := range solicitud {
				m[kk] = vv
			}
			m[k] = v
			return m
		}
		tests := []struct {
			name string
			body map[string]any
		}{
			{"sin nombre", mod("full_name", "")},
			{"correo mal formado", mod("email", "juan")},
			{"teléfono demasiado corto", mod("phone", "1234")},
			{"tipo de vehículo vacío", mod("vehicle_type", "  ")},
			{"cuerpo vacío", map[string]any{}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				want(t, h.do(http.MethodPost, "/v1/driver-applications", tc.body, nil), http.StatusUnprocessableEntity)
			})
		}
		if n := count(t, `SELECT count(*) FROM driver_applications`); n != 0 {
			t.Fatalf("se guardaron %d solicitudes inválidas", n)
		}
	})

	// Solo el POST es público; ver o revisar las solicitudes no.
	t.Run("el listado NO es público aunque el alta sí", func(t *testing.T) {
		h := newHarness(t)
		want(t, h.do(http.MethodGet, "/v1/driver-applications", nil, nil), http.StatusUnauthorized)
	})
}

func TestListarSolicitudes(t *testing.T) {
	t.Run("exige ser superadmin", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		postular(t, h)
		want(t, h.do(http.MethodGet, "/v1/driver-applications", nil, &u), http.StatusForbidden)
	})

	t.Run("el superadmin ve las solicitudes y puede filtrar por estado", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)
		postular(t, h)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/reject", nil, &sa), http.StatusOK)

		all := h.do(http.MethodGet, "/v1/driver-applications", nil, &sa)
		want(t, all, http.StatusOK)
		if n := len(decode[[]map[string]any](t, all)); n != 2 {
			t.Fatalf("solicitudes = %d, quería 2", n)
		}

		pend := h.do(http.MethodGet, "/v1/driver-applications?status_filter=pending", nil, &sa)
		want(t, pend, http.StatusOK)
		if n := len(decode[[]map[string]any](t, pend)); n != 1 {
			t.Fatalf("pendientes = %d, quería 1", n)
		}
		want(t, h.do(http.MethodGet, "/v1/driver-applications?status_filter=inventado", nil, &sa), http.StatusUnprocessableEntity)
	})
}

func TestAprobarSolicitud(t *testing.T) {
	t.Run("exige ser superadmin", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		id := postular(t, h)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &u), http.StatusForbidden)
	})

	t.Run("aprobar crea el usuario con el rol delivery y una contraseña temporal", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)

		rec := h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["status"] != "approved" {
			t.Fatalf("status = %v", body["status"])
		}
		if pw, _ := body["temporary_password"].(string); len(pw) < 12 {
			t.Fatalf("temporary_password = %q", pw)
		}

		if !hasRoleEmail(t, solicitud["email"].(string), "delivery") {
			t.Fatal("el usuario creado no es domiciliario")
		}
		if hasRoleEmail(t, solicitud["email"].(string), "superadmin") {
			t.Fatal("un domiciliario no debe nacer superadmin")
		}
		if got := str(t, `SELECT full_name FROM users WHERE email = $1`, solicitud["email"]); got != "Juan Pérez" {
			t.Fatalf("full_name = %q", got)
		}
		if got := str(t, `SELECT status::text FROM driver_applications WHERE id = $1`, id); got != "approved" {
			t.Fatalf("estado en base = %q", got)
		}
	})

	t.Run("no se puede aprobar dos veces", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusConflict)
		if n := h.idp.createdCount(); n != 1 {
			t.Fatalf("cuentas creadas en GoTrue = %d, quería 1", n)
		}
	})

	t.Run("rechaza un correo ya registrado sin tocar GoTrue", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		h.user(withEmail(solicitud["email"].(string)))
		id := postular(t, h)

		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusConflict)
		if n := h.idp.createdCount(); n != 0 {
			t.Fatalf("se crearon %d cuentas en GoTrue pese al 409", n)
		}
	})

	t.Run("una solicitud inexistente da 404 y un id mal formado 422", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+uuid.NewString()+"/approve", nil, &sa), http.StatusNotFound)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/no-es-uuid/approve", nil, &sa), http.StatusUnprocessableEntity)
	})

	t.Run("si GoTrue falla responde 502 y la solicitud sigue pendiente", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)
		h.idp.createErr = errors.New("auth-domicilia caído")

		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusBadGateway)
		if got := str(t, `SELECT status::text FROM driver_applications WHERE id = $1`, id); got != "pending" {
			t.Fatalf("estado = %q: debía seguir pendiente para poder reintentar", got)
		}
	})

	// Si guardar el perfil falla DESPUÉS de crear la cuenta, la cuenta debe
	// borrarse: si no, el correo queda ocupado en GoTrue y no se puede reintentar.
	t.Run("si falla guardar el perfil, la cuenta recién creada se deshace y la solicitud sigue pendiente", func(t *testing.T) {
		h := newHarness(t)
		sa, yaTienePerfil := h.user(superadmin()), h.user()
		id := postular(t, h)
		// GoTrue "devuelve" un id que ya tiene perfil: guardar choca.
		h.idp.forceID = &yaTienePerfil.ID

		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusConflict)

		if got := h.idp.deletedIDs(); len(got) != 1 || got[0] != yaTienePerfil.ID {
			t.Fatalf("cuentas deshechas = %v, quería exactamente la recién creada", got)
		}
		if got := str(t, `SELECT status::text FROM driver_applications WHERE id = $1`, id); got != "pending" {
			t.Fatalf("estado = %q: la transacción debió revertirse por completo", got)
		}
	})

	// Dos superadmins aprueban la misma solicitud a la vez. Exactamente uno debe
	// ganar, y la cuenta que el perdedor alcanzó a crear en GoTrue debe deshacerse.
	// (Depende del orden en que se crucen las peticiones: las invariantes finales
	// se comprueban siempre; el camino de deshacer lo cubre la prueba anterior.)
	t.Run("dos aprobaciones simultáneas: gana una y la otra no deja cuentas huérfanas", func(t *testing.T) {
		h := newHarness(t)
		sa1, sa2 := h.user(superadmin()), h.user(superadmin())
		id := postular(t, h)

		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i, sa := range []person{sa1, sa2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa).Code
			}()
		}
		wg.Wait()

		ok, conflict := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatalf("códigos = %v, quería un 200 y un 409", codes)
		}
		if n := count(t, `SELECT count(*) FROM users WHERE email = $1`, solicitud["email"]); n != 1 {
			t.Fatalf("perfiles del domi = %d, quería 1", n)
		}
		// auth.users tiene una fila por cada perfil (2 superadmins + 1 domi): ninguna huérfana.
		if a, u := count(t, `SELECT count(*) FROM auth.users`), count(t, `SELECT count(*) FROM users`); a != u {
			t.Fatalf("auth.users = %d y users = %d: quedó una cuenta huérfana en GoTrue", a, u)
		}
	})
}

func TestRechazarSolicitud(t *testing.T) {
	t.Run("rechazar marca la solicitud y no crea cuentas", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)

		rec := h.do(http.MethodPost, "/v1/driver-applications/"+id+"/reject", nil, &sa)
		want(t, rec, http.StatusOK)
		if got := jsonMap(t, rec)["status"]; got != "rejected" {
			t.Fatalf("status = %v", got)
		}
		if n := h.idp.createdCount(); n != 0 {
			t.Fatalf("se crearon %d cuentas al rechazar", n)
		}
	})

	t.Run("exige ser superadmin", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		id := postular(t, h)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/reject", nil, &u), http.StatusForbidden)
	})

	t.Run("no se puede rechazar dos veces ni aprobar una rechazada", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/reject", nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/reject", nil, &sa), http.StatusConflict)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusConflict)
	})

	t.Run("no se puede rechazar una ya aprobada", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		id := postular(t, h)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/approve", nil, &sa), http.StatusOK)
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+id+"/reject", nil, &sa), http.StatusConflict)
	})

	t.Run("una solicitud inexistente da 404", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		want(t, h.do(http.MethodPost, "/v1/driver-applications/"+uuid.NewString()+"/reject", nil, &sa), http.StatusNotFound)
	})
}
