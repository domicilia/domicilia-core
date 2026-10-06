package app_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Perfil, ajustes del negocio y su cara pública.

func TestPerfilDeLaOrganizacion(t *testing.T) {
	t.Run("el admin cambia nombre y descripción; el slug no se mueve", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Nombre Viejo")
		rec := h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": "Nombre Nuevo", "description": "Pan fresco"}, &admin)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["name"] != "Nombre Nuevo" || body["description"] != "Pan fresco" {
			t.Fatalf("organización = %v", body)
		}
		if body["slug"] != o.Slug {
			t.Fatalf("slug = %v: el slug es la dirección pública y no cambia nunca", body["slug"])
		}
		if !contains(auditActions(t, o), "organization.updated") {
			t.Error("el cambio debía auditarse")
		}
	})

	t.Run("una descripción vacía la deja en blanco; omitirla no la toca", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"description": "algo"}, &admin), http.StatusOK)
		rec := h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": "Acme SAS"}, &admin)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["description"] != "algo" {
			t.Fatalf("omitir description la borró: %v", jsonMap(t, rec))
		}
		rec = h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"description": "  "}, &admin)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["description"] != nil {
			t.Fatalf("description = %v, quería null", jsonMap(t, rec)["description"])
		}
	})

	t.Run("el nombre es único sin distinguir mayúsculas, pero puede cambiar de mayúsculas", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.org("Otra Empresa")
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": "OTRA EMPRESA"}, &admin), http.StatusConflict)
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": "ACME"}, &admin), http.StatusOK) // sí puede con el suyo
	})

	t.Run("valida la entrada", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{}, &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": "  "}, &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": strings.Repeat("a", 256)}, &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"description": strings.Repeat("a", 501)}, &admin), http.StatusUnprocessableEntity)
	})

	t.Run("quién puede editar", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		_, o := orgAdmin(h, "Acme")
		empleado, extrano := h.user(), h.user()
		h.join(empleado, o, "employee")
		body := map[string]any{"name": "Hackeada"}
		want(t, h.do(http.MethodPatch, orgURL(o, ""), body, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodPatch, orgURL(o, ""), body, &extrano), http.StatusForbidden)
		want(t, h.do(http.MethodPatch, "/v1/organizations/"+uuid.NewString(), body, &extrano), http.StatusForbidden) // no revela si existe
		want(t, h.do(http.MethodPatch, "/v1/organizations/"+uuid.NewString(), body, &sa), http.StatusNotFound)
		if got := str(t, `SELECT name FROM organizations WHERE id = $1`, o.ID); got != "Acme" {
			t.Fatalf("nombre = %q: alguien sin permiso lo cambió", got)
		}
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"name": "Del Operador"}, &sa), http.StatusOK)
	})
}

