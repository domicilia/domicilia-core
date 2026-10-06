package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/config"
)

// Bandejas de WhatsApp: conectar un número, guardar su token cifrado, y que nada de eso cruce entre organizaciones.

func inboxesURL(o organization, suffix string) string { return orgURL(o, "/inboxes"+suffix) }

func connectBody(waba, phone, token, name string) map[string]any {
	return map[string]any{"name": name, "waba_id": waba, "phone_number_id": phone, "access_token": token}
}

func TestConectarUnNumeroDeWhatsApp(t *testing.T) {
	t.Run("conecta contra Meta, devuelve los datos del número y NUNCA el token", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.meta.addAccount("200001", "EAAG-token-secreto-de-acme", [2]string{"100001", "+57 300 111 1111"})

		rec := h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody("200001", "100001", "EAAG-token-secreto-de-acme", "Pedidos"), &admin)
		want(t, rec, http.StatusCreated)
		body := jsonMap(t, rec)
		wa := body["whatsapp"].(map[string]any)
		if body["name"] != "Pedidos" || wa["phone_number_id"] != "100001" || wa["display_phone"] != "+57 300 111 1111" ||
			wa["quality_rating"] != "GREEN" || wa["messaging_tier"] != "TIER_1K" || wa["status"] != "connected" || wa["waba_id"] != "200001" {
			t.Fatalf("bandeja = %v", body)
		}
		if strings.Contains(rec.Body.String(), "EAAG") || strings.Contains(rec.Body.String(), "access_token") {
			t.Fatalf("la respuesta contiene el token: %s", rec.Body.String())
		}
		// El token solo viajó a Meta por la cabecera Authorization.
		if calls := h.meta.callCount(); calls != 1 {
			t.Errorf("llamadas a Meta = %d", calls)
		}
	})

	t.Run("el token se guarda CIFRADO y atado a su cuenta; nada lo deja en claro", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "Pedidos")

		enc := str(t, `SELECT access_token_enc FROM whatsapp_accounts WHERE organization_id = $1`, o.ID)
		if strings.Contains(enc, c.Token) || !strings.HasPrefix(enc, "v1:") {
			t.Fatalf("token guardado = %q", enc)
		}
		// En ninguna columna de ninguna tabla relacionada, ni en la auditoría, ni en el log.
		for _, q := range []string{
			`SELECT count(*) FROM whatsapp_accounts a WHERE strpos(a::text, $1) > 0`,
			`SELECT count(*) FROM whatsapp_channels c WHERE strpos(c::text, $1) > 0`,
			`SELECT count(*) FROM inboxes i WHERE strpos(i::text, $1) > 0`,
			`SELECT count(*) FROM audit_log WHERE strpos(detail::text, $1) > 0`,
		} {
			if n := count(t, q, c.Token); n != 0 {
				t.Errorf("el token en claro aparece en la base: %s", q)
			}
		}
		if strings.Contains(h.logs.String(), c.Token) {
			t.Error("el token en claro aparece en el log")
		}
		if !contains(auditActions(t, o), "inbox.connected") {
			t.Errorf("auditoría = %v", auditActions(t, o))
		}
	})

	t.Run("un token distinto por cuenta: el cifrado de una no abre la otra", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		ca, cb := h.connect(adminA, a, "A"), h.connect(adminB, b, "B")

		// Alguien con acceso a la base copia el token cifrado de A sobre la cuenta de B.
		_, err := pool.Exec(t.Context(), `UPDATE whatsapp_accounts SET access_token_enc =
			(SELECT access_token_enc FROM whatsapp_accounts WHERE waba_id = $1) WHERE waba_id = $2`, ca.WABA, cb.WABA)
		must(t, err)
		// El contexto (id de cuenta) no coincide: no se descifra y el servidor responde con un error genérico.
		rec := h.do(http.MethodPost, inboxesURL(b, "/"+cb.Inbox+"/refresh"), nil, &adminB)
		want(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), ca.Token) || strings.Contains(rec.Body.String(), cb.Token) {
			t.Fatal("la respuesta de error filtra un token")
		}
	})

	t.Run("valida los datos y Meta, y no guarda nada si algo falla", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.meta.addAccount("200001", "token-bueno", [2]string{"100001", "+57 300 111 1111"})
		url := inboxesURL(o, "/whatsapp")
		for name, body := range map[string]map[string]any{
			"sin nombre":                   connectBody("200001", "100001", "token-bueno", ""),
			"nombre demasiado largo":       connectBody("200001", "100001", "token-bueno", strings.Repeat("a", 101)),
			"waba_id no numérico":          connectBody("../x", "100001", "token-bueno", "P"),
			"phone_number_id no numérico":  connectBody("200001", "10;drop", "token-bueno", "P"),
			"waba_id demasiado corto":      connectBody("12", "100001", "token-bueno", "P"),
			"sin token":                    connectBody("200001", "100001", "", "P"),
			"token con espacios":           connectBody("200001", "100001", "token bueno", "P"),
			"token con salto de línea":     connectBody("200001", "100001", "token\nbueno", "P"),
			"token demasiado largo":        connectBody("200001", "100001", strings.Repeat("t", 1025), "P"),
			"Meta rechaza el token":        connectBody("200001", "100001", "token-malo", "P"),
			"cuenta inexistente en Meta":   connectBody("9999999", "100001", "token-bueno", "P"),
			"el número no es de la cuenta": connectBody("200001", "7777777", "token-bueno", "P"),
		} {
			t.Run(name, func(t *testing.T) {
				want(t, h.do(http.MethodPost, url, body, &admin), http.StatusUnprocessableEntity)
			})
		}
		for _, table := range []string{"inboxes", "whatsapp_accounts", "whatsapp_channels"} {
			if n := count(t, `SELECT count(*) FROM `+table); n != 0 {
				t.Errorf("%s = %d: una conexión rechazada dejó filas", table, n)
			}
		}
	})

	t.Run("el mensaje de Meta al rechazar un token no incluye el token", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.meta.addAccount("200001", "token-bueno", [2]string{"100001", "+57 300 111 1111"})
		rec := h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody("200001", "100001", "EAAG-token-erroneo-12345", "P"), &admin)
		want(t, rec, http.StatusUnprocessableEntity)
		if strings.Contains(rec.Body.String(), "EAAG-token-erroneo") || strings.Contains(h.logs.String(), "EAAG-token-erroneo") {
			t.Fatal("el token erróneo apareció en la respuesta o en el log")
		}
	})

	t.Run("si Meta está caído responde 502 y no guarda nada", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.meta.srv.Close()
		want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody("200001", "100001", "t-ok", "P"), &admin), http.StatusBadGateway)
		if n := count(t, `SELECT count(*) FROM inboxes`); n != 0 {
			t.Fatalf("inboxes = %d", n)
		}
	})

	t.Run("sin llave de cifrado en el servidor responde 503 y no guarda el token en claro", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.SecretsKey = "" })
		admin, o := orgAdmin(h, "Acme")
		h.meta.addAccount("200001", "t-ok", [2]string{"100001", "+57 300 111 1111"})
		want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody("200001", "100001", "t-ok", "P"), &admin), http.StatusServiceUnavailable)
		if n := count(t, `SELECT count(*) FROM whatsapp_accounts`); n != 0 {
			t.Fatalf("cuentas = %d", n)
		}
	})
}

