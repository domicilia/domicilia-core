package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Bandeja de atención: lista, ventana de 24 h, cambios y aislamiento entre organizaciones.

// waSetup es una organización con su bandeja conectada, un administrador y un agente (empleado).
type waSetup struct {
	h     *harness
	o     organization
	admin person
	agent person
	c     connected
}

func newWA(t *testing.T) waSetup {
	t.Helper()
	h := newHarness(t)
	// Un reloj fijo pero CERCANO al real: la base usa su propio now() para decidir qué mensajes están listos.
	h.clock = time.Now().UTC().Truncate(time.Second)
	admin, o := orgAdmin(h, "Acme")
	agent := h.user()
	h.join(agent, o, "employee")
	return waSetup{h: h, o: o, admin: admin, agent: agent, c: h.connect(admin, o, "Pedidos")}
}

// in entrega un mensaje de un cliente (phone sin "+") que llegó hace `ago`.
func (w waSetup) in(phone, name, id, body string, ago time.Duration) {
	w.h.t.Helper()
	want(w.h.t, w.h.inbound(w.c.PhoneNumberID, waMsg{From: phone, Name: name, ID: id, Body: body, At: w.h.clock.Add(-ago)}), http.StatusOK)
}

func (w waSetup) conv(phone string) string { return convOf(w.h.t, w.o, "+"+phone) }

func (w waSetup) url(suffix string) string { return orgURL(w.o, "/conversations"+suffix) }

func (w waSetup) get(as person, id string) map[string]any {
	w.h.t.Helper()
	rec := w.h.do(http.MethodGet, w.url("/"+id), nil, &as)
	want(w.h.t, rec, http.StatusOK)
	return jsonMap(w.h.t, rec)
}

