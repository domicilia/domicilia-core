package app_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Invitaciones con token. El token es la credencial: solo su hash toca la base, sirve
// una vez, y aceptarlo exige que la sesión sea de la persona invitada.

// invite invita y devuelve la respuesta.
func invite(h *harness, as person, o organization, email, role string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, orgURL(o, "/invitations"), map[string]string{"email": email, "role": role}, &as)
}

// tokenOf extrae el token del enlace de una invitación recién emitida.
func tokenOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	link, _ := jsonMap(t, rec)["accept_url"].(string)
	return tokenFromLink(t, link)
}

func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("token") == "" {
		t.Fatalf("accept_url = %q: no trae token", link)
	}
	return u.Query().Get("token")
}

func acceptBody(token string) map[string]string { return map[string]string{"token": token} }

// invitee es alguien con sesión en GoTrue pero sin perfil de negocio.
func (h *harness) invitee(email string) person {
	h.t.Helper()
	return person{ID: h.authIdentity(), Email: email}
}

func TestEmitirInvitaciones(t *testing.T) {
	t.Run("un admin invita a un empleado y recibe el enlace", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := invite(h, admin, o, "  Nueva@Ejemplo.COM ", "employee")
		want(t, rec, http.StatusCreated)
		body := jsonMap(t, rec)
		if body["email"] != "nueva@ejemplo.com" || body["role"] != "employee" || body["organization_id"] != o.ID.String() {
			t.Fatalf("invitación = %v", body)
		}
		if link, _ := body["accept_url"].(string); !strings.HasPrefix(link, testAppURL+"/auth/accept-invite?token=") {
			t.Fatalf("accept_url = %q", link)
		}
		exp, err := time.Parse(time.RFC3339, body["expires_at"].(string))
		if err != nil || time.Until(exp) < 6*24*time.Hour || time.Until(exp) > 8*24*time.Hour {
			t.Errorf("expires_at = %v: la vigencia debía rondar los 7 días", body["expires_at"])
		}
		if acts := auditActions(t, o); !contains(acts, "invitation.created") {
			t.Errorf("auditoría = %v", acts)
		}
	})

	t.Run("el token no se guarda: solo su hash", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		// Ninguna columna de la fila contiene el token en claro.
		if n := count(t, `SELECT count(*) FROM organization_invitations i WHERE strpos(i::text, $1) > 0`, token); n != 0 {
			t.Fatal("el token en claro está guardado en la base")
		}
		if n := count(t, `SELECT count(*) FROM audit_log WHERE strpos(detail::text, $1) > 0`, token); n != 0 {
			t.Fatal("el token en claro está en la auditoría")
		}
		if got := str(t, `SELECT length(token_hash)::text FROM organization_invitations`); got != "32" {
			t.Errorf("largo del hash = %s, quería 32 (sha-256)", got)
		}
	})

	t.Run("el listado no trae el enlace ni el token", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, invite(h, admin, o, "a@ejemplo.com", "employee"), http.StatusCreated)
		rec := h.do(http.MethodGet, orgURL(o, "/invitations"), nil, &admin)
		want(t, rec, http.StatusOK)
		if strings.Contains(rec.Body.String(), "token") || strings.Contains(rec.Body.String(), "accept_url") {
			t.Fatalf("el listado no debe traer el token: %s", rec.Body.String())
		}
		if n := len(decode[[]map[string]any](t, rec)); n != 1 {
			t.Fatalf("invitaciones = %d", n)
		}
	})

	t.Run("quién puede invitar y a qué rol", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		empleado, extrano := h.user(), h.user()
		h.join(empleado, o, "employee")

		want(t, invite(h, empleado, o, "a@ejemplo.com", "employee"), http.StatusForbidden)
		want(t, invite(h, extrano, o, "a@ejemplo.com", "employee"), http.StatusForbidden)
		// Un admin de organización no nombra otro admin (no propaga el rol).
		want(t, invite(h, admin, o, "b@ejemplo.com", "admin"), http.StatusForbidden)
		// El superadmin sí.
		want(t, invite(h, sa, o, "b@ejemplo.com", "admin"), http.StatusCreated)
		if n := count(t, `SELECT count(*) FROM organization_invitations WHERE email = 'a@ejemplo.com'`); n != 0 {
			t.Fatal("se creó una invitación pese al 403")
		}
	})

	t.Run("valida el correo y el rol", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, invite(h, admin, o, "no-es-correo", "employee"), http.StatusUnprocessableEntity)
		want(t, invite(h, admin, o, "a@ejemplo.com", "dueño"), http.StatusUnprocessableEntity)
		want(t, invite(h, admin, o, "a@ejemplo.com", ""), http.StatusUnprocessableEntity)
		want(t, invite(h, admin, o, "a@localhost", "employee"), http.StatusUnprocessableEntity)
		if n := count(t, `SELECT count(*) FROM organization_invitations`); n != 0 {
			t.Fatalf("invitaciones = %d", n)
		}
	})

	t.Run("no se invita a quien ya es miembro", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		miembro := h.user(withEmail("ya@ejemplo.com"))
		h.join(miembro, o, "employee")
		want(t, invite(h, admin, o, "YA@ejemplo.com", "employee"), http.StatusConflict)
	})

	t.Run("sí se invita a alguien que ya tiene cuenta en otra organización", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		h.user(withEmail("otra@ejemplo.com"))
		want(t, invite(h, admin, o, "otra@ejemplo.com", "employee"), http.StatusCreated)
	})

	t.Run("un rol personalizado de la organización se puede invitar por código", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		crearRol(t, h, admin, o, "cajero", "org.members.read")
		rec := invite(h, admin, o, "c@ejemplo.com", "cajero")
		want(t, rec, http.StatusCreated)
		if jsonMap(t, rec)["role"] != "cajero" {
			t.Fatalf("invitación = %v", jsonMap(t, rec))
		}
	})

	t.Run("el rol de otra organización no existe para esta", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		otroAdmin, otra := orgAdmin(h, "Otra")
		idAjeno := crearRol(t, h, otroAdmin, otra, "secreto", "org.members.read")
		want(t, invite(h, admin, o, "c@ejemplo.com", idAjeno), http.StatusUnprocessableEntity)
	})
}

