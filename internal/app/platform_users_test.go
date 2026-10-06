package app_test

import (
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// Gestión de usuarios por el operador: listar, filtrar, ver, activar y desactivar.

func userEmails(t *testing.T, h *harness, path string, as person) (emails []string, total float64) {
	t.Helper()
	rec := h.do(http.MethodGet, path, nil, &as)
	want(t, rec, http.StatusOK)
	body := jsonMap(t, rec)
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items debe ser una lista y no null: %v", body["items"])
	}
	for _, it := range items {
		emails = append(emails, it.(map[string]any)["email"].(string))
	}
	total, _ = body["total"].(float64)
	return emails, total
}

// plataformaConUsuarios arma un escenario con un usuario de cada tipo.
type escenario struct {
	sa, a1, e1, b1, domi, cliente, inactivo person
	orgA, orgB                              organization
}

func armarEscenario(h *harness) escenario {
	var e escenario
	e.sa = h.user(superadmin(), withEmail("sa@ejemplo.com"))
	e.orgA, e.orgB = h.org("Acme"), h.org("Beta")
	e.a1, e.e1, e.b1 = h.user(withEmail("a1@ejemplo.com")), h.user(withEmail("e1@ejemplo.com")), h.user(withEmail("b1@ejemplo.com"))
	h.join(e.a1, e.orgA, "admin")
	h.join(e.e1, e.orgA, "employee")
	h.join(e.b1, e.orgB, "admin")
	e.domi = h.user(delivery(), withEmail("domi@ejemplo.com"))
	e.cliente = h.user(withEmail("cliente@ejemplo.com"))
	h.grant(e.cliente.ID, "customer")
	e.inactivo = h.user(inactive(), withEmail("inactivo@ejemplo.com"))
	return e
}

func TestListarUsuariosDeLaPlataforma(t *testing.T) {
	t.Run("exige platform.users.read", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		want(t, h.do(http.MethodGet, "/v1/platform/users", nil, &e.a1), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/platform/users", nil, &e.cliente), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/platform/users", nil, nil), http.StatusUnauthorized)
	})

	t.Run("lista a todos, ordenados por correo, con sus roles", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		emails, total := userEmails(t, h, "/v1/platform/users", e.sa)
		if total != 7 || len(emails) != 7 {
			t.Fatalf("total = %v, items = %d, quería 7", total, len(emails))
		}
		if !slices.IsSorted(emails) {
			t.Fatalf("no vienen ordenados por correo: %v", emails)
		}

		rec := h.do(http.MethodGet, "/v1/platform/users?role=delivery", nil, &e.sa)
		it := jsonMap(t, rec)["items"].([]any)[0].(map[string]any)
		roles, _ := it["roles"].([]any)
		if it["is_delivery"] != true || it["is_general_admin"] != false || len(roles) != 1 || roles[0].(map[string]any)["code"] != "delivery" {
			t.Fatalf("usuario = %v", it)
		}
	})

	t.Run("pagina", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		p1, total := userEmails(t, h, "/v1/platform/users?limit=3&offset=0", e.sa)
		p2, _ := userEmails(t, h, "/v1/platform/users?limit=3&offset=3", e.sa)
		p3, _ := userEmails(t, h, "/v1/platform/users?limit=3&offset=6", e.sa)
		vacia, totalVacia := userEmails(t, h, "/v1/platform/users?limit=3&offset=50", e.sa)
		if total != 7 || len(p1) != 3 || len(p2) != 3 || len(p3) != 1 {
			t.Fatalf("páginas = %d/%d/%d, total = %v", len(p1), len(p2), len(p3), total)
		}
		if len(vacia) != 0 || totalVacia != 7 {
			t.Fatalf("una página fuera de rango debe ser [] con el total intacto: %v, %v", vacia, totalVacia)
		}
		if all := append(append(p1, p2...), p3...); len(slices.Compact(slices.Clone(all))) != 7 {
			t.Fatalf("las páginas repiten o pierden usuarios: %v", all)
		}
	})

	t.Run("valida limit, offset y filtros", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		for _, q := range []string{"limit=0", "limit=201", "limit=abc", "offset=-1", "offset=x", "is_active=quizas", "organization_id=no-es-uuid"} {
			want(t, h.do(http.MethodGet, "/v1/platform/users?"+q, nil, &e.sa), http.StatusUnprocessableEntity)
		}
	})

	t.Run("filtra por texto, rol, organización y estado", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		tests := []struct {
			name  string
			query string
			want  []string
		}{
			{"búsqueda en el correo, sin distinguir mayúsculas", "search=CLIENTE", []string{"cliente@ejemplo.com"}},
			{"rol de plataforma", "role=superadmin", []string{"sa@ejemplo.com"}},
			{"rol de plataforma: domiciliario", "role=delivery", []string{"domi@ejemplo.com"}},
			{"rol de organización", "role=admin", []string{"a1@ejemplo.com", "b1@ejemplo.com"}},
			{"rol de organización: empleado", "role=employee", []string{"e1@ejemplo.com"}},
			{"organización", "organization_id=" + e.orgA.ID.String(), []string{"a1@ejemplo.com", "e1@ejemplo.com"}},
			{"rol y organización a la vez", "role=admin&organization_id=" + e.orgA.ID.String(), []string{"a1@ejemplo.com"}},
			{"inactivos", "is_active=false", []string{"inactivo@ejemplo.com"}},
			{"nadie coincide", "search=zzz-no-existe", nil},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, total := userEmails(t, h, "/v1/platform/users?"+tc.query, e.sa)
				if !slices.Equal(got, tc.want) || int(total) != len(tc.want) {
					t.Fatalf("resultado = %v (total %v), quería %v", got, total, tc.want)
				}
			})
		}
	})

	t.Run("los comodines de LIKE en la búsqueda se toman literales", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		for _, q := range []string{"%", "_", "a%", "\\"} {
			got, _ := userEmails(t, h, "/v1/platform/users?search="+url.QueryEscape(q), e.sa)
			if len(got) != 0 {
				t.Errorf("search=%q devolvió %v: los comodines no deben funcionar como comodines", q, got)
			}
		}
	})
}

