package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Envío de mensajes: encolar, entregar a Meta y qué pasa cuando falla. Lo que no debe pasar nunca: un mensaje
// duplicado ante el cliente, dos mensajes de una conversación desordenados, o un token en un log.

func (w waSetup) send(as person, conv, body string) *httptest.ResponseRecorder {
	return w.h.do(http.MethodPost, w.url("/"+conv+"/messages"), map[string]any{"body": body}, &as)
}

// dueNow deja listos para entregar los mensajes en cola (la base decide con su propio now()).
func dueNow(t *testing.T) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `UPDATE messages SET next_attempt_at = now() - interval '1 second', locked_until = NULL
		WHERE direction = 'outbound' AND status = 'queued'`)
	must(t, err)
}

func msgState(t *testing.T, id string) string {
	t.Helper()
	return str(t, `SELECT status || '|' || attempts || '|' || coalesce(error_code, '-') || '|' || coalesce(wamid, '-') FROM messages WHERE id = $1`, id)
}

func TestEnviarUnMensaje(t *testing.T) {
	t.Run("nace en cola, lo entrega el trabajador y queda enviado con su wamid", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		conv := w.conv("573001110001")
		callsBefore := w.h.meta.callCount()

		rec := w.send(w.agent, conv, "Hola Ana,\n¿qué te sirvo?")
		want(t, rec, http.StatusAccepted)
		m := jsonMap(t, rec)
		id := m["id"].(string)
		if m["status"] != "queued" || m["direction"] != "outbound" || m["sender_user_id"] != w.agent.ID.String() || m["wamid"] != nil {
			t.Fatalf("mensaje = %v", m)
		}
		if w.h.meta.callCount() != callsBefore {
			t.Fatal("responder no debe hablar con Meta: eso lo hace el trabajador")
		}
		// La conversación ya refleja la respuesta.
		if got := str(t, `SELECT last_message_direction || '|' || last_message_preview || '|' || coalesce(waiting_since::text, '-') || '|' || (first_response_at IS NOT NULL)::text || '|' || unread_count FROM conversations WHERE id = $1`, conv); got != "outbound|Hola Ana, ¿qué te sirvo?|-|true|1" {
			t.Errorf("conversación = %q", got)
		}

		w.h.dispatchAll()
		sent := w.h.meta.sentMessages()
		if len(sent) != 1 {
			t.Fatalf("envíos a Meta = %d", len(sent))
		}
		if s := sent[0]; s.Token != w.c.Token || s.PhoneNumberID != w.c.PhoneNumberID || s.To != "573001110001" || s.Body != "Hola Ana,\n¿qué te sirvo?" {
			t.Fatalf("envío = %+v", s)
		}
		if got := msgState(t, id); got != "sent|1|-|wamid.OUT0001" {
			t.Fatalf("mensaje = %q", got)
		}
		if n := count(t, `SELECT count(*) FROM messages WHERE id = $1 AND sent_at IS NOT NULL AND locked_until IS NULL AND next_attempt_at IS NULL`, id); n != 1 {
			t.Error("el mensaje enviado debía quedar sin arrendamiento ni reintento pendiente")
		}
		w.h.dispatchAll()
		if len(w.h.meta.sentMessages()) != 1 {
			t.Fatal("un mensaje ya enviado se envió otra vez")
		}
		if strings.Contains(w.h.logs.String(), w.c.Token) {
			t.Fatal("el token apareció en el log")
		}
	})

	t.Run("valida el texto y a qué responde", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		w.in("573001110002", "Beto", "b1", "hola", time.Minute)
		conv, other := w.conv("573001110001"), w.conv("573001110002")
		w.send(w.agent, other, "para Beto")
		foreign := str(t, `SELECT id::text FROM messages WHERE direction = 'outbound'`)

		for name, body := range map[string]string{"vacío": "", "en blanco": "  \n ", "demasiado largo": strings.Repeat("a", 4097)} {
			t.Run(name, func(t *testing.T) { want(t, w.send(w.agent, conv, body), http.StatusUnprocessableEntity) })
		}
		want(t, w.h.do(http.MethodPost, w.url("/"+conv+"/messages"), map[string]any{"body": "x", "reply_to_message_id": foreign}, &w.agent), http.StatusUnprocessableEntity)
		want(t, w.h.do(http.MethodPost, w.url("/"+conv+"/messages"), map[string]any{"body": "x", "reply_to_message_id": uuid.NewString()}, &w.agent), http.StatusUnprocessableEntity)
		want(t, w.h.do(http.MethodPost, w.url("/"+conv+"/messages"), map[string]any{"body": "x", "reply_to_message_id": "no-uuid"}, &w.agent), http.StatusBadRequest)
		if n := count(t, `SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound'`, conv); n != 0 {
			t.Fatalf("un envío inválido dejó %d mensajes en cola", n)
		}
		want(t, w.send(w.agent, conv, strings.Repeat("é", 4096)), http.StatusAccepted) // el límite exacto sí
	})

	t.Run("responder a un mensaje del cliente lo enlaza y Meta recibe el contexto", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "wamid.CLIENTE1", "quiero una pizza", time.Minute)
		conv := w.conv("573001110001")
		inMsg := str(t, `SELECT id::text FROM messages WHERE wamid = 'wamid.CLIENTE1'`)
		rec := w.h.do(http.MethodPost, w.url("/"+conv+"/messages"), map[string]any{"body": "claro", "reply_to_message_id": inMsg}, &w.agent)
		want(t, rec, http.StatusAccepted)
		if jsonMap(t, rec)["reply_to_message_id"] != inMsg {
			t.Fatalf("mensaje = %v", jsonMap(t, rec))
		}
		w.h.dispatchAll()
		if got := w.h.meta.sentMessages()[0].ReplyTo; got != "wamid.CLIENTE1" {
			t.Fatalf("context.message_id = %q", got)
		}
	})

	t.Run("no se puede responder fuera de la ventana, a un bloqueado ni por una bandeja desconectada: 409 y nada en cola", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", 25*time.Hour)
		w.in("573001110002", "Beto", "b1", "hola", time.Minute)
		w.in("573001110003", "Carla", "c1", "hola", time.Minute)
		_, err := pool.Exec(t.Context(), `UPDATE contacts SET blocked = true WHERE phone_e164 = '+573001110002'`)
		must(t, err)

		for name, tc := range map[string]struct{ phone, reason string }{
			"ventana cerrada": {"573001110001", "window_closed"}, "contacto bloqueado": {"573001110002", "contact_blocked"},
		} {
			t.Run(name, func(t *testing.T) {
				rec := w.send(w.agent, w.conv(tc.phone), "hola")
				want(t, rec, http.StatusConflict)
				if d, _ := jsonMap(t, rec)["detail"].(string); !strings.HasPrefix(d, tc.reason+":") {
					t.Fatalf("detail = %q", d)
				}
			})
		}
		want(t, w.h.do(http.MethodDelete, orgURL(w.o, "/inboxes/"+w.c.Inbox), nil, &w.admin), http.StatusNoContent)
		rec := w.send(w.agent, w.conv("573001110003"), "hola")
		want(t, rec, http.StatusConflict)
		if d, _ := jsonMap(t, rec)["detail"].(string); !strings.HasPrefix(d, "inbox_disconnected:") {
			t.Fatalf("detail = %q", d)
		}
		if n := count(t, `SELECT count(*) FROM messages WHERE direction = 'outbound'`); n != 0 {
			t.Fatalf("se encolaron %d mensajes que no podían enviarse", n)
		}
	})

	t.Run("el plan manda: sin la función del inbox no se responde (402)", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		sa := w.h.user(superadmin())
		want(t, w.h.do(http.MethodPut, platformOrgURL(w.o, "/feature-overrides/inbox_24h"), map[string]any{"enabled": false}, &sa), http.StatusOK)
		want(t, w.send(w.agent, w.conv("573001110001"), "hola"), http.StatusPaymentRequired)
	})
}