func TestUnNumeroYUnaCuentaSoloPertenecenAUnaOrganizacion(t *testing.T) {
	h := newHarness(t)
	adminA, a := orgAdmin(h, "Org A")
	adminB, b := orgAdmin(h, "Org B")
	h.meta.addAccount("200001", "token-a", [2]string{"100001", "+57 300 111 1111"}, [2]string{"100002", "+57 300 111 2222"})
	want(t, h.do(http.MethodPost, inboxesURL(a, "/whatsapp"), connectBody("200001", "100001", "token-a", "A"), &adminA), http.StatusCreated)

	// B intenta conectar el MISMO número, y otro número de la MISMA cuenta, con un token que sí funciona.
	for name, body := range map[string]map[string]any{
		"el mismo número":                connectBody("200001", "100001", "token-a", "B1"),
		"otro número de la misma cuenta": connectBody("200001", "100002", "token-a", "B2"),
	} {
		t.Run(name, func(t *testing.T) {
			want(t, h.do(http.MethodPost, inboxesURL(b, "/whatsapp"), body, &adminB), http.StatusConflict)
		})
	}
	if n := count(t, `SELECT count(*) FROM whatsapp_accounts WHERE organization_id = $1`, b.ID); n != 0 {
		t.Fatalf("B quedó con %d cuentas", n)
	}
	if n := count(t, `SELECT count(*) FROM whatsapp_accounts WHERE organization_id = $1`, a.ID); n != 1 {
		t.Fatalf("A perdió su cuenta: %d", n)
	}
}

