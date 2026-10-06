package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Aislamiento entre inquilinos visto desde HTTP: los códigos de estado que
// devuelve la API cuando alguien intenta alcanzar datos de otra organización.

func orgNames(t *testing.T, h *harness, as person) []string {
	t.Helper()
	rec := h.do(http.MethodGet, "/v1/organizations", nil, &as)
	want(t, rec, http.StatusOK)
	var names []string
	for _, o := range decode[[]map[string]any](t, rec) {
		names = append(names, o["name"].(string))
	}
	return names
}

func TestOrganizacionesSinAutenticar(t *testing.T) {
	h := newHarness(t)
	want(t, h.do(http.MethodGet, "/v1/organizations", nil, nil), http.StatusUnauthorized)
	want(t, h.do(http.MethodPost, "/v1/organizations", map[string]string{"name": "X"}, nil), http.StatusUnauthorized)
}

func TestListadoDeOrganizaciones(t *testing.T) {
	t.Run("el usuario solo ve sus organizaciones", func(t *testing.T) {
		// La prueba de aislamiento más importante del listado: hay tres
		// organizaciones y el usuario pertenece a una.
		h := newHarness(t)
		u := h.user()
		h.join(u, h.org("La mia"), "employee")
		h.org("Ajena uno")
		h.org("Ajena dos")

		got := orgNames(t, h, u)
		if len(got) != 1 || got[0] != "La mia" {
			t.Fatalf("organizaciones = %v, quería solo [La mia]", got)
		}
	})

	t.Run("un usuario sin organizaciones ve una lista vacía, no null", func(t *testing.T) {
		h := newHarness(t)
		h.org("")
		u := h.user()
		rec := h.do(http.MethodGet, "/v1/organizations", nil, &u)
		want(t, rec, http.StatusOK)
		if body := rec.Body.String(); body != "[]\n" && body != "[]" {
			t.Fatalf("cuerpo = %q, quería []", body)
		}
	})

	t.Run("el superadmin las ve todas", func(t *testing.T) {
		h := newHarness(t)
		admin := h.user(superadmin())
		h.org("Una")
		h.org("Otra")

		got := orgNames(t, h, admin)
		if len(got) != 2 {
			t.Fatalf("organizaciones = %v, quería las 2", got)
		}
	})
}

