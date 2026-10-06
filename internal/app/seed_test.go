package app_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/seed"
)

// `api seed` da a un entorno de desarrollo una organización y un usuario por rol.

func opcionesDeDemo() seed.Options {
	return seed.Options{
		OrgName:  "Restaurante Demo",
		Admin:    seed.Account{Email: "admin@domicilia.dev", Password: "clave-demo-1"},
		OrgAdmin: seed.Account{Email: "orgadmin@domicilia.dev", Password: "clave-demo-2"},
		Employee: seed.Account{Email: "employee@domicilia.dev", Password: "clave-demo-3"},
	}
}

func TestSeedCreaUnUsuarioPorRolYSuOrganizacion(t *testing.T) {
	h := newHarness(t)
	var out bytes.Buffer
	if err := seed.Run(t.Context(), pool, h.idp, opcionesDeDemo(), &out); err != nil {
		t.Fatalf("seed: %v\n%s", err, out.String())
	}

	if n := count(t, `SELECT count(*) FROM organizations WHERE slug = 'restaurante-demo'`); n != 1 {
		t.Fatalf("organizaciones = %d", n)
	}
	roleIn := func(email string) string {
		return str(t, `SELECT r.code FROM user_organizations uo JOIN roles r ON r.id = uo.role_id
			JOIN users u ON u.id = uo.user_id WHERE u.email = $1`, email)
	}
	if roleIn("admin@domicilia.dev") != "admin" || roleIn("orgadmin@domicilia.dev") != "admin" || roleIn("employee@domicilia.dev") != "employee" {
		t.Fatal("roles de organización incorrectos")
	}

	// El superadmin lo es de verdad: entra al panel de plataforma con su token.
	var sa person
	must(t, pool.QueryRow(t.Context(), `SELECT id, email FROM users WHERE email = 'admin@domicilia.dev'`).Scan(&sa.ID, &sa.Email))
	want(t, h.do(http.MethodGet, "/v1/platform/overview", nil, &sa), http.StatusOK)

	// El admin de la organización NO es superadmin; el empleado tampoco.
	for _, email := range []string{"orgadmin@domicilia.dev", "employee@domicilia.dev"} {
		if flag(t, `SELECT EXISTS (SELECT 1 FROM user_platform_roles upr JOIN users u ON u.id = upr.user_id WHERE u.email = $1)`, email) {
			t.Errorf("%s no debía tener roles de plataforma", email)
		}
	}
}

func TestSeedEsIdempotente(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		var out bytes.Buffer
		if err := seed.Run(t.Context(), pool, h.idp, opcionesDeDemo(), &out); err != nil {
			t.Fatalf("ejecución %d: %v", i+1, err)
		}
		if i > 0 && !strings.Contains(out.String(), "se salta") {
			t.Errorf("la ejecución %d debía saltarse lo existente:\n%s", i+1, out.String())
		}
	}
	if n := count(t, `SELECT count(*) FROM users`); n != 3 {
		t.Fatalf("usuarios = %d, quería 3", n)
	}
	if n := h.idp.createdCount(); n != 3 {
		t.Fatalf("cuentas creadas en GoTrue = %d: repetir no debe crear más", n)
	}
	if n := count(t, `SELECT count(*) FROM user_organizations`); n != 3 {
		t.Fatalf("membresías = %d, quería 3", n)
	}
}

func TestSeedReutilizaUnaCuentaQueYaExisteEnGoTrue(t *testing.T) {
	h := newHarness(t)
	existente := h.authIdentity()
	h.idp.accounts = map[string]uuid.UUID{"employee@domicilia.dev": existente}
	if err := seed.Run(t.Context(), pool, h.idp, opcionesDeDemo(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := str(t, `SELECT id::text FROM users WHERE email = 'employee@domicilia.dev'`); got != existente.String() {
		t.Fatalf("id = %s, quería reutilizar la identidad existente %s", got, existente)
	}
}

func TestSeedExigeTodasLasVariables(t *testing.T) {
	h := newHarness(t)
	o := opcionesDeDemo()
	o.Employee.Password = ""
	o.OrgName = "  "
	err := seed.Run(t.Context(), pool, h.idp, o, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "ADMIN_ORG, EMPLOYEE_PASSWORD") {
		t.Fatalf("err = %v, quería la lista ordenada de lo que falta", err)
	}
	if n := count(t, `SELECT count(*) FROM users`); n != 0 {
		t.Fatal("no debía crearse nada con datos incompletos")
	}
}