func TestVistaPreviaDeUnaInvitacion(t *testing.T) {
	preview := func(h *harness, token string) *httptest.ResponseRecorder {
		return h.do(http.MethodPost, "/v1/invitations/preview", map[string]string{"token": token}, nil)
	}

	t.Run("es pública y muestra organización, correo y rol", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme Corp")
		token := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		rec := preview(h, token)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["organization_name"] != "Acme Corp" || body["organization_slug"] != "acme-corp" ||
			body["email"] != "a@ejemplo.com" || body["role_name"] != "Empleado" {
			t.Fatalf("vista previa = %v", body)
		}
		if _, leaks := body["organization_id"]; leaks {
			t.Error("la vista previa no debe revelar identificadores internos")
		}
	})

	t.Run("cualquier token inválido responde igual: 404", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		valido := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))

		revocado := invite(h, admin, o, "b@ejemplo.com", "employee")
		tokRevocado := tokenOf(t, revocado)
		want(t, h.do(http.MethodDelete, orgURL(o, "/invitations/"+jsonMap(t, revocado)["id"].(string)), nil, &admin), http.StatusNoContent)

		caducada := tokenOf(t, invite(h, admin, o, "c@ejemplo.com", "employee"))
		_, err := pool.Exec(t.Context(), `UPDATE organization_invitations SET expires_at = now() - interval '1 minute' WHERE email = 'c@ejemplo.com'`)
		must(t, err)

		var bodies []string
		for name, tok := range map[string]string{
			"inventado": "abc", "vacío de sentido": strings.Repeat("A", 43), "revocado": tokRevocado, "caducado": caducada,
		} {
			rec := preview(h, tok)
			want(t, rec, http.StatusNotFound)
			bodies = append(bodies, name+":"+jsonMap(t, rec)["detail"].(string))
		}
		// Mismo mensaje: quien tiene un token viejo no averigua por qué falló.
		for _, b := range bodies {
			if _, detail, _ := strings.Cut(b, ":"); detail != "la invitación no existe o ya no es válida" {
				t.Errorf("mensaje distinto: %s", b)
			}
		}
		want(t, preview(h, valido), http.StatusOK) // el válido sigue sirviendo
		want(t, preview(h, ""), http.StatusUnprocessableEntity)
	})

	t.Run("una organización suspendida no invita", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, preview(h, token), http.StatusNotFound)
	})
}