func TestAccesoDirectoAUnaOrganizacion(t *testing.T) {
	t.Run("un miembro lee su organización", func(t *testing.T) {
		h := newHarness(t)
		u, o := h.user(), h.org("")
		h.join(u, o, "employee")

		rec := h.do(http.MethodGet, "/v1/organizations/"+o.ID.String(), nil, &u)
		want(t, rec, http.StatusOK)
		if got := jsonMap(t, rec)["id"]; got != o.ID.String() {
			t.Fatalf("id = %v, quería %s", got, o.ID)
		}
	})

	t.Run("un extraño recibe 403: conocer el identificador no basta", func(t *testing.T) {
		h := newHarness(t)
		ajena := h.org("")
		u := h.user()
		want(t, h.do(http.MethodGet, "/v1/organizations/"+ajena.ID.String(), nil, &u), http.StatusForbidden)
	})

	t.Run("pertenecer a otra organización no abre esta", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		h.join(u, h.org(""), "admin")
		ajena := h.org("")
		want(t, h.do(http.MethodGet, "/v1/organizations/"+ajena.ID.String(), nil, &u), http.StatusForbidden)
	})

	t.Run("una organización inexistente da 404", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodGet, "/v1/organizations/"+uuid.NewString(), nil, &u), http.StatusNotFound)
	})

	// HALLAZGO DE SEGURIDAD heredado, fijado a propósito. Ante una organización
	// que existe pero no es suya, el usuario recibe 403; ante una que no existe,
	// 404. La diferencia permite ENUMERAR: probando identificadores o slugs,
	// alguien de fuera averigua qué organizaciones hay dadas de alta (con slugs
	// es más grave, porque son adivinables: /by-slug/acme que responda 403
	// confirma que Acme es cliente). La práctica habitual es 404 en ambos casos.
	// Si se decide corregir, hay que cambiar Service.Get, Service.GetBySlug y
	// esta prueba a la vez. Ver README_MULTITENANT.md.
	t.Run("los códigos distinguen existir de pertenecer (hallazgo conocido)", func(t *testing.T) {
		h := newHarness(t)
		forastero := h.user()
		existente := h.org("")

		ajena := h.do(http.MethodGet, "/v1/organizations/"+existente.ID.String(), nil, &forastero)
		inexistente := h.do(http.MethodGet, "/v1/organizations/"+uuid.NewString(), nil, &forastero)

		if ajena.Code != http.StatusForbidden || inexistente.Code != http.StatusNotFound {
			t.Fatalf("existente = %d, inexistente = %d; quería 403 y 404", ajena.Code, inexistente.Code)
		}
	})

	t.Run("por slug también exige pertenencia", func(t *testing.T) {
		h := newHarness(t)
		ajena := h.org("Acme Corp")
		u := h.user()
		want(t, h.do(http.MethodGet, "/v1/organizations/by-slug/"+ajena.Slug, nil, &u), http.StatusForbidden)
	})

	t.Run("por slug, un miembro la lee y un slug inexistente da 404", func(t *testing.T) {
		h := newHarness(t)
		u, o := h.user(), h.org("Panadería Sol")
		h.join(u, o, "employee")
		want(t, h.do(http.MethodGet, "/v1/organizations/by-slug/"+o.Slug, nil, &u), http.StatusOK)
		want(t, h.do(http.MethodGet, "/v1/organizations/by-slug/no-existe", nil, &u), http.StatusNotFound)
	})

	// is_active=false es la palanca de suspensión por impago: debe cerrar el
	// acceso incluso a los miembros legítimos, o no serviría de nada.
	t.Run("una organización suspendida se cierra a sus miembros", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		suspendida := h.orgWith("", false)
		h.join(u, suspendida, "admin")
		want(t, h.do(http.MethodGet, "/v1/organizations/"+suspendida.ID.String(), nil, &u), http.StatusForbidden)
	})

	t.Run("el superadmin entra en una suspendida: el operador debe poder inspeccionar lo que suspendió", func(t *testing.T) {
		h := newHarness(t)
		admin := h.user(superadmin())
		suspendida := h.orgWith("", false)
		want(t, h.do(http.MethodGet, "/v1/organizations/"+suspendida.ID.String(), nil, &admin), http.StatusOK)
	})

	t.Run("un identificador mal formado es 422, no 404", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodGet, "/v1/organizations/no-es-un-uuid", nil, &u), http.StatusUnprocessableEntity)
	})
}

func TestCrearOrganizacion(t *testing.T) {
	t.Run("solo el superadmin crea inquilinos", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		h.join(u, h.org(""), "admin")
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]string{"name": "Intento"}, &u), http.StatusForbidden)
	})

	t.Run("el superadmin crea y se genera el slug", func(t *testing.T) {
		h := newHarness(t)
		admin := h.user(superadmin())
		rec := h.do(http.MethodPost, "/v1/organizations",
			map[string]string{"name": "Panaderia Del Centro", "description": "Pan fresco"}, &admin)

		want(t, rec, http.StatusCreated)
		body := jsonMap(t, rec)
		if body["slug"] != "panaderia-del-centro" || body["is_active"] != true || body["plan_tier"] != "starter" {
			t.Fatalf("organización = %v", body)
		}
		if body["description"] != "Pan fresco" {
			t.Fatalf("description = %v", body["description"])
		}
	})

	t.Run("no admite dos organizaciones con el mismo nombre", func(t *testing.T) {
		h := newHarness(t)
		admin := h.user(superadmin())
		existente := h.org("Repetida")
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]string{"name": existente.Name}, &admin), http.StatusConflict)
	})

	t.Run("un nombre largo genera un slug recortado y válido", func(t *testing.T) {
		h := newHarness(t)
		admin := h.user(superadmin())
		largo := strings.Repeat("b", 200)
		rec := h.do(http.MethodPost, "/v1/organizations", map[string]string{"name": largo}, &admin)
		want(t, rec, http.StatusCreated)
		if slug, _ := jsonMap(t, rec)["slug"].(string); len(slug) != 63 {
			t.Fatalf("slug = %q (%d), quería 63 caracteres", slug, len(slug))
		}
	})

	t.Run("no admite dos nombres distintos con el mismo slug", func(t *testing.T) {
		// "Mi Tienda" y "mi tienda" son nombres distintos para la base, pero
		// generan el mismo slug.
		h := newHarness(t)
		admin := h.user(superadmin())
		h.org("Mi Tienda")
		want(t, h.do(http.MethodPost, "/v1/organizations", map[string]string{"name": "mi tienda"}, &admin), http.StatusConflict)
	})

	t.Run("valida la entrada", func(t *testing.T) {
		h := newHarness(t)
		admin := h.user(superadmin())
		largo := make([]byte, 256)
		for i := range largo {
			largo[i] = 'a'
		}

		tests := []struct {
			name string
			body map[string]any
		}{
			{"sin nombre", map[string]any{}},
			{"nombre en blanco", map[string]any{"name": "   "}},
			{"nombre de más de 255 caracteres", map[string]any{"name": string(largo)}},
			{"descripción de más de 500 caracteres", map[string]any{"name": "X", "description": string(append(largo, largo...))}},
			{"nombre sin ningún carácter útil para un slug", map[string]any{"name": "¡¡¡???"}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				want(t, h.do(http.MethodPost, "/v1/organizations", tc.body, &admin), http.StatusUnprocessableEntity)
			})
		}
	})
}

