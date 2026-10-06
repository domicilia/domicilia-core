package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/auth"
)

const (
	secret   = "0123456789abcdef0123456789abcdef" // gitleaks:allow — valor falso de prueba
	audience = "authenticated"
)

type claims struct {
	Email string `json:"email,omitempty"`
	Role  string `json:"role,omitempty"`
	AAL   string `json:"aal,omitempty"`
	jwt.RegisteredClaims
}

func valid() claims {
	return claims{
		Email: "ana@ejemplo.com",
		Role:  "authenticated",
		AAL:   "aal1",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "11111111-1111-1111-1111-111111111111",
			Audience:  jwt.ClaimStrings{audience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
}

func sign(t *testing.T, c claims, method jwt.SigningMethod, key any) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, c).SignedString(key)
	if err != nil {
		t.Fatalf("firmar token: %v", err)
	}
	return s
}

func TestVerify(t *testing.T) {
	expired := valid()
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))

	recent := valid() // vencido hace 1 s: dentro de la tolerancia de reloj
	recent.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Second))

	noExp := valid()
	noExp.ExpiresAt = nil

	badAud := valid()
	badAud.Audience = jwt.ClaimStrings{"otra-cosa"}

	noSub := valid()
	noSub.Subject = ""

	tests := []struct {
		name  string
		token string
		ok    bool
	}{
		{"válido", sign(t, valid(), jwt.SigningMethodHS256, []byte(secret)), true},
		{"vencido hace 1 s (tolerancia de reloj)", sign(t, recent, jwt.SigningMethodHS256, []byte(secret)), true},
		{"vencido", sign(t, expired, jwt.SigningMethodHS256, []byte(secret)), false},
		{"sin exp", sign(t, noExp, jwt.SigningMethodHS256, []byte(secret)), false},
		{"secreto ajeno", sign(t, valid(), jwt.SigningMethodHS256, []byte("otro-secreto-otro-secreto-otro-secreto")), false},
		{"audiencia equivocada", sign(t, badAud, jwt.SigningMethodHS256, []byte(secret)), false},
		{"sin sub", sign(t, noSub, jwt.SigningMethodHS256, []byte(secret)), false},
		{"otro algoritmo HS512", sign(t, valid(), jwt.SigningMethodHS512, []byte(secret)), false},
		{"alg none", sign(t, valid(), jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType), false},
		{"basura", "esto.no.es.un.jwt", false},
		{"vacío", "", false},
	}

	v := auth.NewVerifier(secret, audience)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := v.Verify(tc.token)
			if tc.ok {
				if err != nil {
					t.Fatalf("debía ser válido: %v", err)
				}
				if got.Subject == "" || got.Email != "ana@ejemplo.com" || got.AAL != "aal1" {
					t.Fatalf("claims inesperadas: %+v", got)
				}
				return
			}
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("debía rechazarse con ErrInvalidToken, err = %v", err)
			}
		})
	}
}

func serverConEjemplo() *echo.Echo {
	e := echo.New()
	g := e.Group("/v1", auth.Middleware(auth.NewVerifier(secret, audience)))
	g.GET("/whoami", auth.Whoami)
	g.GET("/ctx", func(c *echo.Context) error {
		// La identidad debe llegar al context.Context, no solo a Echo.
		cl, ok := auth.FromContext(context.WithoutCancel(c.Request().Context()))
		if !ok {
			return errors.New("sin identidad en el context")
		}
		return c.String(http.StatusOK, cl.Subject)
	})
	return e
}

func get(e *echo.Echo, path, authz string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	if authz != "" {
		req.Header.Set(echo.HeaderAuthorization, authz)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestMiddleware(t *testing.T) {
	e := serverConEjemplo()
	good := sign(t, valid(), jwt.SigningMethodHS256, []byte(secret))

	tests := []struct {
		name  string
		authz string
		want  int
	}{
		{"sin cabecera", "", http.StatusUnauthorized},
		{"esquema Basic", "Basic abc", http.StatusUnauthorized},
		{"Bearer sin token", "Bearer ", http.StatusUnauthorized},
		{"solo el token, sin esquema", good, http.StatusUnauthorized},
		{"token inválido", "Bearer basura", http.StatusUnauthorized},
		{"Bearer válido", "Bearer " + good, http.StatusOK},
		{"esquema en minúsculas", "bearer " + good, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(e, "/v1/whoami", tc.authz)
			if rec.Code != tc.want {
				t.Fatalf("código = %d, quería %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestMiddlewareAnunciaEsquemaEnElRechazo(t *testing.T) {
	rec := get(serverConEjemplo(), "/v1/whoami", "")

	if got := rec.Header().Get(echo.HeaderWWWAuthenticate); got == "" {
		t.Fatal("un 401 debe traer WWW-Authenticate")
	}
}

func TestMiddlewarePoneLaIdentidadEnElContext(t *testing.T) {
	good := sign(t, valid(), jwt.SigningMethodHS256, []byte(secret))

	rec := get(serverConEjemplo(), "/v1/ctx", "Bearer "+good)

	if rec.Code != http.StatusOK || rec.Body.String() != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("respuesta = %d %q", rec.Code, rec.Body.String())
	}
}