func TestOrdenDeEntregaPorConversacion(t *testing.T) {
	t.Run("los mensajes de una conversación salen en orden y de a uno por vuelta", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		conv := w.conv("573001110001")
		for _, b := range []string{"uno", "dos", "tres"} {
			want(t, w.send(w.agent, conv, b), http.StatusAccepted)
		}
		for i := range 3 {
			n, err := w.h.workers.WhatsApp.RunOnce(t.Context())
			must(t, err)
			if n != 1 {
				t.Fatalf("vuelta %d: reclamó %d mensajes, quería 1 (solo el más viejo de la conversación)", i+1, n)
			}
		}
		var got []string
		for _, s := range w.h.meta.sentMessages() {
			got = append(got, s.Body)
		}
		if strings.Join(got, ",") != "uno,dos,tres" {
			t.Fatalf("orden de entrega = %v", got)
		}
	})

	t.Run("si el primero espera un reintento, los siguientes esperan con él", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		conv := w.conv("573001110001")
		w.h.meta.reply(scriptedReply{status: 500, body: `{"error":{"message":"caído","code":131000}}`})
		w.send(w.agent, conv, "uno")
		w.send(w.agent, conv, "dos")
		w.h.dispatchAll() // el primero falla de forma transitoria y se reprograma en el futuro
		if n := len(w.h.meta.sentMessages()); n != 1 {
			t.Fatalf("envíos = %d: el segundo se adelantó al primero", n)
		}
		dueNow(t)
		w.h.dispatchAll()
		var got []string
		for _, s := range w.h.meta.sentMessages() {
			got = append(got, s.Body)
		}
		if strings.Join(got, ",") != "uno,uno,dos" { // 1.º intento fallido, reintento, luego el segundo
			t.Fatalf("orden = %v", got)
		}
	})

	t.Run("conversaciones distintas avanzan en la misma vuelta", func(t *testing.T) {
		w := newWA(t)
		for i := range 3 {
			w.in("57300111000"+str2(int64(i)), "C", "w"+str2(int64(i)), "hola", time.Minute)
			w.send(w.agent, w.conv("57300111000"+str2(int64(i))), "para "+str2(int64(i)))
		}
		n, err := w.h.workers.WhatsApp.RunOnce(t.Context())
		must(t, err)
		if n != 3 {
			t.Fatalf("reclamó %d, quería 3 (una por conversación)", n)
		}
	})
}