func TestAceptarUnaInvitacion(t *testing.T) {
	accept := func(h *harness, as person, token string) *httptest.ResponseRecorder {
		return h.do(http.MethodPost, "/v1/invitations/accept", acceptBody(token), &as)
	}

	t.Run("quien no tiene perfil lo recibe con la membresía, y ya puede operar", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "nuevo@ejemplo.com", "employee"))
		nuevo := h.invitee("nuevo@ejemplo.com")
		if n := count(t, `SELECT count(*) FROM users WHERE id = $1`, nuevo.ID); n != 0 {
			t.Fatal("la persona ya tenía perfil")
		}

		rec := accept(h, nuevo, token)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		if body["role"] != "employee" || body["organization_id"] != o.ID.String() || body["user_id"] != nuevo.ID.String() {
			t.Fatalf("miembro = %v", body)
		}
		if got := str(t, `SELECT email FROM users WHERE id = $1`, nuevo.ID); got != "nuevo@ejemplo.com" {
			t.Errorf("perfil creado con el correo %q", got)
		}
		if n := count(t, `SELECT count(*) FROM customers WHERE user_id = $1`, nuevo.ID); n != 0 {
			t.Error("el personal invitado no es cliente: no debía crearse un perfil de cliente")
		}
		if got := str(t, `SELECT r.code FROM user_organizations uo JOIN roles r ON r.id = uo.role_id WHERE uo.user_id = $1`, nuevo.ID); got != "employee" {
			t.Errorf("rol = %q", got)
		}
		// Desde ahora es un usuario de negocio normal.
		want(t, h.do(http.MethodGet, orgURL(o, ""), nil, &nuevo), http.StatusOK)
		acts := auditActions(t, o)
		if !contains(acts, "invitation.accepted") {
			t.Errorf("auditoría = %v", acts)
		}
		if got := str(t, `SELECT actor_id::text FROM audit_log WHERE action = 'invitation.accepted'`); got != nuevo.ID.String() {
			t.Errorf("el actor de la aceptación debía ser quien aceptó: %s", got)
		}
	})

	t.Run("quien ya tiene perfil solo suma la membresía", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		otra := h.user(withEmail("ya@ejemplo.com"))
		h.join(otra, h.org("Otra Org"), "employee")
		token := tokenOf(t, invite(h, admin, o, "ya@ejemplo.com", "employee"))
		want(t, accept(h, otra, token), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, otra.ID); n != 2 {
			t.Fatalf("membresías = %d, quería 2 (puede estar en varias organizaciones)", n)
		}
	})

	t.Run("el token sirve una sola vez", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		a := h.invitee("a@ejemplo.com")
		want(t, accept(h, a, token), http.StatusOK)
		want(t, accept(h, a, token), http.StatusNotFound)
		// Ni siquiera en vista previa.
		want(t, h.do(http.MethodPost, "/v1/invitations/preview", acceptBody(token), nil), http.StatusNotFound)
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE organization_id = $1`, o.ID); n != 2 {
			t.Fatalf("miembros = %d, quería 2", n)
		}
	})

	t.Run("el correo de la sesión debe ser el de la invitación", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "invitado@ejemplo.com", "employee"))
		intruso := h.invitee("intruso@ejemplo.com")

		want(t, accept(h, intruso, token), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM users WHERE id = $1`, intruso.ID); n != 0 {
			t.Error("un intento rechazado creó un perfil")
		}
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, intruso.ID); n != 0 {
			t.Error("un intento rechazado creó una membresía")
		}
		// La invitación sigue viva para su destinatario legítimo.
		want(t, accept(h, h.invitee("invitado@ejemplo.com"), token), http.StatusOK)
	})

	t.Run("no distingue mayúsculas en el correo", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "Mixto@Ejemplo.com", "employee"))
		want(t, accept(h, h.invitee("MIXTO@ejemplo.COM"), token), http.StatusOK)
	})

	t.Run("rechaza tokens inválidos, caducados y revocados", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		p := h.invitee("a@ejemplo.com")
		want(t, accept(h, p, "inventado"), http.StatusNotFound)
		want(t, accept(h, p, ""), http.StatusUnprocessableEntity)

		caducada := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		_, err := pool.Exec(t.Context(), `UPDATE organization_invitations SET expires_at = now() - interval '1 second'`)
		must(t, err)
		want(t, accept(h, p, caducada), http.StatusNotFound)

		rec := invite(h, admin, o, "a@ejemplo.com", "employee") // reinvita: reemplaza la caducada
		want(t, rec, http.StatusCreated)
		want(t, h.do(http.MethodDelete, orgURL(o, "/invitations/"+jsonMap(t, rec)["id"].(string)), nil, &admin), http.StatusNoContent)
		want(t, accept(h, p, tokenOf(t, rec)), http.StatusNotFound)
		if n := count(t, `SELECT count(*) FROM users WHERE id = $1`, p.ID); n != 0 {
			t.Error("los intentos rechazados no deben crear un perfil")
		}
	})

	t.Run("una cuenta desactivada no acepta", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		baja := h.user(withEmail("baja@ejemplo.com"), inactive())
		token := tokenOf(t, invite(h, admin, o, "baja@ejemplo.com", "employee"))
		// Un usuario inactivo ni siquiera pasa el filtro de negocio, pero esta ruta solo
		// pide sesión: la regla debe cumplirse igual.
		want(t, accept(h, baja, token), http.StatusForbidden)
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, baja.ID); n != 0 {
			t.Error("una cuenta desactivada entró a la organización")
		}
	})

	t.Run("una organización suspendida no admite miembros nuevos", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		want(t, h.do(http.MethodPost, platformOrgURL(o, "/suspend"), map[string]any{"reason": "x"}, &sa), http.StatusOK)
		want(t, accept(h, h.invitee("a@ejemplo.com"), token), http.StatusNotFound) // no se revela: como si no existiera
	})

	t.Run("quien ya es miembro con el mismo rol consume la invitación sin duplicarse", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "ya@ejemplo.com", "employee"))
		miembro := h.user(withEmail("ya@ejemplo.com"))
		h.join(miembro, o, "employee") // se sumó por otra vía después de ser invitado
		want(t, accept(h, miembro, token), http.StatusOK)
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, miembro.ID); n != 1 {
			t.Fatalf("membresías = %d", n)
		}
	})

	t.Run("quien ya es miembro con otro rol no cambia de rol por una invitación", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, sa, o, "ya@ejemplo.com", "admin"))
		miembro := h.user(withEmail("ya@ejemplo.com"))
		h.join(miembro, o, "employee")
		want(t, accept(h, miembro, token), http.StatusConflict)
		if got := str(t, `SELECT r.code FROM user_organizations uo JOIN roles r ON r.id = uo.role_id WHERE uo.user_id = $1`, miembro.ID); got != "employee" {
			t.Fatalf("rol = %q: una invitación le cambió el rol", got)
		}
		_ = admin
	})

	t.Run("dos aceptaciones simultáneas: solo una gana", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		token := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		p := h.invitee("a@ejemplo.com")
		codes := make(chan int, 2)
		for range 2 {
			go func() { codes <- accept(h, p, token).Code }()
		}
		a, b := <-codes, <-codes
		oneWins := (a == http.StatusOK && b == http.StatusNotFound) || (a == http.StatusNotFound && b == http.StatusOK)
		if !oneWins {
			t.Fatalf("respuestas = %d y %d, quería un 200 y un 404", a, b)
		}
		if n := count(t, `SELECT count(*) FROM user_organizations WHERE user_id = $1`, p.ID); n != 1 {
			t.Fatalf("membresías = %d", n)
		}
	})

	t.Run("exige sesión", func(t *testing.T) {
		h := newHarness(t)
		want(t, h.do(http.MethodPost, "/v1/invitations/accept", acceptBody("x"), nil), http.StatusUnauthorized)
	})
}