func TestQuienPuedeAdministrarLasBandejas(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())
	admin, o := orgAdmin(h, "Acme")
	empleado, extrano := h.user(), h.user()
	h.join(empleado, o, "employee")
	h.meta.addAccount("200001", "t", [2]string{"100001", "+57 300 111 1111"})
	body := connectBody("200001", "100001", "t", "P")

	want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), body, &empleado), http.StatusForbidden)
	want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), body, &extrano), http.StatusForbidden)
	if n := count(t, `SELECT count(*) FROM inboxes`); n != 0 {
		t.Fatalf("una conexión rechazada creó %d bandejas", n)
	}
	// Un rol a medida con org.inbox.manage no se puede fabricar: no es delegable.
	want(t, h.do(http.MethodPost, orgRolesURL(o), roleReq("conector", "Conector", "org.inbox.manage"), &admin), http.StatusForbidden)

	want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), body, &admin), http.StatusCreated)

	// El superadmin también puede gestionar a nombre de un negocio.
	h.meta.addAccount("200002", "t2", [2]string{"100002", "+57 300 222 2222"})
	h2, o2 := orgAdmin(h, "Otra")
	_ = h2
	want(t, h.do(http.MethodPost, inboxesURL(o2, "/whatsapp"), connectBody("200002", "100002", "t2", "Q"), &sa), http.StatusCreated)
}

func TestListarYLeerBandejas(t *testing.T) {
	t.Run("solo lista las de su organización y quien puede atender las ve", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		empleado, extrano := h.user(), h.user()
		h.join(empleado, a, "employee")
		ca, cb := h.connect(adminA, a, "Bandeja A"), h.connect(adminB, b, "Bandeja B")

		list := decode[[]map[string]any](t, h.do(http.MethodGet, inboxesURL(a, ""), nil, &empleado))
		if len(list) != 1 || list[0]["id"] != ca.Inbox {
			t.Fatalf("lista de A = %v", list)
		}
		want(t, h.do(http.MethodGet, inboxesURL(a, ""), nil, &extrano), http.StatusForbidden)
		want(t, h.do(http.MethodGet, inboxesURL(b, ""), nil, &adminA), http.StatusForbidden)

		// El id de una bandeja de B usado en la ruta de A no existe para A.
		want(t, h.do(http.MethodGet, inboxesURL(a, "/"+cb.Inbox), nil, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodPatch, inboxesURL(a, "/"+cb.Inbox), map[string]any{"name": "Robada"}, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodDelete, inboxesURL(a, "/"+cb.Inbox), nil, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodPut, inboxesURL(a, "/"+cb.Inbox+"/credentials"), map[string]any{"access_token": "x"}, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodPost, inboxesURL(a, "/"+cb.Inbox+"/refresh"), nil, &adminA), http.StatusNotFound)
		if got := str(t, `SELECT name FROM inboxes WHERE id = $1`, cb.Inbox); got != "Bandeja B" {
			t.Fatalf("la bandeja de B se modificó: %q", got)
		}
		want(t, h.do(http.MethodGet, inboxesURL(a, "/"+uuid.NewString()), nil, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodGet, inboxesURL(a, "/no-es-uuid"), nil, &adminA), http.StatusUnprocessableEntity)
	})

	t.Run("el empleado no administra: no renombra, no desconecta, no cambia el token", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		empleado := h.user()
		h.join(empleado, o, "employee")
		c := h.connect(admin, o, "P")
		want(t, h.do(http.MethodGet, inboxesURL(o, "/"+c.Inbox), nil, &empleado), http.StatusOK)
		want(t, h.do(http.MethodPatch, inboxesURL(o, "/"+c.Inbox), map[string]any{"name": "X"}, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodDelete, inboxesURL(o, "/"+c.Inbox), nil, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodPut, inboxesURL(o, "/"+c.Inbox+"/credentials"), map[string]any{"access_token": "x"}, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodPost, inboxesURL(o, "/"+c.Inbox+"/refresh"), nil, &empleado), http.StatusForbidden)
	})

	t.Run("una organización suspendida no opera sus bandejas", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, h.do(http.MethodGet, inboxesURL(o, ""), nil, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodPatch, inboxesURL(o, "/"+c.Inbox), map[string]any{"name": "X"}, &admin), http.StatusForbidden)
		want(t, h.do(http.MethodGet, inboxesURL(o, ""), nil, &sa), http.StatusOK)
	})
}