func itemsOf(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, x := range m["items"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func namesOf(items []map[string]any) []string {
	var out []string
	for _, it := range items {
		c := it["contact"].(map[string]any)
		if n, ok := c["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

func TestListadoDeConversaciones(t *testing.T) {
	t.Run("ordena por actividad reciente y solo trae las abiertas por omisión", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "uno", 50*time.Minute)
		w.in("573001110002", "Beto", "b1", "dos", 30*time.Minute)
		w.in("573001110003", "Carla", "c1", "tres", 10*time.Minute)
		_, err := pool.Exec(t.Context(), `UPDATE conversations SET status = 'resolved' WHERE contact_id = (SELECT id FROM contacts WHERE name = 'Beto')`)
		must(t, err)

		got := namesOf(itemsOf(t, jsonMap(t, w.h.do(http.MethodGet, w.url(""), nil, &w.agent))))
		if strings.Join(got, ",") != "Carla,Ana" {
			t.Fatalf("abiertas = %v, quería Carla, Ana (la resuelta no sale)", got)
		}
		got = namesOf(itemsOf(t, jsonMap(t, w.h.do(http.MethodGet, w.url("?status=all"), nil, &w.agent))))
		if strings.Join(got, ",") != "Carla,Beto,Ana" {
			t.Fatalf("todas = %v", got)
		}
		got = namesOf(itemsOf(t, jsonMap(t, w.h.do(http.MethodGet, w.url("?status=resolved"), nil, &w.agent))))
		if strings.Join(got, ",") != "Beto" {
			t.Fatalf("resueltas = %v", got)
		}
	})

	t.Run("un cursor recorre todo sin repetir ni saltarse filas, aunque lleguen mensajes nuevos", func(t *testing.T) {
		w := newWA(t)
		for i := range 7 {
			w.in("57300111000"+str2(int64(i)), "C"+str2(int64(i)), "w"+str2(int64(i)), "hola", time.Duration(70-i*5)*time.Minute)
		}
		var seen []string
		cursor := ""
		for page := range 10 {
			u := w.url("?limit=3")
			if cursor != "" {
				u += "&cursor=" + cursor
			}
			body := jsonMap(t, w.h.do(http.MethodGet, u, nil, &w.agent))
			seen = append(seen, namesOf(itemsOf(t, body))...)
			if page == 0 {
				// Entre página y página llega un mensaje del cliente más viejo: sube al tope, pero la
				// lectura en curso no repite ni pierde a nadie de lo que falta por leer.
				w.in("573001110000", "C0", "w-nuevo", "otra vez", 0)
			}
			next, _ := body["next_cursor"].(string)
			if next == "" {
				break
			}
			cursor = next
		}
		seenSet := map[string]int{}
		for _, n := range seen {
			seenSet[n]++
		}
		for n, c := range seenSet {
			if c > 1 {
				t.Errorf("%s apareció %d veces", n, c)
			}
		}
		if len(seenSet) < 6 { // C0 pudo haber quedado atrás de lo ya leído, pero nadie más se pierde
			t.Fatalf("se leyeron %d de 7: %v", len(seenSet), seen)
		}
	})

	t.Run("filtra por responsable, bandeja y búsqueda (con comodines literales)", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana 100%", "a1", "x", 5*time.Minute)
		w.in("573001110002", "Beto", "b1", "x", 4*time.Minute)
		w.in("573001110003", "Carla", "c1", "x", 3*time.Minute)
		want(t, w.h.do(http.MethodPatch, w.url("/"+w.conv("573001110001")), map[string]any{"assignee_id": w.agent.ID.String()}, &w.admin), http.StatusOK)
		want(t, w.h.do(http.MethodPatch, w.url("/"+w.conv("573001110002")), map[string]any{"assignee_id": w.admin.ID.String()}, &w.admin), http.StatusOK)

		names := func(q string, as person) string {
			return strings.Join(namesOf(itemsOf(t, jsonMap(t, w.h.do(http.MethodGet, w.url(q), nil, &as)))), ",")
		}
		if got := names("?assignee=me", w.agent); got != "Ana 100%" {
			t.Errorf("mías = %q", got)
		}
		if got := names("?assignee=unassigned", w.agent); got != "Carla" {
			t.Errorf("sin asignar = %q", got)
		}
		if got := names("?assignee="+w.admin.ID.String(), w.agent); got != "Beto" {
			t.Errorf("de otro usuario = %q", got)
		}
		if got := names("?q=beto", w.agent); got != "Beto" {
			t.Errorf("búsqueda por nombre = %q", got)
		}
		if got := names("?q=110003", w.agent); got != "Carla" {
			t.Errorf("búsqueda por teléfono = %q", got)
		}
		if got := names("?q=%25", w.agent); got != "Ana 100%" { // q="%": solo el que tiene % en el nombre
			t.Errorf("un %% se toma literal, no como comodín: %q", got)
		}
		if got := names("?q=_", w.agent); got != "" {
			t.Errorf("un _ se toma literal: %q", got)
		}
		if got := names("?inbox_id="+w.c.Inbox, w.agent); got != "Carla,Beto,Ana 100%" {
			t.Errorf("por bandeja = %q", got)
		}
		if got := names("?inbox_id="+uuid.NewString(), w.agent); got != "" {
			t.Errorf("otra bandeja = %q", got)
		}
	})

	t.Run("valida los filtros", func(t *testing.T) {
		w := newWA(t)
		for name, q := range map[string]string{
			"estado desconocido": "?status=borrada", "responsable inválido": "?assignee=nadie", "limit 0": "?limit=0", "limit enorme": "?limit=101",
			"limit no numérico": "?limit=x", "cursor roto": "?cursor=!!!", "bandeja inválida": "?inbox_id=no-uuid",
		} {
			t.Run(name, func(t *testing.T) {
				want(t, w.h.do(http.MethodGet, w.url(q), nil, &w.agent), http.StatusUnprocessableEntity)
			})
		}
	})

	t.Run("las pestañas cuentan mías, sin asignar y todas", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "x", 5*time.Minute)
		w.in("573001110002", "Beto", "b1", "x", 4*time.Minute)
		w.in("573001110003", "Carla", "c1", "x", 3*time.Minute)
		want(t, w.h.do(http.MethodPatch, w.url("/"+w.conv("573001110001")), map[string]any{"assignee_id": w.agent.ID.String()}, &w.admin), http.StatusOK)
		rec := w.h.do(http.MethodGet, w.url("/counts"), nil, &w.agent)
		want(t, rec, http.StatusOK)
		if b := jsonMap(t, rec); b["mine"] != float64(1) || b["unassigned"] != float64(2) || b["all"] != float64(3) {
			t.Fatalf("pestañas = %v", b)
		}
	})
}