func TestReemitirYRevocarInvitaciones(t *testing.T) {
	t.Run("reinvitar reemplaza el enlace anterior", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		viejo := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		nuevo := tokenOf(t, invite(h, admin, o, "a@ejemplo.com", "employee"))
		if viejo == nuevo {
			t.Fatal("el enlace nuevo debía ser distinto")
		}
		if n := count(t, `SELECT count(*) FROM organization_invitations`); n != 1 {
			t.Fatalf("invitaciones = %d: reinvitar no debe dejar dos vivas", n)
		}
		p := h.invitee("a@ejemplo.com")
		want(t, h.do(http.MethodPost, "/v1/invitations/accept", acceptBody(viejo), &p), http.StatusNotFound)
		want(t, h.do(http.MethodPost, "/v1/invitations/accept", acceptBody(nuevo), &p), http.StatusOK)
	})

	t.Run("reenviar por id da un enlace nuevo y mata el anterior", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := invite(h, admin, o, "a@ejemplo.com", "employee")
		viejo, id := tokenOf(t, rec), jsonMap(t, rec)["id"].(string)

		re := h.do(http.MethodPost, orgURL(o, "/invitations/"+id+"/resend"), nil, &admin)
		want(t, re, http.StatusOK)
		nuevo := tokenOf(t, re)
		want(t, h.do(http.MethodPost, "/v1/invitations/preview", acceptBody(viejo), nil), http.StatusNotFound)
		want(t, h.do(http.MethodPost, "/v1/invitations/preview", acceptBody(nuevo), nil), http.StatusOK)
		want(t, h.do(http.MethodPost, orgURL(o, "/invitations/"+uuid.NewString()+"/resend"), nil, &admin), http.StatusNotFound)
	})

	t.Run("revocar mata el enlace; revocar dos veces o una ya aceptada da 404", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := invite(h, admin, o, "a@ejemplo.com", "employee")
		token, id := tokenOf(t, rec), jsonMap(t, rec)["id"].(string)
		url := orgURL(o, "/invitations/"+id)

		want(t, h.do(http.MethodDelete, url, nil, &admin), http.StatusNoContent)
		want(t, h.do(http.MethodDelete, url, nil, &admin), http.StatusNotFound)
		want(t, h.do(http.MethodPost, "/v1/invitations/preview", acceptBody(token), nil), http.StatusNotFound)
		if acts := auditActions(t, o); !contains(acts, "invitation.revoked") {
			t.Errorf("auditoría = %v", acts)
		}

		// Una aceptada tampoco se revoca: ya es una membresía (se quita como miembro).
		rec = invite(h, admin, o, "b@ejemplo.com", "employee")
		p := h.invitee("b@ejemplo.com")
		want(t, h.do(http.MethodPost, "/v1/invitations/accept", acceptBody(tokenOf(t, rec)), &p), http.StatusOK)
		want(t, h.do(http.MethodDelete, orgURL(o, "/invitations/"+jsonMap(t, rec)["id"].(string)), nil, &admin), http.StatusNotFound)
	})

	t.Run("aislamiento entre organizaciones", func(t *testing.T) {
		h := newHarness(t)
		adminA, a := orgAdmin(h, "Org A")
		adminB, b := orgAdmin(h, "Org B")
		recB := invite(h, adminB, b, "x@ejemplo.com", "employee")
		idB := jsonMap(t, recB)["id"].(string)

		// A no lista, no revoca y no reenvía las de B, ni por su propia URL ni por la de B.
		want(t, h.do(http.MethodGet, orgURL(b, "/invitations"), nil, &adminA), http.StatusForbidden)
		want(t, h.do(http.MethodDelete, orgURL(b, "/invitations/"+idB), nil, &adminA), http.StatusForbidden)
		want(t, h.do(http.MethodDelete, orgURL(a, "/invitations/"+idB), nil, &adminA), http.StatusNotFound)
		want(t, h.do(http.MethodPost, orgURL(a, "/invitations/"+idB+"/resend"), nil, &adminA), http.StatusNotFound)
		if n := count(t, `SELECT count(*) FROM organization_invitations WHERE id = $1 AND revoked_at IS NULL`, idB); n != 1 {
			t.Fatal("la invitación de B fue revocada por A")
		}
		// Y el listado de A no incluye las de B.
		listA := decode[[]map[string]any](t, h.do(http.MethodGet, orgURL(a, "/invitations"), nil, &adminA))
		if len(listA) != 0 {
			t.Fatalf("A ve invitaciones ajenas: %v", listA)
		}
	})

	t.Run("un empleado no lista, revoca ni reenvía", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		empleado := h.user()
		h.join(empleado, o, "employee")
		rec := invite(h, admin, o, "a@ejemplo.com", "employee")
		id := jsonMap(t, rec)["id"].(string)
		want(t, h.do(http.MethodGet, orgURL(o, "/invitations"), nil, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodDelete, orgURL(o, "/invitations/"+id), nil, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodPost, orgURL(o, "/invitations/"+id+"/resend"), nil, &empleado), http.StatusForbidden)
	})
}