func TestDetalleDeUsuario(t *testing.T) {
	h := newHarness(t)
	e := armarEscenario(h)

	rec := h.do(http.MethodGet, "/v1/platform/users/"+e.a1.ID.String(), nil, &e.sa)
	want(t, rec, http.StatusOK)
	d := jsonMap(t, rec)
	ms, _ := d["memberships"].([]any)
	if d["email"] != "a1@ejemplo.com" || len(ms) != 1 || ms[0].(map[string]any)["role"] != "admin" || ms[0].(map[string]any)["organization_slug"] != "acme" {
		t.Fatalf("detalle = %v", d)
	}
	if v, present := d["customer_profile"]; !present || v != nil {
		t.Fatalf("un staff no tiene perfil de cliente: %v", v)
	}

	want(t, h.do(http.MethodGet, "/v1/platform/users/"+e.a1.ID.String(), nil, &e.e1), http.StatusForbidden)
	want(t, h.do(http.MethodGet, "/v1/platform/users/"+uuid.NewString(), nil, &e.sa), http.StatusNotFound)
	want(t, h.do(http.MethodGet, "/v1/platform/users/no-es-uuid", nil, &e.sa), http.StatusUnprocessableEntity)
}

func activeURL(u person) string { return "/v1/platform/users/" + u.ID.String() + "/active" }