func TestAislamientoDeLasConversaciones(t *testing.T) {
	h := newHarness(t)
	h.clock = time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC)
	adminA, a := orgAdmin(h, "Org A")
	adminB, b := orgAdmin(h, "Org B")
	ca, cb := h.connect(adminA, a, "A"), h.connect(adminB, b, "B")
	want(t, h.inbound(ca.PhoneNumberID, waMsg{From: "573001110001", Name: "Cliente de A", ID: "a1", Body: "secreto de A", At: h.clock}), http.StatusOK)
	want(t, h.inbound(cb.PhoneNumberID, waMsg{From: "573001110002", Name: "Cliente de B", ID: "b1", Body: "secreto de B", At: h.clock}), http.StatusOK)
	convA, convB := convOf(t, a, "+573001110001"), convOf(t, b, "+573001110002")
	extrano := h.user()

	// Cada administrador ve solo lo suyo.
	got := namesOf(itemsOf(t, jsonMap(t, h.do(http.MethodGet, orgURL(a, "/conversations?status=all"), nil, &adminA))))
	if strings.Join(got, ",") != "Cliente de A" {
		t.Fatalf("A ve %v", got)
	}
	// Nadie lee la organización ajena, con ninguna operación.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, orgURL(b, "/conversations"), nil},
		{http.MethodGet, orgURL(b, "/conversations/counts"), nil},
		{http.MethodGet, orgURL(b, "/conversations/"+convB), nil},
		{http.MethodPatch, orgURL(b, "/conversations/"+convB), map[string]any{"status": "resolved"}},
		{http.MethodPost, orgURL(b, "/conversations/"+convB+"/read"), nil},
		{http.MethodGet, orgURL(b, "/conversations/"+convB+"/messages"), nil},
		{http.MethodPost, orgURL(b, "/conversations/"+convB+"/messages"), map[string]any{"body": "hola"}},
	} {
		if rec := h.do(tc.method, tc.path, tc.body, &adminA); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, quería 403", tc.method, tc.path, rec.Code)
		}
		if rec := h.do(tc.method, tc.path, tc.body, &extrano); rec.Code != http.StatusForbidden {
			t.Errorf("(extraño) %s %s: %d, quería 403", tc.method, tc.path, rec.Code)
		}
	}
	// La conversación de B usada por la ruta de A no existe para A: 404 (no se revela).
	for _, tc := range []struct {
		method, suffix string
		body           any
	}{
		{http.MethodGet, "", nil}, {http.MethodPatch, "", map[string]any{"status": "resolved"}},
		{http.MethodPost, "/read", nil}, {http.MethodGet, "/messages", nil}, {http.MethodPost, "/messages", map[string]any{"body": "hola"}},
	} {
		if rec := h.do(tc.method, orgURL(a, "/conversations/"+convB+tc.suffix), tc.body, &adminA); rec.Code != http.StatusNotFound {
			t.Errorf("%s /conversations/{de B}%s por la ruta de A: %d, quería 404", tc.method, tc.suffix, rec.Code)
		}
	}
	if got := str(t, `SELECT status FROM conversations WHERE id = $1`, convB); got != "open" {
		t.Fatalf("la conversación de B cambió: %q", got)
	}
	if n := count(t, `SELECT count(*) FROM messages WHERE direction = 'outbound'`); n != 0 {
		t.Fatalf("se encoló %d mensajes desde una organización ajena", n)
	}
	_ = convA

	// La integridad la impone también la BASE: una fila no puede apuntar a datos de otra organización.
	t.Run("la base rechaza filas que cruzan organizaciones", func(t *testing.T) {
		inboxA := str(t, `SELECT id::text FROM inboxes WHERE organization_id = $1`, a.ID)
		for _, tc := range []struct {
			name string
			q    string
			args []any
		}{
			{"conversación de A con un contacto de B", `INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id)
				SELECT $1::uuid, $2::uuid, ct.id, 99 FROM contacts ct WHERE ct.organization_id = $3::uuid LIMIT 1`, []any{a.ID, inboxA, b.ID}},
			{"mensaje de A en una conversación de B", `INSERT INTO messages (organization_id, conversation_id, inbox_id, direction, kind, status)
				VALUES ($1::uuid, $2::uuid, $3::uuid, 'inbound', 'text', 'received')`, []any{a.ID, convB, inboxA}},
			{"conversación de A en una bandeja de B", `INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id)
				SELECT $1::uuid, $2::uuid, ct.id, 98 FROM contacts ct WHERE ct.organization_id = $1::uuid LIMIT 1`, []any{a.ID, cb.Inbox}},
			{"canal de A sobre una bandeja de B", `INSERT INTO whatsapp_channels (inbox_id, organization_id, whatsapp_account_id, phone_number_id, display_phone)
				SELECT $2::uuid, $1::uuid, id, '555555555', '+57 1' FROM whatsapp_accounts WHERE organization_id = $1::uuid`, []any{a.ID, cb.Inbox}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := pool.Exec(t.Context(), tc.q, tc.args...); err == nil {
					t.Fatal("la base aceptó una fila que cruza organizaciones")
				}
			})
		}
	})
}