func TestFallosDeEntrega(t *testing.T) {
	// enqueue deja un mensaje en cola y devuelve su id.
	enqueue := func(t *testing.T) (waSetup, string) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		rec := w.send(w.agent, w.conv("573001110001"), "pedido listo")
		want(t, rec, http.StatusAccepted)
		return w, jsonMap(t, rec)["id"].(string)
	}
	apiErr := func(status, code int, msg string) scriptedReply {
		return scriptedReply{status: status, body: `{"error":{"message":"` + msg + `","code":` + str2(int64(code)) + `}}`}
	}

	t.Run("un fallo transitorio se reintenta con espera y termina enviándose UNA vez", func(t *testing.T) {
		for name, r := range map[string]scriptedReply{
			"caída de Meta (500)": apiErr(500, 131000, "Internal error"), "límite de ritmo del número": apiErr(429, 130429, "Throughput limit"),
			"servicio no disponible": apiErr(503, 131016, "Service unavailable"),
		} {
			t.Run(name, func(t *testing.T) {
				w, id := enqueue(t)
				w.h.meta.reply(r)
				w.h.dispatchAll()
				if got := msgState(t, id); !strings.HasPrefix(got, "queued|1|") {
					t.Fatalf("tras el fallo = %q, quería queued con 1 intento", got)
				}
				if n := count(t, `SELECT count(*) FROM messages WHERE id = $1 AND next_attempt_at > now() AND locked_until IS NULL`, id); n != 1 {
					t.Fatal("el reintento debía quedar programado en el futuro, sin arrendamiento")
				}
				w.h.dispatchAll() // todavía no toca: no se intenta antes de tiempo
				if len(w.h.meta.sentMessages()) != 1 {
					t.Fatal("se reintentó antes de tiempo")
				}
				dueNow(t)
				w.h.dispatchAll()
				if got := msgState(t, id); got != "sent|2|-|wamid.OUT0002" {
					t.Fatalf("tras el reintento = %q", got)
				}
			})
		}
	})

	t.Run("un rechazo que no mejora con el tiempo falla YA y no se reintenta", func(t *testing.T) {
		for name, tc := range map[string]struct {
			r    scriptedReply
			code string
		}{
			"ventana cerrada en Meta":   {apiErr(400, 131047, "Re-engagement message"), "131047"},
			"destinatario sin WhatsApp": {apiErr(400, 131026, "Message undeliverable"), "131026"},
			"parámetro inválido":        {apiErr(400, 100, "Invalid parameter"), "100"},
		} {
			t.Run(name, func(t *testing.T) {
				w, id := enqueue(t)
				w.h.meta.reply(tc.r)
				w.h.dispatchAll()
				if got := msgState(t, id); got != "failed|1|"+tc.code+"|-" {
					t.Fatalf("mensaje = %q", got)
				}
				dueNow(t)
				w.h.dispatchAll()
				if len(w.h.meta.sentMessages()) != 1 {
					t.Fatal("un rechazo permanente se reintentó")
				}
				if n := count(t, `SELECT count(*) FROM messages WHERE id = $1 AND failed_at IS NOT NULL AND error_detail <> ''`, id); n != 1 {
					t.Error("el fallo debía guardar hora y motivo")
				}
			})
		}
	})

	t.Run("se agotan los reintentos: falla con retries_exhausted tras 6 intentos exactos", func(t *testing.T) {
		w, id := enqueue(t)
		for range 10 {
			w.h.meta.reply(apiErr(500, 131000, "caído"))
		}
		for range 8 {
			dueNow(t)
			w.h.dispatchAll()
		}
		if got := msgState(t, id); got != "failed|6|retries_exhausted|-" {
			t.Fatalf("mensaje = %q", got)
		}
		if n := len(w.h.meta.sentMessages()); n != 6 {
			t.Fatalf("intentos a Meta = %d, quería 6", n)
		}
	})

	t.Run("un fallo AMBIGUO (Meta pudo haberlo recibido) NO se reintenta: reenviar duplicaría el mensaje", func(t *testing.T) {
		w, id := enqueue(t)
		w.h.meta.reply(scriptedReply{drop: true}) // la conexión se corta después de recibir la petición
		w.h.dispatchAll()
		if got := msgState(t, id); got != "failed|1|delivery_unknown|-" {
			t.Fatalf("mensaje = %q", got)
		}
		dueNow(t)
		w.h.dispatchAll()
		if n := len(w.h.meta.sentMessages()); n != 1 {
			t.Fatalf("Meta recibió %d envíos: un mensaje ambiguo se reintentó y el cliente lo recibiría dos veces", n)
		}
	})

	t.Run("si nunca se pudo conectar sí es seguro reintentar", func(t *testing.T) {
		w, id := enqueue(t)
		w.h.meta.srv.Close() // Meta no responde: la conexión ni siquiera se abre
		w.h.dispatchAll()
		if got := msgState(t, id); !strings.HasPrefix(got, "queued|1|network|") {
			t.Fatalf("mensaje = %q, quería queued con error network", got)
		}
	})

	t.Run("el token rechazado marca el canal y los mensajes siguientes fallan sin llamar a Meta", func(t *testing.T) {
		w, id := enqueue(t)
		second := jsonMap(t, w.send(w.agent, w.conv("573001110001"), "segundo"))["id"].(string)
		w.h.meta.reply(apiErr(401, 190, "Error validating access token: token de EAAG-secreto"))
		w.h.dispatchAll()
		if got := msgState(t, id); !strings.HasPrefix(got, "failed|1|190|") {
			t.Fatalf("primero = %q", got)
		}
		if got := str(t, `SELECT status FROM whatsapp_channels`); got != "needs_reauth" {
			t.Fatalf("canal = %q", got)
		}
		if got := msgState(t, second); !strings.HasPrefix(got, "failed|") || !strings.Contains(got, "inbox_disconnected") {
			t.Fatalf("segundo = %q, quería fallar por bandeja desconectada", got)
		}
		if n := len(w.h.meta.sentMessages()); n != 1 {
			t.Fatalf("Meta recibió %d envíos: no se debe seguir enviando con un token rechazado", n)
		}
		// La interfaz lo sabe antes de que alguien lo intente.
		want(t, w.send(w.agent, w.conv("573001110001"), "tercero"), http.StatusConflict)
		// Cargar un token nuevo lo reactiva.
		w.h.meta.setToken(w.c.WABA, "EAAG-nuevo")
		want(t, w.h.do(http.MethodPut, orgURL(w.o, "/inboxes/"+w.c.Inbox+"/credentials"), map[string]any{"access_token": "EAAG-nuevo"}, &w.admin), http.StatusOK)
		want(t, w.send(w.agent, w.conv("573001110001"), "cuarto"), http.StatusAccepted)
		w.h.dispatchAll()
		if got := w.h.meta.sentMessages(); got[len(got)-1].Token != "EAAG-nuevo" {
			t.Fatalf("el envío no usó el token nuevo: %+v", got[len(got)-1])
		}
		if strings.Contains(w.h.logs.String(), w.c.Token) || strings.Contains(w.h.logs.String(), "EAAG-nuevo") {
			t.Fatal("un token apareció en el log")
		}
	})

	t.Run("una bandeja desconectada con mensajes en cola los falla sin llamar a Meta", func(t *testing.T) {
		w, id := enqueue(t)
		want(t, w.h.do(http.MethodDelete, orgURL(w.o, "/inboxes/"+w.c.Inbox), nil, &w.admin), http.StatusNoContent)
		callsBefore := w.h.meta.callCount()
		w.h.dispatchAll()
		if got := msgState(t, id); !strings.Contains(got, "inbox_disconnected") || !strings.HasPrefix(got, "failed|") {
			t.Fatalf("mensaje = %q", got)
		}
		if w.h.meta.callCount() != callsBefore {
			t.Fatal("se llamó a Meta por una bandeja desconectada")
		}
	})
}