func TestActivarYDesactivarCuentas(t *testing.T) {
	t.Run("una cuenta desactivada recibe 403 en todo; reactivarla la devuelve", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)

		rec := h.do(http.MethodPatch, activeURL(e.a1), map[string]any{"is_active": false}, &e.sa)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["is_active"] != false {
			t.Fatalf("respuesta = %v", jsonMap(t, rec))
		}
		want(t, h.do(http.MethodGet, "/v1/users/me", nil, &e.a1), http.StatusForbidden)
		want(t, h.do(http.MethodGet, "/v1/organizations", nil, &e.a1), http.StatusForbidden)

		want(t, h.do(http.MethodPatch, activeURL(e.a1), map[string]any{"is_active": true}, &e.sa), http.StatusOK)
		want(t, h.do(http.MethodGet, "/v1/users/me", nil, &e.a1), http.StatusOK)
	})

	t.Run("exige platform.users.manage; leer no basta", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		crearRolDePlataforma(t, h, e.sa, "lector", "platform.users.read")
		lector := conRolDePlataforma(t, h, e.sa, "lector")
		want(t, h.do(http.MethodPatch, activeURL(e.a1), map[string]any{"is_active": false}, &lector), http.StatusForbidden)
		want(t, h.do(http.MethodPatch, activeURL(e.a1), map[string]any{"is_active": false}, &e.e1), http.StatusForbidden)
		if !flag(t, `SELECT is_active FROM users WHERE id = $1`, e.a1.ID) {
			t.Fatal("se desactivó pese al 403")
		}
	})

	t.Run("nadie desactiva su propia cuenta", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		want(t, h.do(http.MethodPatch, activeURL(e.sa), map[string]any{"is_active": false}, &e.sa), http.StatusConflict)
	})

	t.Run("no se desactiva al último superadmin activo", func(t *testing.T) {
		h := newHarness(t)
		sa := h.user(superadmin())
		crearRolDePlataforma(t, h, sa, "gestor-usuarios", "platform.users.manage", "platform.users.read")
		gestor := conRolDePlataforma(t, h, sa, "gestor-usuarios")

		want(t, h.do(http.MethodPatch, activeURL(sa), map[string]any{"is_active": false}, &gestor), http.StatusConflict)
		if !flag(t, `SELECT is_active FROM users WHERE id = $1`, sa.ID) {
			t.Fatal("se desactivó al último superadmin: la plataforma quedó sin operador")
		}

		// Con otro superadmin activo, sí.
		otro := h.user(superadmin())
		want(t, h.do(http.MethodPatch, activeURL(sa), map[string]any{"is_active": false}, &gestor), http.StatusOK)
		// Y ahora el que queda es el último.
		want(t, h.do(http.MethodPatch, activeURL(otro), map[string]any{"is_active": false}, &gestor), http.StatusConflict)
	})

	t.Run("dos superadmins que se desactivan a la vez: queda exactamente uno activo", func(t *testing.T) {
		h := newHarness(t)
		sa1, sa2 := h.user(superadmin()), h.user(superadmin())

		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i, pair := range [][2]person{{sa1, sa2}, {sa2, sa1}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				actor, target := pair[0], pair[1]
				codes[i] = h.do(http.MethodPatch, activeURL(target), map[string]any{"is_active": false}, &actor).Code
			}()
		}
		wg.Wait()

		ok := 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusForbidden, http.StatusConflict:
			default:
				t.Fatalf("códigos = %v: resultado inesperado", codes)
			}
		}
		if ok != 1 {
			t.Fatalf("códigos = %v, quería exactamente un 200", codes)
		}
		if n := count(t, `SELECT count(*) FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
			JOIN users u ON u.id = upr.user_id WHERE r.code = 'superadmin' AND u.is_active`); n != 1 {
			t.Fatalf("superadmins activos = %d, quería 1", n)
		}
	})

	t.Run("valida el cuerpo y el usuario", func(t *testing.T) {
		h := newHarness(t)
		e := armarEscenario(h)
		want(t, h.do(http.MethodPatch, activeURL(e.a1), map[string]any{}, &e.sa), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPatch, "/v1/platform/users/"+uuid.NewString()+"/active", map[string]any{"is_active": true}, &e.sa), http.StatusNotFound)
		want(t, h.do(http.MethodPatch, "/v1/platform/users/no-es-uuid/active", map[string]any{"is_active": true}, &e.sa), http.StatusUnprocessableEntity)
	})
}