func TestVentanaDe24Horas(t *testing.T) {
	canReply := func(t *testing.T, w waSetup, id string) (bool, any, any) {
		m := w.get(w.agent, id)
		return m["can_reply"].(bool), m["reply_block_reason"], m["window_expires_at"]
	}

	t.Run("dentro de la ventana se puede responder y se ve cuándo se cierra", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", 23*time.Hour+59*time.Minute)
		ok, reason, exp := canReply(t, w, w.conv("573001110001"))
		if !ok || reason != nil {
			t.Fatalf("can_reply=%v reason=%v", ok, reason)
		}
		end, err := time.Parse(time.RFC3339, exp.(string))
		if err != nil || !end.Equal(w.h.clock.Add(time.Minute)) {
			t.Fatalf("window_expires_at = %v, quería %v", exp, w.h.clock.Add(time.Minute))
		}
	})

	t.Run("a las 24 h exactas ya está cerrada", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", 24*time.Hour)
		ok, reason, _ := canReply(t, w, w.conv("573001110001"))
		if ok || reason != "window_closed" {
			t.Fatalf("can_reply=%v reason=%v", ok, reason)
		}
	})

	t.Run("la ventana la renueva cada mensaje del cliente, no lo que escribimos nosotros", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", 23*time.Hour)
		conv := w.conv("573001110001")
		want(t, w.h.do(http.MethodPost, w.url("/"+conv+"/messages"), map[string]any{"body": "hola Ana"}, &w.agent), http.StatusAccepted)
		w.h.clock = w.h.clock.Add(2 * time.Hour) // pasan 25 h desde que ella escribió
		if ok, reason, _ := canReply(t, w, conv); ok || reason != "window_closed" {
			t.Fatalf("responder no debe renovar la ventana: can_reply=%v reason=%v", ok, reason)
		}
		want(t, w.h.inbound(w.c.PhoneNumberID, waMsg{From: "573001110001", ID: "a2", Body: "sigo aquí", At: w.h.clock}), http.StatusOK)
		if ok, _, _ := canReply(t, w, conv); !ok {
			t.Fatal("un mensaje del cliente debía reabrir la ventana")
		}
	})

	t.Run("sin un mensaje del cliente nunca se abre", func(t *testing.T) {
		w := newWA(t)
		_, err := pool.Exec(t.Context(), `INSERT INTO contacts (organization_id, phone_e164, source) VALUES ($1, '+573001110009', 'manual')`, w.o.ID)
		must(t, err)
		_, err = pool.Exec(t.Context(), `INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id)
			SELECT $1, $2, id, 50 FROM contacts WHERE phone_e164 = '+573001110009'`, w.o.ID, w.c.Inbox)
		must(t, err)
		ok, reason, exp := canReply(t, w, w.conv("573001110009"))
		if ok || reason != "window_closed" || exp != nil {
			t.Fatalf("can_reply=%v reason=%v window_expires_at=%v", ok, reason, exp)
		}
	})

	t.Run("un contacto bloqueado o una bandeja desconectada no se pueden responder", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		conv := w.conv("573001110001")
		want(t, w.h.do(http.MethodPatch, orgURL(w.o, "/contacts/"+str(t, `SELECT id::text FROM contacts WHERE phone_e164 = '+573001110001'`)), map[string]any{"blocked": true}, &w.admin), http.StatusOK)
		if ok, reason, _ := canReply(t, w, conv); ok || reason != "contact_blocked" {
			t.Fatalf("bloqueado: can_reply=%v reason=%v", ok, reason)
		}
		want(t, w.h.do(http.MethodPatch, orgURL(w.o, "/contacts/"+str(t, `SELECT id::text FROM contacts WHERE phone_e164 = '+573001110001'`)), map[string]any{"blocked": false}, &w.admin), http.StatusOK)
		if ok, _, _ := canReply(t, w, conv); !ok {
			t.Fatal("desbloqueado debía poder responderse")
		}
		want(t, w.h.do(http.MethodDelete, orgURL(w.o, "/inboxes/"+w.c.Inbox), nil, &w.admin), http.StatusNoContent)
		if ok, reason, _ := canReply(t, w, conv); ok || reason != "inbox_disconnected" {
			t.Fatalf("desconectada: can_reply=%v reason=%v", ok, reason)
		}
	})

	t.Run("con el token rechazado por Meta la bandeja pasa a inbox_disconnected", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		w.h.meta.revokeToken(w.c.WABA)
		want(t, w.h.do(http.MethodPost, orgURL(w.o, "/inboxes/"+w.c.Inbox+"/refresh"), nil, &w.admin), http.StatusOK)
		if ok, reason, _ := canReply(t, w, w.conv("573001110001")); ok || reason != "inbox_disconnected" {
			t.Fatalf("can_reply=%v reason=%v", ok, reason)
		}
	})
}

