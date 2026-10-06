package app_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// El token es lo único que llega de fuera. Lo que más importa no es que uno
// válido pase, sino que se rechacen los que deben rechazarse.
func TestIdentidadRechazaTokensInvalidos(t *testing.T) {
	h := newHarness(t)
	activo := h.user()
	desactivado := h.user(inactive())
	sinPerfil := h.authIdentity()
	const claveAjena = "una-clave-ajena-suficientemente-larga-para-hmac-sha256"

	tests := []struct {
		name   string
		token  string
		status int
	}{
		{
			name:   "un token válido de un usuario con perfil pasa",
			token:  h.bearer(activo),
			status: http.StatusOK,
		},
		{
			name:   "sin cabecera Authorization",
			token:  "",
			status: http.StatusUnauthorized,
		},
		{
			name: "audiencia equivocada: distingue un token de GoTrue de cualquier otro JWT con la misma clave",
			token: signToken(t, testSecret, activo.ID.String(), func(c jwt.MapClaims) {
				c["aud"] = "otra-audiencia"
			}),
			status: http.StatusUnauthorized,
		},
		{
			name: "sin audiencia: el token service_role de los scripts no sirve para autenticarse como usuario",
			token: signToken(t, testSecret, activo.ID.String(), func(c jwt.MapClaims) {
				delete(c, "aud")
			}),
			status: http.StatusUnauthorized,
		},
		{
			name:   "firmado con otra clave: un token fabricado por un tercero",
			token:  signToken(t, claveAjena, activo.ID.String(), nil),
			status: http.StatusUnauthorized,
		},
		{
			name: "caducado",
			token: signToken(t, testSecret, activo.ID.String(), func(c jwt.MapClaims) {
				c["exp"] = time.Now().Add(-time.Hour).Unix()
			}),
			status: http.StatusUnauthorized,
		},
		{
			name:   "ilegible",
			token:  "esto-no-es-un-jwt",
			status: http.StatusUnauthorized,
		},
		{
			name:   "el sujeto no es un UUID",
			token:  signToken(t, testSecret, "no-es-un-uuid", nil),
			status: http.StatusUnauthorized,
		},
		{
			name:   "bien firmado pero sin perfil: la cuenta existe en GoTrue y aún no en el negocio",
			token:  signToken(t, testSecret, sinPerfil.String(), nil),
			status: http.StatusUnauthorized,
		},
		{
			name:   "usuario desactivado: 403 y no 401, porque está identificado y no puede",
			token:  h.bearer(desactivado),
			status: http.StatusForbidden,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.doToken(http.MethodGet, "/v1/users/me", nil, tc.token)
			want(t, rec, tc.status)
		})
	}
}

func TestUnTokenSinPerfilSeRechazaConDesafioDeAutenticacion(t *testing.T) {
	h := newHarness(t)
	rec := h.doToken(http.MethodGet, "/v1/users/me", nil, signToken(t, testSecret, h.authIdentity().String(), nil))

	want(t, rec, http.StatusUnauthorized)
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("un 401 debe traer WWW-Authenticate")
	}
}

// Ser admin de una organización NO da privilegios de plataforma: es la frontera
// entre el inquilino y el operador del servicio. Si se difuminara, cualquier
// cliente podría administrar a los demás.
func TestSoloElSuperadminEntraAlPanelDePlataforma(t *testing.T) {
	h := newHarness(t)
	normal := h.user()
	adminDeOrg := h.user()
	h.join(adminDeOrg, h.org("Una"), "admin")
	superadmin := h.user(superadmin())

	tests := []struct {
		name   string
		as     *person
		status int
	}{
		{"sin credenciales", nil, http.StatusUnauthorized},
		{"un usuario normal", &normal, http.StatusForbidden},
		{"un administrador de organización", &adminDeOrg, http.StatusForbidden},
		{"el superadmin", &superadmin, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want(t, h.do(http.MethodGet, "/v1/platform/overview", nil, tc.as), tc.status)
		})
	}
}

// Seguro por omisión: una ruta de /v1 que no existe no debe revelar nada a quien
// no tiene credenciales.
func TestUnaRutaInexistenteDeV1ExigeCredenciales(t *testing.T) {
	h := newHarness(t)
	want(t, h.do(http.MethodGet, "/v1/no-existe", nil, nil), http.StatusUnauthorized)
}