func TestPlanYBandejas(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	h.setPlan(o, "starter")
	h.meta.addAccount("200001", "t1", [2]string{"100001", "+57 300 111 1111"})
	h.meta.addAccount("200002", "t2", [2]string{"100002", "+57 300 222 2222"})

	// La primera bandeja es del plan base.
	want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody("200001", "100001", "t1", "Uno"), &admin), http.StatusCreated)
	// Tener más de un número es de Enterprise: 402, y no se guarda nada.
	want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody("200002", "100002", "t2", "Dos"), &admin), http.StatusPaymentRequired)
	if n := count(t, `SELECT count(*) FROM inboxes WHERE organization_id = $1`, o.ID); n != 1 {
		t.Fatalf("bandejas = %d", n)
	}
	h.setPlan(o, "enterprise")
	want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody("200002", "100002", "t2", "Dos"), &admin), http.StatusCreated)
	// Las excepciones por función también valen: starter + excepción multi_inbox.
	h2, o2 := orgAdmin(h, "Otra")
	h.setPlan(o2, "starter")
	sa := h.user(superadmin())
	want(t, h.do(http.MethodPut, platformOrgURL(o2, "/feature-overrides/multi_inbox"), map[string]any{"enabled": true}, &sa), http.StatusOK)
	h.meta.addAccount("200003", "t3", [2]string{"100003", "+57 300 333 3333"})
	h.meta.addAccount("200004", "t4", [2]string{"100004", "+57 300 444 4444"})
	want(t, h.do(http.MethodPost, inboxesURL(o2, "/whatsapp"), connectBody("200003", "100003", "t3", "A"), &h2), http.StatusCreated)
	want(t, h.do(http.MethodPost, inboxesURL(o2, "/whatsapp"), connectBody("200004", "100004", "t4", "B"), &h2), http.StatusCreated)
}