// Los miembros son el punto donde se podría escalar privilegios.
func TestMiembros(t *testing.T) {
	t.Run("un empleado no puede listar los miembros", func(t *testing.T) {
		h := newHarness(t)
		u, o := h.user(), h.org("")
		h.join(u, o, "employee")
		want(t, h.do(http.MethodGet, "/v1/organizations/"+o.ID.String()+"/members", nil, &u), http.StatusForbidden)
	})

	t.Run("un extraño no puede listar los miembros", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodGet, "/v1/organizations/"+h.org("").ID.String()+"/members", nil, &u), http.StatusForbidden)
	})

	t.Run("el administrador de la organización lista sus miembros", func(t *testing.T) {
		h := newHarness(t)
		jefe, o := h.user(), h.org("")
		h.join(jefe, o, "admin")
		h.join(h.user(), o, "employee")

		rec := h.do(http.MethodGet, "/v1/organizations/"+o.ID.String()+"/members", nil, &jefe)
		want(t, rec, http.StatusOK)
		if n := len(decode[[]map[string]any](t, rec)); n != 2 {
			t.Fatalf("miembros = %d, quería 2", n)
		}
	})

	t.Run("el listado no incluye los de otra organización", func(t *testing.T) {
		h := newHarness(t)
		jefe, mia := h.user(), h.org("")
		h.join(jefe, mia, "admin")
		h.join(h.user(), h.org(""), "employee")

		rec := h.do(http.MethodGet, "/v1/organizations/"+mia.ID.String()+"/members", nil, &jefe)
		want(t, rec, http.StatusOK)
		for _, m := range decode[[]map[string]any](t, rec) {
			if m["organization_id"] != mia.ID.String() {
				t.Fatalf("miembro de otra organización: %v", m)
			}
		}
	})

	// Escalada de privilegios: el caso que más importa. Un admin de organización
	// puede sumar empleados, pero no otros administradores; si pudiera, el rol
	// admin se propagaría solo y la distinción con el superadmin no valdría nada.
	t.Run("un admin de organización no puede nombrar otro admin", func(t *testing.T) {
		h := newHarness(t)
		jefe, o, nuevo := h.user(), h.org(""), h.user()
		h.join(jefe, o, "admin")

		rec := h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members",
			map[string]string{"user_id": nuevo.ID.String(), "role": "admin"}, &jefe)
		want(t, rec, http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, nuevo.ID); n != 0 {
			t.Fatal("la membresía se creó pese al 403")
		}
	})

	t.Run("un admin de organización sí puede añadir empleados", func(t *testing.T) {
		h := newHarness(t)
		jefe, o, nuevo := h.user(), h.org(""), h.user()
		h.join(jefe, o, "admin")

		rec := h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members",
			map[string]string{"user_id": nuevo.ID.String(), "role": "employee"}, &jefe)
		want(t, rec, http.StatusCreated)
		if got := jsonMap(t, rec)["role"]; got != "employee" {
			t.Fatalf("role = %v", got)
		}
	})

	t.Run("el superadmin sí puede nombrar admins", func(t *testing.T) {
		h := newHarness(t)
		admin, o, nuevo := h.user(superadmin()), h.org(""), h.user()
		want(t, h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members",
			map[string]string{"user_id": nuevo.ID.String(), "role": "admin"}, &admin), http.StatusCreated)
	})

	t.Run("no se puede afiliar a un usuario inexistente", func(t *testing.T) {
		h := newHarness(t)
		jefe, o := h.user(), h.org("")
		h.join(jefe, o, "admin")
		want(t, h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members",
			map[string]string{"user_id": uuid.NewString(), "role": "employee"}, &jefe), http.StatusNotFound)
	})

	t.Run("no se puede afiliar a una organización inexistente (el superadmin lo ve como 404)", func(t *testing.T) {
		h := newHarness(t)
		admin, u := h.user(superadmin()), h.user()
		want(t, h.do(http.MethodPost, "/v1/organizations/"+uuid.NewString()+"/members",
			map[string]string{"user_id": u.ID.String(), "role": "employee"}, &admin), http.StatusNotFound)
	})

	t.Run("no se puede afiliar dos veces al mismo usuario", func(t *testing.T) {
		h := newHarness(t)
		jefe, o, yaEsta := h.user(), h.org(""), h.user()
		h.join(jefe, o, "admin")
		h.join(yaEsta, o, "employee")
		want(t, h.do(http.MethodPost, "/v1/organizations/"+o.ID.String()+"/members",
			map[string]string{"user_id": yaEsta.ID.String(), "role": "employee"}, &jefe), http.StatusConflict)
	})

	t.Run("valida rol e identificador", func(t *testing.T) {
		h := newHarness(t)
		jefe, o, u := h.user(), h.org(""), h.user()
		h.join(jefe, o, "admin")
		url := "/v1/organizations/" + o.ID.String() + "/members"
		want(t, h.do(http.MethodPost, url, map[string]string{"user_id": u.ID.String(), "role": "dueño"}, &jefe), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, url, map[string]string{"user_id": "no-es-uuid", "role": "employee"}, &jefe), http.StatusUnprocessableEntity)
	})

	t.Run("un extraño no puede expulsar a nadie", func(t *testing.T) {
		h := newHarness(t)
		o, victima, extrano := h.org(""), h.user(), h.user()
		h.join(victima, o, "employee")
		want(t, h.do(http.MethodDelete, "/v1/organizations/"+o.ID.String()+"/members/"+victima.ID.String(), nil, &extrano), http.StatusForbidden)
	})

	t.Run("un empleado no puede expulsar a nadie", func(t *testing.T) {
		h := newHarness(t)
		o, empleado, victima := h.org(""), h.user(), h.user()
		h.join(empleado, o, "employee")
		h.join(victima, o, "employee")
		want(t, h.do(http.MethodDelete, "/v1/organizations/"+o.ID.String()+"/members/"+victima.ID.String(), nil, &empleado), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, victima.ID); n != 1 {
			t.Fatal("la víctima fue expulsada pese al 403")
		}
	})

	t.Run("el administrador expulsa a un miembro", func(t *testing.T) {
		h := newHarness(t)
		jefe, o, saliente := h.user(), h.org(""), h.user()
		h.join(jefe, o, "admin")
		h.join(saliente, o, "employee")
		want(t, h.do(http.MethodDelete, "/v1/organizations/"+o.ID.String()+"/members/"+saliente.ID.String(), nil, &jefe), http.StatusNoContent)
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, saliente.ID); n != 0 {
			t.Fatal("el miembro sigue en la organización")
		}
	})

	t.Run("expulsar a quien no es miembro da 404", func(t *testing.T) {
		h := newHarness(t)
		jefe, o := h.user(), h.org("")
		h.join(jefe, o, "admin")
		want(t, h.do(http.MethodDelete, "/v1/organizations/"+o.ID.String()+"/members/"+h.user().ID.String(), nil, &jefe), http.StatusNotFound)
	})

	// Comportamiento correcto y deliberado: quien no administra recibe 403 tanto
	// si la organización existe como si no. Contrasta con Get, que sí distingue.
	t.Run("una organización inexistente da 403 y no 404 a quien no la administra", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		want(t, h.do(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/members", nil, &u), http.StatusForbidden)
	})

	t.Run("ser admin de otra organización no sirve", func(t *testing.T) {
		h := newHarness(t)
		u := h.user()
		h.join(u, h.org(""), "admin")
		otra := h.org("")
		want(t, h.do(http.MethodGet, "/v1/organizations/"+otra.ID.String()+"/members", nil, &u), http.StatusForbidden)
	})
}
