package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/users"
)

// El alta del primer superadmin (`api create-superadmin`) es el bootstrap de la
// plataforma: sin él nadie puede crear organizaciones. Se prueba contra la base
// real y el GoTrue simulado.

func superadminService(h *harness) *users.SuperadminService {
	return users.NewSuperadminService(users.NewSuperadminRepository(pool), h.idp)
}

const pwSuperadmin = "una-contrasena-larga-1"

func TestCrearSuperadminNuevo(t *testing.T) {
	h := newHarness(t)
	name := "Ana Pérez"

	u, err := superadminService(h).Ensure(context.Background(), "Ana@Ejemplo.com", pwSuperadmin, &name)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if u.Email != "ana@ejemplo.com" || !u.IsGeneralAdmin || !u.IsActive {
		t.Fatalf("usuario = %+v", u)
	}
	if n := h.idp.createdCount(); n != 1 {
		t.Fatalf("cuentas creadas en GoTrue = %d, quería 1", n)
	}
	// El superadmin creado puede, de verdad, entrar al panel de plataforma.
	p := person{ID: u.ID, Email: u.Email}
	want(t, h.do(http.MethodGet, "/v1/platform/overview", nil, &p), http.StatusOK)
}

func TestSuperadminEsIdempotenteYNoCambiaLaContrasena(t *testing.T) {
	h := newHarness(t)
	svc := superadminService(h)
	if _, err := svc.Ensure(context.Background(), "ana@ejemplo.com", pwSuperadmin, nil); err != nil {
		t.Fatal(err)
	}

	// Segunda vez: ya existe, no hace falta contraseña y no se crea otra cuenta.
	u, err := svc.Ensure(context.Background(), "ana@ejemplo.com", "", nil)
	if err != nil || !u.IsGeneralAdmin {
		t.Fatalf("Ensure repetido = %+v, %v", u, err)
	}
	if n := h.idp.createdCount(); n != 1 {
		t.Fatalf("cuentas creadas en GoTrue = %d: repetir no debe crear otra", n)
	}
	if n := count(t, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("usuarios = %d, quería 1", n)
	}
}

func TestPromoverAUnUsuarioExistenteLoReactiva(t *testing.T) {
	h := newHarness(t)
	existente := h.user(inactive(), withEmail("cliente@ejemplo.com"))

	u, err := superadminService(h).Ensure(context.Background(), "cliente@ejemplo.com", "", nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if u.ID != existente.ID || !u.IsGeneralAdmin || !u.IsActive {
		t.Fatalf("usuario = %+v: debía promoverse y reactivarse", u)
	}
	if n := h.idp.createdCount(); n != 0 {
		t.Fatalf("se creó %d cuenta(s) en GoTrue para alguien que ya existía", n)
	}
}

func TestPromoverNoPisaElNombreExistente(t *testing.T) {
	h := newHarness(t)
	h.user(withEmail("ana@ejemplo.com"))
	_, err := pool.Exec(context.Background(), `UPDATE users SET full_name = 'Nombre Original' WHERE email = 'ana@ejemplo.com'`)
	must(t, err)

	otro := "Otro Nombre"
	if _, err := superadminService(h).Ensure(context.Background(), "ana@ejemplo.com", "", &otro); err != nil {
		t.Fatal(err)
	}
	if got := str(t, `SELECT full_name FROM users WHERE email = 'ana@ejemplo.com'`); got != "Nombre Original" {
		t.Fatalf("full_name = %q: el nombre solo se rellena si estaba vacío", got)
	}
}

func TestSuperadminReutilizaLaIdentidadQueYaTieneGoTrue(t *testing.T) {
	// Se registró como cliente en GoTrue y nunca llegó a tener fila de negocio.
	h := newHarness(t)
	cuenta := h.authIdentity()
	h.idp.accounts = map[string]uuid.UUID{"ana@ejemplo.com": cuenta}

	u, err := superadminService(h).Ensure(context.Background(), "ana@ejemplo.com", pwSuperadmin, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if u.ID != cuenta {
		t.Fatalf("id = %s, quería reutilizar la identidad existente %s", u.ID, cuenta)
	}
}

func TestSuperadminNuevoExigeUnaContrasenaSolida(t *testing.T) {
	h := newHarness(t)
	for _, pw := range []string{"", "corta", "once-chars!"} { // 11 caracteres
		_, err := superadminService(h).Ensure(context.Background(), "ana@ejemplo.com", pw, nil)
		if !apperr.Is(err, apperr.KindInvalid) {
			t.Fatalf("contraseña %q debía rechazarse, err = %v", pw, err)
		}
	}
	if h.idp.createdCount() != 0 || count(t, `SELECT count(*) FROM users`) != 0 {
		t.Fatal("no debía crearse nada con una contraseña débil")
	}
}

func TestSuperadminRechazaUnCorreoInvalido(t *testing.T) {
	h := newHarness(t)
	if _, err := superadminService(h).Ensure(context.Background(), "no-es-correo", pwSuperadmin, nil); !apperr.Is(err, apperr.KindInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestSuperadminEncuentraCorreosHistoricosConMayusculas(t *testing.T) {
	// Filas guardadas como se escribieron (el índice único distingue mayúsculas):
	// buscar en minúsculas no debe crear un duplicado.
	h := newHarness(t)
	existente := h.user(withEmail("Ana@Ejemplo.com"))

	u, err := superadminService(h).Ensure(context.Background(), "ana@ejemplo.com", "", nil)
	if err != nil || u.ID != existente.ID {
		t.Fatalf("Ensure = %+v, %v: debía encontrar a la existente", u, err)
	}
}

func TestRetirarUnSuperadmin(t *testing.T) {
	t.Run("quita el rol, desactiva la cuenta y no borra nada", func(t *testing.T) {
		h := newHarness(t)
		viejo := h.user(superadmin(), withEmail("admin@domicilia.dev"))

		found, err := superadminService(h).Retire(context.Background(), "ADMIN@domicilia.dev")
		if err != nil || !found {
			t.Fatalf("Retire = %v, %v", found, err)
		}
		if hasRole(t, viejo.ID, "superadmin") || flag(t, `SELECT is_active FROM users WHERE id = $1`, viejo.ID) {
			t.Fatal("debía quedar sin rol y desactivado")
		}
		// Desde ese momento la API le responde 403 "usuario inactivo", no 401.
		want(t, h.do(http.MethodGet, "/v1/users/me", nil, &viejo), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM users WHERE id = $1`, viejo.ID); n != 1 {
			t.Fatal("retirar no debe borrar la fila: queda por trazabilidad")
		}
	})

	t.Run("un correo que no existe da false y no es un error", func(t *testing.T) {
		h := newHarness(t)
		found, err := superadminService(h).Retire(context.Background(), "fantasma@ejemplo.com")
		if err != nil || found {
			t.Fatalf("Retire = %v, %v; quería false, nil", found, err)
		}
	})

	t.Run("acepta correos heredados que hoy no pasarían la validación", func(t *testing.T) {
		// Las cuentas semilla viejas pueden tener correos de desarrollo raros, y
		// precisamente esas son las que se retiran.
		h := newHarness(t)
		h.user(superadmin(), withEmail("admin@localhost"))
		found, err := superadminService(h).Retire(context.Background(), "admin@localhost")
		if err != nil || !found {
			t.Fatalf("Retire = %v, %v", found, err)
		}
	})
}