func TestAjustesDelNegocio(t *testing.T) {
	t.Run("una organización nueva trae los valores por omisión de Colombia", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodGet, orgURL(o, "/settings"), nil, &admin)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["timezone"] != "America/Bogota" || body["locale"] != "es-CO" || body["currency"] != "COP" {
			t.Fatalf("ajustes = %v", body)
		}
		if hours, ok := body["business_hours"].(map[string]any); !ok || len(hours) != 0 {
			t.Fatalf("business_hours = %v, quería un objeto vacío (no null)", body["business_hours"])
		}
	})

	t.Run("un cambio parcial solo toca lo enviado, normaliza y audita los campos", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{
			"legal_name": "Acme S.A.S.", "tax_id": "900.123.456-7", "contact_email": " Ventas@Acme.COM ",
			"contact_phone": "+57 (300) 123-4567", "address": "Cra 7 # 12-34", "city": "Bogotá",
			"logo_url": "https://cdn.acme.com/logo.png", "currency": "cop",
			"business_hours": map[string]any{"mon": []map[string]string{{"open": "14:00", "close": "20:00"}, {"open": "08:00", "close": "12:00"}}},
		}, &admin)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["contact_email"] != "ventas@acme.com" || body["contact_phone"] != "+573001234567" || body["currency"] != "COP" {
			t.Fatalf("ajustes sin normalizar: %v", body)
		}
		mon := body["business_hours"].(map[string]any)["mon"].([]any)
		if mon[0].(map[string]any)["open"] != "08:00" {
			t.Fatalf("franjas sin ordenar: %v", mon)
		}
		if body["timezone"] != "America/Bogota" {
			t.Errorf("lo no enviado debía quedar igual: %v", body["timezone"])
		}

		// Un segundo cambio de una sola cosa no borra lo demás.
		rec = h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"city": "Medellín"}, &admin)
		want(t, rec, http.StatusOK)
		body = jsonMap(t, rec)
		if body["city"] != "Medellín" || body["legal_name"] != "Acme S.A.S." {
			t.Fatalf("ajustes = %v", body)
		}
		// Un texto vacío deja el campo en blanco.
		rec = h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"address": ""}, &admin)
		if jsonMap(t, rec)["address"] != nil {
			t.Fatalf("address = %v, quería null", jsonMap(t, rec)["address"])
		}

		// La auditoría lista los CAMPOS cambiados, no sus valores.
		got := strs(t, `SELECT detail::text FROM audit_log WHERE organization_id = $1 AND action = 'organization.settings_changed' ORDER BY created_at, id`, o.ID)
		if len(got) != 3 || !strings.Contains(got[1], `"city"`) || strings.Contains(got[1], "Medell") {
			t.Fatalf("auditoría = %v", got)
		}
	})

	t.Run("un cambio que no cambia nada no se audita", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"city": "Cali"}, &admin), http.StatusOK)
		before := len(auditActions(t, o))
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"city": "Cali", "currency": "COP"}, &admin), http.StatusOK)
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{}, &admin), http.StatusOK)
		if after := len(auditActions(t, o)); after != before {
			t.Errorf("entradas de auditoría nuevas = %d, quería 0", after-before)
		}
	})

	t.Run("rechaza valores inválidos y no cambia nada", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"city": "Cali"}, &admin), http.StatusOK)
		for name, body := range map[string]map[string]any{
			"correo":               {"contact_email": "no-es-correo"},
			"teléfono":             {"contact_phone": "123"},
			"logo http":            {"logo_url": "http://acme.com/logo.png"},
			"zona horaria":         {"timezone": "Marte/Olimpo"},
			"locale":               {"locale": "español"},
			"moneda":               {"currency": "PESOS"},
			"nit largo":            {"tax_id": strings.Repeat("9", 31)},
			"día desconocido":      {"business_hours": map[string]any{"lunes": []map[string]string{{"open": "08:00", "close": "12:00"}}}},
			"apertura tras cierre": {"business_hours": map[string]any{"mon": []map[string]string{{"open": "20:00", "close": "08:00"}}}},
			"franjas traslapadas":  {"business_hours": map[string]any{"mon": []map[string]string{{"open": "08:00", "close": "12:00"}, {"open": "11:00", "close": "15:00"}}}},
			"válido + inválido":    {"city": "Bogotá", "currency": "PESOS"},
		} {
			t.Run(name, func(t *testing.T) {
				want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), body, &admin), http.StatusUnprocessableEntity)
			})
		}
		if got := str(t, `SELECT city FROM organization_settings WHERE organization_id = $1`, o.ID); got != "Cali" {
			t.Fatalf("city = %q: una petición rechazada cambió algo", got)
		}
	})

	t.Run("quién lee y quién edita", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		crearRol(t, h, admin, o, "lector", "org.settings.read")
		lector, empleado, extrano := h.user(), h.user(), h.user()
		h.join(lector, o, "lector")
		h.join(empleado, o, "employee")

		want(t, h.do(http.MethodGet, orgURL(o, "/settings"), nil, &lector), http.StatusOK)
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"city": "X"}, &lector), http.StatusForbidden) // leer no es editar
		want(t, h.do(http.MethodGet, orgURL(o, "/settings"), nil, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodGet, orgURL(o, "/settings"), nil, &extrano), http.StatusForbidden)
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"city": "X"}, &extrano), http.StatusForbidden)
		// org.settings.manage no es delegable: un admin no puede fabricar un rol que edite ajustes.
		want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("editor", "Editor", "org.settings.manage"), &admin), http.StatusForbidden)
	})

	t.Run("una organización de otra no se lee ni con su id", func(t *testing.T) {
		h := newHarness(t)
		adminA, _ := orgAdmin(h, "Org A")
		_, b := orgAdmin(h, "Org B")
		want(t, h.do(http.MethodGet, orgURL(b, "/settings"), nil, &adminA), http.StatusForbidden)
		want(t, h.do(http.MethodPatch, orgURL(b, "/settings"), map[string]any{"city": "X"}, &adminA), http.StatusForbidden)
	})

	t.Run("una fila de ajustes que falta se crea al editar y se lee como por omisión", func(t *testing.T) {
		// Una organización creada por fuera del servicio (sin fila de ajustes).
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		if n := count(t, `SELECT count(*) FROM organization_settings WHERE organization_id = $1`, o.ID); n != 0 {
			t.Fatalf("el arnés creó ajustes: %d", n)
		}
		rec := h.do(http.MethodGet, orgURL(o, "/settings"), nil, &admin)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["currency"] != "COP" {
			t.Fatalf("ajustes = %v", jsonMap(t, rec))
		}
		if n := count(t, `SELECT count(*) FROM organization_settings WHERE organization_id = $1`, o.ID); n != 0 {
			t.Error("leer no debe escribir")
		}
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"city": "Cali"}, &admin), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM organization_settings WHERE organization_id = $1`, o.ID); n != 1 {
			t.Error("editar debía crear la fila")
		}
	})

	t.Run("cambios simultáneos no se pisan entre sí", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		var wg sync.WaitGroup
		for _, body := range []map[string]any{{"city": "Cali"}, {"address": "Calle 1"}, {"legal_name": "Acme SAS"}, {"tax_id": "900"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.do(http.MethodPatch, orgURL(o, "/settings"), body, &admin)
			}()
		}
		wg.Wait()
		if got := str(t, `SELECT concat_ws('|', city, address, legal_name, tax_id) FROM organization_settings WHERE organization_id = $1`, o.ID); got != "Cali|Calle 1|Acme SAS|900" {
			t.Fatalf("ajustes = %q: una actualización simultánea pisó a otra", got)
		}
	})
}

func TestLaCaraPublicaDelNegocio(t *testing.T) {
	t.Run("muestra lo necesario para pedir y nada de lo interno", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Café Central")
		want(t, h.do(http.MethodPatch, orgURL(o, ""), map[string]any{"description": "El mejor café"}, &admin), http.StatusOK)
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{
			"city": "Medellín", "logo_url": "https://cdn.cafe.com/logo.png",
			"legal_name": "Café Central S.A.S.", "tax_id": "900123", "contact_email": "interno@cafe.com",
		}, &admin), http.StatusOK)

		rec := h.do(http.MethodGet, "/v1/public/organizations/"+o.Slug, nil, nil)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["name"] != "Café Central" || body["slug"] != "cafe-central" || body["city"] != "Medellín" || body["description"] != "El mejor café" {
			t.Fatalf("cara pública = %v", body)
		}
		for _, secret := range []string{"id", "status", "status_reason", "plan_tier", "legal_name", "tax_id", "contact_email", "contact_phone", "address", "created_at"} {
			if _, leaks := body[secret]; leaks {
				t.Errorf("la cara pública expone %q", secret)
			}
		}
	})

	t.Run("el slug no distingue mayúsculas", func(t *testing.T) {
		h := newHarness(t)
		o := h.org("Acme")
		want(t, h.do(http.MethodGet, "/v1/public/organizations/ACME", nil, nil), http.StatusOK)
		_ = o
	})

	t.Run("inexistente, suspendida y archivada responden exactamente igual", func(t *testing.T) {
		h := newHarness(t)
		susp := h.orgStatus("Suspendida", "suspended")
		arch := h.orgStatus("Archivada", "archived")
		bodies := map[string]string{}
		for name, slug := range map[string]string{"inexistente": "no-existe", "suspendida": susp.Slug, "archivada": arch.Slug} {
			rec := h.do(http.MethodGet, "/v1/public/organizations/"+slug, nil, nil)
			want(t, rec, http.StatusNotFound)
			p := jsonMap(t, rec)
			delete(p, "instance")
			delete(p, "request_id")
			bodies[name] = strings.Join([]string{p["title"].(string), p["detail"].(string)}, "|")
		}
		if bodies["suspendida"] != bodies["inexistente"] || bodies["archivada"] != bodies["inexistente"] {
			t.Fatalf("las respuestas difieren y permiten enumerar negocios: %v", bodies)
		}
	})

	t.Run("open_now sigue el horario en la zona horaria del negocio", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		url := "/v1/public/organizations/" + o.Slug
		bogota, err := time.LoadLocation("America/Bogota")
		must(t, err)

		// Sin horario configurado: no se sabe (null), no "cerrado".
		if got := jsonMap(t, h.do(http.MethodGet, url, nil, nil))["open_now"]; got != nil {
			t.Fatalf("open_now sin horario = %v, quería null", got)
		}

		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{
			"business_hours": map[string]any{"mon": []map[string]string{{"open": "08:00", "close": "12:00"}, {"open": "14:00", "close": "20:00"}}},
		}, &admin), http.StatusOK)

		// 2026-09-28 es lunes.
		for _, tc := range []struct {
			hour, minute int
			want         bool
		}{{9, 0, true}, {12, 0, false}, {13, 0, false}, {14, 0, true}, {19, 59, true}, {20, 0, false}, {7, 59, false}} {
			h.clock = time.Date(2026, 9, 28, tc.hour, tc.minute, 0, 0, bogota)
			if got := jsonMap(t, h.do(http.MethodGet, url, nil, nil))["open_now"]; got != tc.want {
				t.Errorf("lunes %02d:%02d: open_now = %v, quería %v", tc.hour, tc.minute, got, tc.want)
			}
		}
		// Martes no tiene franjas: cerrado.
		h.clock = time.Date(2026, 9, 29, 10, 0, 0, 0, bogota)
		if got := jsonMap(t, h.do(http.MethodGet, url, nil, nil))["open_now"]; got != false {
			t.Errorf("martes: open_now = %v, quería false", got)
		}
		// El mismo instante, en otra zona horaria del negocio, cae en otro día/hora.
		want(t, h.do(http.MethodPatch, orgURL(o, "/settings"), map[string]any{"timezone": "Asia/Tokyo"}, &admin), http.StatusOK)
		h.clock = time.Date(2026, 9, 28, 9, 0, 0, 0, bogota) // en Tokio ya es lunes 23:00
		if got := jsonMap(t, h.do(http.MethodGet, url, nil, nil))["open_now"]; got != false {
			t.Errorf("con zona Asia/Tokyo: open_now = %v, quería false", got)
		}
	})
}

// TestElDirectorioPublico cubre GET /v1/public/organizations — la app de
// cliente eligiendo con cuál organización pedir.
func TestElDirectorioPublico(t *testing.T) {
	t.Run("solo lista activas, sin sesión, y no filtra internos", func(t *testing.T) {
		h := newHarness(t)
		admin, conAjustes := orgAdmin(h, "Café Central")
		want(t, h.do(http.MethodPatch, orgURL(conAjustes, ""), map[string]any{"description": "El mejor café"}, &admin), http.StatusOK)
		want(t, h.do(http.MethodPatch, orgURL(conAjustes, "/settings"), map[string]any{
			"city": "Medellín", "logo_url": "https://cdn.cafe.com/logo.png",
		}, &admin), http.StatusOK)
		// h.org no pasa por Create: nunca tiene fila en organization_settings. Sale
		// igual en el directorio (LEFT JOIN), con logo_url/city en null — la misma
		// resiliencia que ya tiene Settings() con DefaultSettings().
		sinAjustes := h.org("Panadería Sol")
		h.orgStatus("Suspendida", "suspended")
		h.orgStatus("Archivada", "archived")

		rec := h.do(http.MethodGet, "/v1/public/organizations", nil, nil)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["total"] != float64(2) {
			t.Fatalf("total = %v, quería 2 (solo las activas)", body["total"])
		}
		bySlug := map[string]map[string]any{}
		for _, raw := range body["items"].([]any) {
			row := raw.(map[string]any)
			bySlug[row["slug"].(string)] = row
			for _, secret := range []string{"id", "status", "plan_tier", "timezone", "locale", "currency", "business_hours", "open_now"} {
				if _, leaks := row[secret]; leaks {
					t.Errorf("el directorio expone %q", secret)
				}
			}
		}
		conA := bySlug[conAjustes.Slug]
		if conA == nil || conA["name"] != "Café Central" || conA["description"] != "El mejor café" ||
			conA["city"] != "Medellín" || conA["logo_url"] != "https://cdn.cafe.com/logo.png" {
			t.Fatalf("fila con ajustes = %v", conA)
		}
		sinA := bySlug[sinAjustes.Slug]
		if sinA == nil || sinA["city"] != nil || sinA["logo_url"] != nil {
			t.Fatalf("fila sin ajustes = %v, quería city/logo_url en null pero presente en el directorio", sinA)
		}
	})

	t.Run("q filtra por nombre o slug", func(t *testing.T) {
		h := newHarness(t)
		h.org("Pizza Norte")
		h.org("Panadería Sol")

		for url, wantTotal := range map[string]float64{
			"/v1/public/organizations":             2,
			"/v1/public/organizations?q=PIZZA":     1, // sin distinguir mayúsculas
			"/v1/public/organizations?q=panader":   1, // por nombre, prefijo
			"/v1/public/organizations?q=no-existe": 0,
		} {
			if got := jsonMap(t, h.do(http.MethodGet, url, nil, nil))["total"]; got != wantTotal {
				t.Errorf("%s: total = %v, quería %v", url, got, wantTotal)
			}
		}
	})

	t.Run("pagina", func(t *testing.T) {
		h := newHarness(t)
		h.org("Aaa")
		h.org("Bbb")
		h.org("Ccc")

		rec := h.do(http.MethodGet, "/v1/public/organizations?limit=2&offset=1", nil, nil)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		items := body["items"].([]any)
		if len(items) != 2 || body["total"] != float64(3) || body["limit"] != float64(2) || body["offset"] != float64(1) {
			t.Fatalf("página = %v", body)
		}
		if items[0].(map[string]any)["name"] != "Bbb" {
			t.Errorf("orden = %v, quería alfabético empezando en Bbb", items)
		}
	})
}