func TestCambiarUnaConversacion(t *testing.T) {
	setup := func(t *testing.T) (waSetup, string) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		return w, w.conv("573001110001")
	}
	patch := func(w waSetup, id string, body map[string]any, as person) *httptest.ResponseRecorder {
		return w.h.do(http.MethodPatch, w.url("/"+id), body, &as)
	}

	t.Run("cambia el estado y la prioridad, y una conversación resuelta se reabre", func(t *testing.T) {
		w, id := setup(t)
		rec := patch(w, id, map[string]any{"status": "pending", "priority": "high"}, w.agent)
		want(t, rec, http.StatusOK)
		if b := jsonMap(t, rec); b["status"] != "pending" || b["priority"] != "high" {
			t.Fatalf("conversación = %v", b)
		}
		want(t, patch(w, id, map[string]any{"status": "resolved", "priority": nil}, w.agent), http.StatusOK)
		if b := w.get(w.agent, id); b["status"] != "resolved" || b["priority"] != nil {
			t.Fatalf("resuelta = %v", b)
		}
		want(t, patch(w, id, map[string]any{"status": "open"}, w.agent), http.StatusOK)
	})

	t.Run("no se reabre una conversación si el contacto ya tiene otra abierta", func(t *testing.T) {
		w, id := setup(t)
		want(t, patch(w, id, map[string]any{"status": "resolved"}, w.agent), http.StatusOK)
		w.in("573001110001", "Ana", "a2", "de nuevo", 0) // abre una NUEVA
		want(t, patch(w, id, map[string]any{"status": "open"}, w.agent), http.StatusConflict)
		if got := str(t, `SELECT status FROM conversations WHERE id = $1`, id); got != "resolved" {
			t.Fatalf("estado = %q", got)
		}
	})

	t.Run("posponer exige una fecha futura y solo con status snoozed", func(t *testing.T) {
		w, id := setup(t)
		future := w.h.clock.Add(48 * time.Hour).Format(time.RFC3339)
		past := w.h.clock.Add(-time.Hour).Format(time.RFC3339)
		want(t, patch(w, id, map[string]any{"status": "snoozed"}, w.agent), http.StatusUnprocessableEntity)
		want(t, patch(w, id, map[string]any{"status": "snoozed", "snoozed_until": past}, w.agent), http.StatusUnprocessableEntity)
		want(t, patch(w, id, map[string]any{"snoozed_until": future}, w.agent), http.StatusUnprocessableEntity)
		want(t, patch(w, id, map[string]any{"status": "snoozed", "snoozed_until": future}, w.agent), http.StatusOK)
		want(t, patch(w, id, map[string]any{"status": "open"}, w.agent), http.StatusOK)
		if got := str(t, `SELECT coalesce(snoozed_until::text, '-') FROM conversations WHERE id = $1`, id); got != "-" {
			t.Fatalf("snoozed_until = %q: salir de pospuesta debía limpiar la fecha", got)
		}
	})

	t.Run("asignar: solo a quien puede atender la bandeja de ESTA organización", func(t *testing.T) {
		w, id := setup(t)
		soloLee := w.h.user()
		crearRol(t, w.h, w.admin, w.o, "lector", "org.inbox.read")
		w.h.join(soloLee, w.o, "lector")
		inactivo := w.h.user(inactive())
		w.h.join(inactivo, w.o, "employee")
		ajeno := w.h.user() // empleado de OTRA organización
		w.h.join(ajeno, w.h.org(""), "employee")
		sinRol := w.h.user()

		for name, u := range map[string]person{"solo puede leer": soloLee, "cuenta inactiva": inactivo, "de otra organización": ajeno, "sin pertenecer": sinRol} {
			t.Run(name, func(t *testing.T) {
				want(t, patch(w, id, map[string]any{"assignee_id": u.ID.String()}, w.admin), http.StatusUnprocessableEntity)
			})
		}
		want(t, patch(w, id, map[string]any{"assignee_id": uuid.NewString()}, w.admin), http.StatusUnprocessableEntity)
		if got := str(t, `SELECT coalesce(assignee_id::text, '-') FROM conversations WHERE id = $1`, id); got != "-" {
			t.Fatalf("assignee = %q: una asignación rechazada cambió algo", got)
		}
		want(t, patch(w, id, map[string]any{"assignee_id": w.agent.ID.String()}, w.admin), http.StatusOK)
		want(t, patch(w, id, map[string]any{"assignee_id": nil}, w.agent), http.StatusOK) // null desasigna
		if b := w.get(w.agent, id); b["assignee_id"] != nil {
			t.Fatalf("assignee_id = %v", b["assignee_id"])
		}
	})

	t.Run("pasa de bot a persona y viceversa", func(t *testing.T) {
		w, id := setup(t)
		want(t, patch(w, id, map[string]any{"handled_by": "bot"}, w.agent), http.StatusOK)
		if b := w.get(w.agent, id); b["handled_by"] != "bot" {
			t.Fatalf("handled_by = %v", b["handled_by"])
		}
		want(t, patch(w, id, map[string]any{"handled_by": "robot"}, w.agent), http.StatusUnprocessableEntity)
	})

	t.Run("rechaza valores inválidos y cuerpos vacíos", func(t *testing.T) {
		w, id := setup(t)
		for name, body := range map[string]map[string]any{
			"estado": {"status": "borrada"}, "prioridad": {"priority": "altísima"}, "vacío": {}, "asignado inválido": {"assignee_id": "no-uuid"},
		} {
			t.Run(name, func(t *testing.T) {
				rec := patch(w, id, body, w.agent)
				if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
					t.Fatalf("código = %d", rec.Code)
				}
			})
		}
		want(t, patch(w, uuid.NewString(), map[string]any{"status": "open"}, w.agent), http.StatusNotFound)
	})

	t.Run("quién puede: leer no es atender", func(t *testing.T) {
		w, id := setup(t)
		lector, extrano := w.h.user(), w.h.user()
		crearRol(t, w.h, w.admin, w.o, "lector", "org.inbox.read")
		w.h.join(lector, w.o, "lector")
		want(t, w.h.do(http.MethodGet, w.url("/"+id), nil, &lector), http.StatusOK)
		want(t, patch(w, id, map[string]any{"status": "resolved"}, lector), http.StatusForbidden)
		want(t, w.h.do(http.MethodPost, w.url("/"+id+"/messages"), map[string]any{"body": "hola"}, &lector), http.StatusForbidden)
		want(t, patch(w, id, map[string]any{"status": "resolved"}, extrano), http.StatusForbidden)
		if got := str(t, `SELECT status FROM conversations WHERE id = $1`, id); got != "open" {
			t.Fatalf("estado = %q", got)
		}
	})

	t.Run("marcar como leída pone en cero los no leídos", func(t *testing.T) {
		w, id := setup(t)
		w.in("573001110001", "Ana", "a2", "otro", 0)
		if got := str(t, `SELECT unread_count::text FROM conversations WHERE id = $1`, id); got != "2" {
			t.Fatalf("no leídos = %s", got)
		}
		want(t, w.h.do(http.MethodPost, w.url("/"+id+"/read"), nil, &w.agent), http.StatusNoContent)
		if got := str(t, `SELECT unread_count::text FROM conversations WHERE id = $1`, id); got != "0" {
			t.Fatalf("no leídos = %s", got)
		}
		want(t, w.h.do(http.MethodPost, w.url("/"+uuid.NewString()+"/read"), nil, &w.agent), http.StatusNotFound)
	})

	t.Run("una organización suspendida o archivada no opera sus conversaciones", func(t *testing.T) {
		w, id := setup(t)
		sa := w.h.user(superadmin())
		want(t, w.h.do(http.MethodPost, platformOrgURL(w.o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, w.h.do(http.MethodGet, w.url("/"+id), nil, &w.agent), http.StatusForbidden)
		want(t, patch(w, id, map[string]any{"status": "resolved"}, w.agent), http.StatusForbidden)
		want(t, w.h.do(http.MethodPost, w.url("/"+id+"/messages"), map[string]any{"body": "hola"}, &w.agent), http.StatusForbidden)
		want(t, w.h.do(http.MethodPost, platformOrgURL(w.o, "/archive"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, w.h.do(http.MethodGet, w.url("/"+id), nil, &w.agent), http.StatusNotFound)
	})
}

func TestHiloDeMensajes(t *testing.T) {
	w := newWA(t)
	for i := range 5 {
		w.in("573001110001", "Ana", "m"+str2(int64(i)), "mensaje "+str2(int64(i)), time.Duration(50-i*10)*time.Minute)
	}
	id := w.conv("573001110001")

	t.Run("del más nuevo al más viejo, con cursor", func(t *testing.T) {
		var bodies []string
		cursor := ""
		for range 5 {
			u := w.url("/" + id + "/messages?limit=2")
			if cursor != "" {
				u += "&cursor=" + cursor
			}
			page := jsonMap(t, w.h.do(http.MethodGet, u, nil, &w.agent))
			for _, m := range itemsOf(t, page) {
				bodies = append(bodies, m["body"].(string))
			}
			next, _ := page["next_cursor"].(string)
			if next == "" {
				break
			}
			cursor = next
		}
		if strings.Join(bodies, "|") != "mensaje 4|mensaje 3|mensaje 2|mensaje 1|mensaje 0" {
			t.Fatalf("hilo = %v", bodies)
		}
	})

	t.Run("trae el estado y los adjuntos", func(t *testing.T) {
		want(t, w.h.inbound(w.c.PhoneNumberID, waMsg{From: "573001110001", ID: "img", Type: "image", At: w.h.clock,
			Extra: map[string]any{"image": map[string]any{"id": "MED1", "mime_type": "image/png", "caption": "foto"}}}), http.StatusOK)
		page := jsonMap(t, w.h.do(http.MethodGet, w.url("/"+id+"/messages?limit=1"), nil, &w.agent))
		m := itemsOf(t, page)[0]
		atts := m["attachments"].([]any)
		if m["kind"] != "image" || m["direction"] != "inbound" || m["status"] != "received" || len(atts) != 1 || atts[0].(map[string]any)["mime_type"] != "image/png" {
			t.Fatalf("mensaje = %v", m)
		}
	})

	t.Run("valida el cursor y la conversación", func(t *testing.T) {
		want(t, w.h.do(http.MethodGet, w.url("/"+id+"/messages?cursor=roto"), nil, &w.agent), http.StatusUnprocessableEntity)
		want(t, w.h.do(http.MethodGet, w.url("/"+uuid.NewString()+"/messages"), nil, &w.agent), http.StatusNotFound)
	})
}