func TestRenombrarYDesconectarUnaBandeja(t *testing.T) {
	t.Run("renombra y no admite dos con el mismo nombre", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.setPlan(o, "enterprise")
		c1, c2 := h.connect(admin, o, "Pedidos"), h.connect(admin, o, "Soporte")
		rec := h.do(http.MethodPatch, inboxesURL(o, "/"+c1.Inbox), map[string]any{"name": "  Ventas  "}, &admin)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["name"] != "Ventas" {
			t.Fatalf("bandeja = %v", jsonMap(t, rec))
		}
		want(t, h.do(http.MethodPatch, inboxesURL(o, "/"+c2.Inbox), map[string]any{"name": "VENTAS"}, &admin), http.StatusConflict)
		want(t, h.do(http.MethodPatch, inboxesURL(o, "/"+c2.Inbox), map[string]any{"name": ""}, &admin), http.StatusUnprocessableEntity)
		if !contains(auditActions(t, o), "inbox.renamed") {
			t.Error("renombrar debía auditarse")
		}
	})

	t.Run("desconectar libera el número, conserva las conversaciones y se puede reconectar", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "Pedidos")
		want(t, h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", Name: "Ana", ID: "wamid.IN1", Body: "hola"}), http.StatusOK)

		want(t, h.do(http.MethodDelete, inboxesURL(o, "/"+c.Inbox), nil, &admin), http.StatusNoContent)
		want(t, h.do(http.MethodDelete, inboxesURL(o, "/"+c.Inbox), nil, &admin), http.StatusNotFound) // ya archivada

		if got := decode[[]map[string]any](t, h.do(http.MethodGet, inboxesURL(o, ""), nil, &admin)); len(got) != 0 {
			t.Fatalf("la archivada sigue en el listado: %v", got)
		}
		archived := decode[[]map[string]any](t, h.do(http.MethodGet, inboxesURL(o, "?include_archived=true"), nil, &admin))
		if len(archived) != 1 || archived[0]["archived_at"] == nil || archived[0]["whatsapp"] != nil {
			t.Fatalf("archivadas = %v", archived)
		}
		// El número y la cuenta quedaron libres; las conversaciones se conservan.
		if n := count(t, `SELECT count(*) FROM whatsapp_channels`); n != 0 {
			t.Errorf("canales = %d", n)
		}
		if n := count(t, `SELECT count(*) FROM whatsapp_accounts`); n != 0 {
			t.Errorf("cuentas huérfanas = %d", n)
		}
		if n := count(t, `SELECT count(*) FROM conversations WHERE organization_id = $1`, o.ID); n != 1 {
			t.Errorf("conversaciones = %d", n)
		}
		// Ya no recibe mensajes por ese número.
		want(t, h.inbound(c.PhoneNumberID, waMsg{From: "573001112222", ID: "wamid.IN2", Body: "sigues ahí"}), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM messages WHERE wamid = 'wamid.IN2'`); n != 0 {
			t.Error("una bandeja desconectada siguió recibiendo mensajes")
		}
		// Se puede volver a conectar el mismo número.
		want(t, h.do(http.MethodPost, inboxesURL(o, "/whatsapp"), connectBody(c.WABA, c.PhoneNumberID, c.Token, "Pedidos"), &admin), http.StatusCreated)
		if !contains(auditActions(t, o), "inbox.archived") {
			t.Error("desconectar debía auditarse")
		}
	})
}

func TestCambiarElTokenDeUnaBandeja(t *testing.T) {
	t.Run("un token nuevo válido se guarda cifrado, reactiva el canal y no se devuelve", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		before := str(t, `SELECT access_token_enc FROM whatsapp_accounts WHERE waba_id = $1`, c.WABA)

		h.meta.revokeToken(c.WABA)
		rec := h.do(http.MethodPost, inboxesURL(o, "/"+c.Inbox+"/refresh"), nil, &admin)
		want(t, rec, http.StatusOK)
		if wa := jsonMap(t, rec)["whatsapp"].(map[string]any); wa["status"] != "needs_reauth" || wa["token_status"] != "invalid" {
			t.Fatalf("tras revocar el token: %v", wa)
		}

		h.meta.setToken(c.WABA, "EAAG-token-nuevo-9999")
		rec = h.do(http.MethodPut, inboxesURL(o, "/"+c.Inbox+"/credentials"), map[string]any{"access_token": "EAAG-token-nuevo-9999"}, &admin)
		want(t, rec, http.StatusOK)
		if wa := jsonMap(t, rec)["whatsapp"].(map[string]any); wa["status"] != "connected" || wa["token_status"] != "valid" {
			t.Fatalf("tras cargar el nuevo: %v", wa)
		}
		if strings.Contains(rec.Body.String(), "EAAG") {
			t.Fatal("la respuesta contiene el token")
		}
		after := str(t, `SELECT access_token_enc FROM whatsapp_accounts WHERE waba_id = $1`, c.WABA)
		if after == before || strings.Contains(after, "EAAG-token-nuevo") {
			t.Fatalf("el token no se rotó cifrado: %q", after)
		}
		if !contains(auditActions(t, o), "inbox.credentials_rotated") {
			t.Error("rotar el token debía auditarse")
		}
	})

	t.Run("un token que Meta rechaza, o que no ve el número, no reemplaza al bueno", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		before := str(t, `SELECT access_token_enc FROM whatsapp_accounts WHERE waba_id = $1`, c.WABA)
		url := inboxesURL(o, "/"+c.Inbox+"/credentials")
		for name, tok := range map[string]string{"rechazado por Meta": "otro-token", "vacío": "", "con espacios": "a b"} {
			t.Run(name, func(t *testing.T) {
				want(t, h.do(http.MethodPut, url, map[string]any{"access_token": tok}, &admin), http.StatusUnprocessableEntity)
			})
		}
		if after := str(t, `SELECT access_token_enc FROM whatsapp_accounts WHERE waba_id = $1`, c.WABA); after != before {
			t.Fatal("un token rechazado cambió el guardado")
		}
	})

	t.Run("refrescar trae la calidad y el nivel actuales del número", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		c := h.connect(admin, o, "P")
		h.meta.mu.Lock()
		h.meta.accounts[c.WABA].numbers[0]["quality_rating"] = "RED"
		h.meta.accounts[c.WABA].numbers[0]["messaging_limit_tier"] = "TIER_250"
		h.meta.mu.Unlock()
		rec := h.do(http.MethodPost, inboxesURL(o, "/"+c.Inbox+"/refresh"), nil, &admin)
		want(t, rec, http.StatusOK)
		if wa := jsonMap(t, rec)["whatsapp"].(map[string]any); wa["quality_rating"] != "RED" || wa["messaging_tier"] != "TIER_250" {
			t.Fatalf("número = %v", wa)
		}
	})
}
