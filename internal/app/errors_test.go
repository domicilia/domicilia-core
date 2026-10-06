package app_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// El formato de los errores es parte del contrato: el frontend lee `detail`, y
// los 5xx nunca deben filtrar detalle interno (SQL, nombres de tablas, rutas).

func TestLosErroresSalenComoProblemJSON(t *testing.T) {
	h := newHarness(t)
	u := h.user()
	rec := h.do(http.MethodGet, "/v1/organizations/"+h.org("").ID.String(), nil, &u)

	want(t, rec, http.StatusForbidden)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("Content-Type = %q, quería application/problem+json", ct)
	}
	p := jsonMap(t, rec)
	if p["status"] != float64(http.StatusForbidden) || p["title"] != "Forbidden" {
		t.Fatalf("problem = %v", p)
	}
	if d, _ := p["detail"].(string); d == "" {
		t.Fatalf("un 4xx debe explicar qué pasó en `detail`: %v", p)
	}
	if p["request_id"] == nil || p["request_id"] == "" {
		t.Fatalf("falta request_id para correlacionar con el log: %v", p)
	}
}

func TestUnCuerpoMalFormadoEsUn400(t *testing.T) {
	h := newHarness(t)
	sa := h.user(superadmin())
	want(t, h.do(http.MethodPost, "/v1/organizations", `{"name": `, &sa), http.StatusBadRequest)
}

// Un fallo interno jamás expone su detalle. Aquí se provoca uno real: un token
// válido de una cuenta que no está en auth.users, así que la FK de public.users
// rechaza el alta y Postgres devuelve un error con nombres de tablas.
func TestUnErrorInternoNoFiltraDetalle(t *testing.T) {
	h := newHarness(t)
	idHuerfano := "00000000-0000-4000-8000-000000000001" // no existe en auth.users

	rec := h.doToken(http.MethodPost, "/v1/users", map[string]any{},
		signToken(t, testSecret, idHuerfano, func(c jwt.MapClaims) { c["email"] = "huerfano@ejemplo.com" }))

	want(t, rec, http.StatusInternalServerError)
	body := strings.ToLower(rec.Body.String())
	for _, secreto := range []string{"foreign key", "fk_users_auth_users", "auth.users", "sqlstate", "violates", "constraint"} {
		if strings.Contains(body, secreto) {
			t.Fatalf("el error 5xx filtró %q al cliente: %s", secreto, rec.Body.String())
		}
	}
	if p := jsonMap(t, rec); p["detail"] != nil && p["detail"] != "" {
		t.Fatalf("un 5xx no debe traer detail: %v", p)
	}
}

// Seguro por omisión, y comprobado sobre el router REAL: se recorre toda ruta
// registrada y solo las de la lista blanca pueden responder sin credenciales. Una
// ruta nueva que nazca pública (por olvido, o por colgarla del grupo equivocado)
// hace fallar esta prueba: quien la agregue debe justificar la excepción aquí.
func TestSoloLasRutasJustificadasSonPublicas(t *testing.T) {
	h := newHarness(t)

	publicas := map[string]string{
		"GET /healthz":                                              "sonda de vida: no toca dependencias",
		"GET /readyz":                                               "sonda de disponibilidad",
		"POST /v1/driver-applications":                              "quien se postula como domiciliario aún no tiene cuenta",
		"GET /v1/public/organizations":                              "el directorio para elegir con quién pedir: quien lo mira aún no tiene cuenta",
		"GET /v1/public/organizations/:org_slug":                    "la cara de un negocio: quien va a pedir aún no tiene cuenta",
		"GET /v1/public/products":                                   "el feed de Inicio: quien lo mira aún no tiene cuenta",
		"GET /v1/public/organizations/:org_id/products/:product_id": "el detalle de un producto para configurarlo: quien lo mira aún no tiene cuenta",
		"POST /v1/invitations/preview":                              "el token de la invitación es la credencial; quien la recibe aún no tiene cuenta",
		"GET /webhooks/whatsapp":                                    "lo llama Meta para suscribir el webhook; se autentica con el token de verificación",
		"POST /webhooks/whatsapp":                                   "lo llama Meta con cada evento; se autentica con la firma HMAC del cuerpo",
		"POST /webhooks/payments":                                   "lo llama la pasarela de pago con cada confirmación; se autentica con su propia firma, no con un JWT",
	}
	param := regexp.MustCompile(`:[A-Za-z_]+`)
	const uuidDePrueba = "00000000-0000-4000-8000-000000000001"

	visto := map[string]bool{}
	protegidas := 0
	for _, r := range h.handler.Router().Routes() {
		if r.Method == echo.RouteNotFound {
			continue // los 404 internos de los grupos no son rutas de negocio
		}
		key := r.Method + " " + r.Path
		visto[key] = true
		if _, ok := publicas[key]; ok {
			continue
		}
		protegidas++
		t.Run(key, func(t *testing.T) {
			path := param.ReplaceAllString(r.Path, uuidDePrueba)
			want(t, h.do(r.Method, path, map[string]any{}, nil), http.StatusUnauthorized)
		})
	}

	// La lista blanca no puede quedarse con entradas que ya no existen.
	for key, motivo := range publicas {
		if !visto[key] {
			t.Errorf("la ruta pública %q (%s) ya no está registrada: quítala de la lista", key, motivo)
		}
	}
	// Y la enumeración debe haber encontrado de verdad las rutas de negocio.
	if protegidas < 70 {
		t.Fatalf("solo se recorrieron %d rutas protegidas: ¿cambió la forma de leer el router?", protegidas)
	}
}

func TestLasRutasPublicasSonPublicasDeVerdad(t *testing.T) {
	h := newHarness(t)
	want(t, h.do(http.MethodGet, "/healthz", nil, nil), http.StatusOK)
	want(t, h.do(http.MethodGet, "/readyz", nil, nil), http.StatusOK)
	// Sin credenciales llega hasta la validación del cuerpo (422), no se corta en 401.
	want(t, h.do(http.MethodPost, "/v1/driver-applications", map[string]any{}, nil), http.StatusUnprocessableEntity)
	want(t, h.do(http.MethodGet, "/v1/public/organizations", nil, nil), http.StatusOK)
	want(t, h.do(http.MethodGet, "/v1/public/organizations/no-existe", nil, nil), http.StatusNotFound)
	want(t, h.do(http.MethodGet, "/v1/public/products", nil, nil), http.StatusOK)
	want(t, h.do(http.MethodGet, "/v1/public/organizations/"+uuid.NewString()+"/products/"+uuid.NewString(), nil, nil), http.StatusNotFound)
	want(t, h.do(http.MethodPost, "/v1/invitations/preview", map[string]any{}, nil), http.StatusUnprocessableEntity)
}
