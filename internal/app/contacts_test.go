package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Contactos por organización: el mismo teléfono es un contacto distinto en cada negocio.

func contactsURL(o organization, suffix string) string { return orgURL(o, "/contacts"+suffix) }

func TestCrearContactos(t *testing.T) {
	t.Run("normaliza el teléfono, recorta el nombre y pone el correo en minúsculas", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, contactsURL(o, ""), map[string]any{
			"phone": " +57 (300) 111-1111 ", "name": "  Ana Gómez ", "email": " Ana@Ejemplo.COM ",
			"custom_attributes": map[string]any{"ciudad": "Ibagué", "vip": true, "pedidos": 3},
		}, &admin)
		want(t, rec, http.StatusCreated)
		b := jsonMap(t, rec)
		if b["phone"] != "+573001111111" || b["name"] != "Ana Gómez" || b["email"] != "ana@ejemplo.com" || b["source"] != "manual" || b["blocked"] != false {
			t.Fatalf("contacto = %v", b)
		}
		if attrs := b["custom_attributes"].(map[string]any); attrs["ciudad"] != "Ibagué" || attrs["vip"] != true {
			t.Fatalf("atributos = %v", attrs)
		}
	})

	t.Run("exige el código de país y un número válido", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		for name, phone := range map[string]string{
			"sin código de país": "3001111111", "con letras": "+57300abc1111", "muy corto": "+5730", "muy largo": "+5730011111111111111",
			"empieza en cero": "+0573001111", "vacío": "", "solo el +": "+",
		} {
			t.Run(name, func(t *testing.T) {
				want(t, h.do(http.MethodPost, contactsURL(o, ""), map[string]any{"phone": phone}, &admin), http.StatusUnprocessableEntity)
			})
		}
		if n := count(t, `SELECT count(*) FROM contacts`); n != 0 {
			t.Fatalf("contactos = %d", n)
		}
	})

	t.Run("valida nombre, correo y atributos", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		many := map[string]any{}
		for i := range 51 {
			many["k"+str2(int64(i))] = 1
		}
		for name, body := range map[string]map[string]any{
			"nombre largo":     {"phone": "+573001111111", "name": strings.Repeat("a", 256)},
			"correo":           {"phone": "+573001111111", "email": "no-es-correo"},
			"atributo anidado": {"phone": "+573001111111", "custom_attributes": map[string]any{"a": map[string]any{"b": 1}}},
			"atributo lista":   {"phone": "+573001111111", "custom_attributes": map[string]any{"a": []any{1}}},
			"texto enorme":     {"phone": "+573001111111", "custom_attributes": map[string]any{"a": strings.Repeat("x", 501)}},
			"demasiados":       {"phone": "+573001111111", "custom_attributes": many},
			"clave enorme":     {"phone": "+573001111111", "custom_attributes": map[string]any{strings.Repeat("k", 65): 1}},
		} {
			t.Run(name, func(t *testing.T) {
				want(t, h.do(http.MethodPost, contactsURL(o, ""), body, &admin), http.StatusUnprocessableEntity)
			})
		}
	})

	t.Run("el teléfono es único por organización, aunque se escriba distinto; entre organizaciones no", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		want(t, h.do(http.MethodPost, contactsURL(a, ""), map[string]any{"phone": "+573001111111", "name": "Ana"}, &adminA), http.StatusCreated)
		want(t, h.do(http.MethodPost, contactsURL(a, ""), map[string]any{"phone": "+57 300 111 1111"}, &adminA), http.StatusConflict)
		want(t, h.do(http.MethodPost, contactsURL(b, ""), map[string]any{"phone": "+573001111111", "name": "Ana de B"}, &adminB), http.StatusCreated)
		if n := count(t, `SELECT count(*) FROM contacts WHERE phone_e164 = '+573001111111'`); n != 2 {
			t.Fatalf("contactos = %d, quería uno por organización", n)
		}
	})

	t.Run("quién puede crear: el empleado sí, quien solo lee no, un extraño no", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		empleado, lector, extrano := h.user(), h.user(), h.user()
		h.join(empleado, o, "employee")
		crearRol(t, h, admin, o, "lector", "org.contacts.read")
		h.join(lector, o, "lector")
		want(t, h.do(http.MethodPost, contactsURL(o, ""), map[string]any{"phone": "+573001111111"}, &empleado), http.StatusCreated)
		want(t, h.do(http.MethodPost, contactsURL(o, ""), map[string]any{"phone": "+573001111112"}, &lector), http.StatusForbidden)
		want(t, h.do(http.MethodGet, contactsURL(o, ""), nil, &lector), http.StatusOK)
		want(t, h.do(http.MethodPost, contactsURL(o, ""), map[string]any{"phone": "+573001111113"}, &extrano), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM contacts`); n != 1 {
			t.Fatalf("contactos = %d", n)
		}
	})
}

func TestListarYLeerContactos(t *testing.T) {
	t.Run("solo los de su organización, los más nuevos primero, con búsqueda y cursor", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		names := []string{"Ana 100%", "Beto", "Carla", "Diana", "Elena"}
		for i, n := range names {
			want(t, h.do(http.MethodPost, contactsURL(a, ""), map[string]any{"phone": "+57300111000" + str2(int64(i)), "name": n}, &adminA), http.StatusCreated)
			time.Sleep(5 * time.Millisecond) // created_at distintos
		}
		want(t, h.do(http.MethodPost, contactsURL(b, ""), map[string]any{"phone": "+573009990000", "name": "Ajeno"}, &adminB), http.StatusCreated)

		var seen []string
		cursor := ""
		for range 5 {
			u := contactsURL(a, "?limit=2")
			if cursor != "" {
				u += "&cursor=" + cursor
			}
			page := jsonMap(t, h.do(http.MethodGet, u, nil, &adminA))
			for _, it := range itemsOf(t, page) {
				seen = append(seen, it["name"].(string))
			}
			next, _ := page["next_cursor"].(string)
			if next == "" {
				break
			}
			cursor = next
		}
		if strings.Join(seen, ",") != "Elena,Diana,Carla,Beto,Ana 100%" {
			t.Fatalf("contactos = %v", seen)
		}

		search := func(q string) []string {
			var out []string
			for _, it := range itemsOf(t, jsonMap(t, h.do(http.MethodGet, contactsURL(a, "?q="+q), nil, &adminA))) {
				out = append(out, it["name"].(string))
			}
			return out
		}
		if got := search("carl"); strings.Join(got, ",") != "Carla" {
			t.Errorf("búsqueda por nombre = %v", got)
		}
		if got := search("0004"); strings.Join(got, ",") != "Elena" {
			t.Errorf("búsqueda por teléfono = %v", got)
		}
		if got := search("%25"); strings.Join(got, ",") != "Ana 100%" {
			t.Errorf("un %% se toma literal: %v", got)
		}
		if got := search("ajeno"); len(got) != 0 {
			t.Errorf("A encontró un contacto de B: %v", got)
		}
		want(t, h.do(http.MethodGet, contactsURL(a, "?cursor=roto"), nil, &adminA), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodGet, contactsURL(a, "?limit=0"), nil, &adminA), http.StatusUnprocessableEntity)
	})

	t.Run("nadie lee ni cambia los contactos de otra organización", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		rec := h.do(http.MethodPost, contactsURL(b, ""), map[string]any{"phone": "+573009990000", "name": "Ajeno"}, &adminB)
		want(t, rec, http.StatusCreated)
		idB := jsonMap(t, rec)["id"].(string)

		for _, tc := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, contactsURL(b, ""), nil},
			{http.MethodGet, contactsURL(b, "/"+idB), nil},
			{http.MethodPatch, contactsURL(b, "/"+idB), map[string]any{"name": "Hackeado"}},
			{http.MethodPost, contactsURL(b, ""), map[string]any{"phone": "+573001111111"}},
		} {
			if rec := h.do(tc.method, tc.path, tc.body, &adminA); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d, quería 403", tc.method, tc.path, rec.Code)
			}
		}
		// El id de B por la ruta de A no existe para A.
		want(t, h.do(http.MethodGet, contactsURL(a, "/"+idB), nil, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodPatch, contactsURL(a, "/"+idB), map[string]any{"name": "Hackeado"}, &adminA), http.StatusNotFound)
		if got := str(t, `SELECT name FROM contacts WHERE id = $1`, idB); got != "Ajeno" {
			t.Fatalf("el contacto de B cambió: %q", got)
		}
		want(t, h.do(http.MethodGet, contactsURL(a, "/"+uuid.NewString()), nil, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodGet, contactsURL(a, "/no-uuid"), nil, &adminA), http.StatusUnprocessableEntity)
	})
}

func TestEditarContactos(t *testing.T) {
	setup := func(t *testing.T) (*harness, person, organization, string) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, contactsURL(o, ""), map[string]any{"phone": "+573001111111", "name": "Ana", "email": "ana@ejemplo.com", "custom_attributes": map[string]any{"a": 1}}, &admin)
		want(t, rec, http.StatusCreated)
		return h, admin, o, jsonMap(t, rec)["id"].(string)
	}

	t.Run("un cambio parcial solo toca lo enviado; una cadena vacía deja en blanco", func(t *testing.T) {
		h, admin, o, id := setup(t)
		rec := h.do(http.MethodPatch, contactsURL(o, "/"+id), map[string]any{"name": " Ana María "}, &admin)
		want(t, rec, http.StatusOK)
		if b := jsonMap(t, rec); b["name"] != "Ana María" || b["email"] != "ana@ejemplo.com" || b["blocked"] != false {
			t.Fatalf("contacto = %v", b)
		}
		rec = h.do(http.MethodPatch, contactsURL(o, "/"+id), map[string]any{"email": ""}, &admin)
		want(t, rec, http.StatusOK)
		if b := jsonMap(t, rec); b["email"] != nil || b["name"] != "Ana María" {
			t.Fatalf("contacto = %v", b)
		}
		rec = h.do(http.MethodPatch, contactsURL(o, "/"+id), map[string]any{"custom_attributes": map[string]any{"b": "x"}, "blocked": true}, &admin)
		want(t, rec, http.StatusOK)
		if b := jsonMap(t, rec); b["blocked"] != true || len(b["custom_attributes"].(map[string]any)) != 1 || b["custom_attributes"].(map[string]any)["b"] != "x" {
			t.Fatalf("contacto = %v", b)
		}
	})

	t.Run("rechaza cuerpos vacíos y valores inválidos sin cambiar nada", func(t *testing.T) {
		h, admin, o, id := setup(t)
		want(t, h.do(http.MethodPatch, contactsURL(o, "/"+id), map[string]any{}, &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, contactsURL(o, "/"+id), map[string]any{"email": "no-es-correo"}, &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, contactsURL(o, "/"+id), map[string]any{"name": strings.Repeat("a", 256)}, &admin), http.StatusUnprocessableEntity)
		if got := str(t, `SELECT name || '|' || email FROM contacts WHERE id = $1`, id); got != "Ana|ana@ejemplo.com" {
			t.Fatalf("contacto = %q", got)
		}
	})

	t.Run("el teléfono no se edita: es la identidad del contacto", func(t *testing.T) {
		h, admin, o, id := setup(t)
		rec := h.do(http.MethodPatch, contactsURL(o, "/"+id), map[string]any{"phone": "+573009999999", "name": "X"}, &admin)
		if rec.Code == http.StatusOK && jsonMap(t, rec)["phone"] != "+573001111111" {
			t.Fatalf("el teléfono cambió: %v", jsonMap(t, rec))
		}
		if got := str(t, `SELECT phone_e164 FROM contacts WHERE id = $1`, id); got != "+573001111111" {
			t.Fatalf("teléfono = %q", got)
		}
	})
}

func TestContactosQueEscribenPrimero(t *testing.T) {
	w := newWA(t)
	admin := w.admin

	w.in("573001110001", "Ana Perfil", "a1", "hola", time.Minute)
	if got := str(t, `SELECT name || '|' || source FROM contacts`); got != "Ana Perfil|inbound" {
		t.Fatalf("contacto = %q", got)
	}
	// El equipo lo renombra: el nombre del perfil de WhatsApp no lo pisa después.
	id := str(t, `SELECT id::text FROM contacts`)
	want(t, w.h.do(http.MethodPatch, contactsURL(w.o, "/"+id), map[string]any{"name": "Ana María (VIP)"}, &admin), http.StatusOK)
	w.in("573001110001", "Ana P.", "a2", "otra vez", 0)
	if got := str(t, `SELECT name FROM contacts`); got != "Ana María (VIP)" {
		t.Fatalf("el perfil de WhatsApp pisó el nombre puesto por el equipo: %q", got)
	}
	// Un contacto creado a mano se reutiliza cuando esa persona escribe: no se duplica ni cambia de origen.
	want(t, w.h.do(http.MethodPost, contactsURL(w.o, ""), map[string]any{"phone": "+573001110002", "name": "Beto"}, &admin), http.StatusCreated)
	w.in("573001110002", "Beto Perfil", "b1", "hola", 0)
	if got := str(t, `SELECT count(*) || '|' || name || '|' || source FROM contacts WHERE phone_e164 = '+573001110002' GROUP BY name, source`); got != "1|Beto|manual" {
		t.Fatalf("contacto = %q", got)
	}
	// Y esa conversación se abre normalmente.
	if n := count(t, `SELECT count(*) FROM conversations`); n != 2 {
		t.Fatalf("conversaciones = %d", n)
	}
}