// El flujo completo de incorporar un negocio: el operador crea la organización con su
// primer administrador, que acepta la invitación y queda administrando.
func TestIncorporarUnNegocioDeExtremoAExtremo(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())

	rec := h.do(http.MethodPost, "/v1/organizations", map[string]any{
		"name": "Restaurante Nuevo", "plan_tier": "pro", "admin_email": "dueno@restaurante.com",
		"settings": map[string]any{"city": "Cali"},
	}, &sa)
	want(t, rec, http.StatusCreated)
	org := jsonMap(t, rec)
	inv, _ := org["admin_invitation"].(map[string]any)
	if inv == nil {
		t.Fatalf("falta admin_invitation: %v", org)
	}
	token := tokenFromLink(t, inv["accept_url"].(string))
	o := organization{ID: uuid.MustParse(org["id"].(string)), Slug: org["slug"].(string)}

	// La organización existe pero nadie la administra todavía.
	if n := count(t, `SELECT count(*) FROM user_organizations WHERE organization_id = $1`, o.ID); n != 0 {
		t.Fatalf("miembros antes de aceptar = %d", n)
	}
	// El futuro dueño ve la invitación, inicia sesión y la acepta.
	want(t, h.do(http.MethodPost, "/v1/invitations/preview", acceptBody(token), nil), http.StatusOK)
	dueno := h.invitee("dueno@restaurante.com")
	want(t, h.do(http.MethodPost, "/v1/invitations/accept", acceptBody(token), &dueno), http.StatusOK)

	// Ahora administra: ve sus ajustes, su plan, y puede invitar a su equipo.
	want(t, h.do(http.MethodGet, orgURL(o, "/settings"), nil, &dueno), http.StatusOK)
	if !contains(features(t, h, o, dueno), "template_sync") {
		t.Error("el plan pro incluye sincronizar plantillas")
	}
	want(t, h.do(http.MethodGet, orgURL(o, "/subscription"), nil, &dueno), http.StatusOK)
	want(t, invite(h, dueno, o, "cajera@restaurante.com", "employee"), http.StatusCreated)
	// Pero no puede nombrar a otro administrador.
	want(t, invite(h, dueno, o, "socio@restaurante.com", "admin"), http.StatusForbidden)

	// Y la cara pública del negocio ya responde por su slug.
	pub := h.do(http.MethodGet, "/v1/public/organizations/"+o.Slug, nil, nil)
	want(t, pub, http.StatusOK)
	if jsonMap(t, pub)["city"] != "Cali" {
		t.Errorf("cara pública = %v", jsonMap(t, pub))
	}
}