func TestArrendamientoYConcurrencia(t *testing.T) {
	t.Run("un mensaje con arrendamiento vigente no se reclama; vencido, sí (un trabajador que murió)", func(t *testing.T) {
		w := newWA(t)
		w.in("573001110001", "Ana", "a1", "hola", time.Minute)
		rec := w.send(w.agent, w.conv("573001110001"), "hola")
		id := jsonMap(t, rec)["id"].(string)
		_, err := pool.Exec(t.Context(), `UPDATE messages SET locked_until = now() + interval '1 hour', attempts = 1 WHERE id = $1`, id)
		must(t, err)
		if n, _ := w.h.workers.WhatsApp.RunOnce(t.Context()); n != 0 {
			t.Fatalf("reclamó %d mensajes con el arrendamiento vigente", n)
		}
		_, err = pool.Exec(t.Context(), `UPDATE messages SET locked_until = now() - interval '1 second' WHERE id = $1`, id)
		must(t, err)
		w.h.dispatchAll()
		if got := msgState(t, id); got != "sent|2|-|wamid.OUT0001" {
			t.Fatalf("mensaje = %q", got)
		}
	})

	t.Run("varios trabajadores a la vez: cada mensaje sale exactamente una vez", func(t *testing.T) {
		w := newWA(t)
		const total = 30
		for i := range total {
			phone := "5730011" + str2(int64(10000+i))
			w.in(phone, "C"+str2(int64(i)), "w"+str2(int64(i)), "hola", time.Minute)
			want(t, w.send(w.agent, w.conv(phone), "mensaje "+str2(int64(i))), http.StatusAccepted)
		}
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 6 {
					if _, err := w.h.workers.WhatsApp.RunOnce(t.Context()); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		w.h.dispatchAll()

		seen := map[string]int{}
		for _, s := range w.h.meta.sentMessages() {
			seen[s.Body]++
		}
		if len(seen) != total {
			t.Fatalf("mensajes distintos entregados = %d, quería %d", len(seen), total)
		}
		for body, n := range seen {
			if n != 1 {
				t.Errorf("%q se envió %d veces", body, n)
			}
		}
		if n := count(t, `SELECT count(*) FROM messages WHERE direction = 'outbound' AND status = 'sent'`); n != total {
			t.Fatalf("enviados en la base = %d", n)
		}
	})

	t.Run("Wake no bloquea aunque nadie escuche", func(t *testing.T) {
		w := newWA(t)
		done := make(chan struct{})
		go func() {
			for range 1000 {
				w.h.workers.WhatsApp.Wake()
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Wake se bloqueó")
		}
	})
}
