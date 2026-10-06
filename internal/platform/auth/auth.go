// Package auth valida los JWT que emite auth-domicilia (GoTrue) y expone la
// identidad a los handlers. Solo autentica (quién eres): la autorización por
// organización (qué puedes ver) vive en el middleware de tenant + RLS.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"
)

// clockSkew tolera desfases de reloj entre el contenedor de GoTrue y este.
const clockSkew = 5 * time.Second

// Claims es lo que este servicio usa de un JWT de GoTrue.
type Claims struct {
	Subject   string // id del usuario (auth.users.id)
	Email     string
	Role      string // "authenticated" — NO es el rol de negocio (eso sale de la base)
	SessionID string
	AAL       string // aal1 = solo contraseña, aal2 = con segundo factor
}

type tokenClaims struct {
	Email     string `json:"email"`
	Role      string `json:"role"`
	SessionID string `json:"session_id"`
	AAL       string `json:"aal"`
	jwt.RegisteredClaims
}

// ErrInvalidToken agrupa cualquier fallo de validación. A propósito no dice
// cuál: al cliente solo le llega un 401 genérico.
var ErrInvalidToken = errors.New("auth: token inválido")

// Verifier valida tokens HS256 firmados con el secreto compartido con GoTrue.
type Verifier struct {
	secret []byte
	parser *jwt.Parser
}

// NewVerifier fija algoritmo, audiencia y expiración obligatorios. Fijar el
// algoritmo cierra los ataques clásicos de "alg: none" y de cambio de
// algoritmo.
func NewVerifier(secret, audience string) *Verifier {
	return &Verifier{
		secret: []byte(secret),
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithAudience(audience),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(clockSkew),
		),
	}
}

// Verify devuelve las claims de un token válido o ErrInvalidToken.
func (v *Verifier) Verify(raw string) (Claims, error) {
	var tc tokenClaims
	tok, err := v.parser.ParseWithClaims(raw, &tc, func(*jwt.Token) (any, error) {
		return v.secret, nil
	})
	if err != nil || !tok.Valid {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if tc.Subject == "" {
		return Claims{}, fmt.Errorf("%w: sin sub", ErrInvalidToken)
	}
	return Claims{
		Subject:   tc.Subject,
		Email:     tc.Email,
		Role:      tc.Role,
		SessionID: tc.SessionID,
		AAL:       tc.AAL,
	}, nil
}

type claimsKey struct{}

// FromContext devuelve la identidad puesta por Middleware.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}

// Middleware exige un `Authorization: Bearer <jwt>` válido. La identidad
// viaja en el context.Context de la petición (no en el de Echo) para que las
// capas de servicio y repositorio la lean sin depender del framework.
func Middleware(v *Verifier) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			raw, ok := bearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
			if !ok {
				return Unauthorized(c)
			}
			claims, err := v.Verify(raw)
			if err != nil {
				c.Logger().Debug("jwt rechazado", "error", err, "path", c.Request().URL.Path)
				return Unauthorized(c)
			}
			req := c.Request()
			c.SetRequest(req.WithContext(context.WithValue(req.Context(), claimsKey{}, claims)))
			return next(c)
		}
	}
}

// Unauthorized responde 401 con el desafío WWW-Authenticate. Lo usan también los
// middleware de negocio que dependen de la identidad (p. ej. "el usuario ya no
// existe"), para que todo 401 salga igual.
func Unauthorized(c *echo.Context) error {
	c.Response().Header().Set(echo.HeaderWWWAuthenticate, `Bearer realm="domicilia"`)
	return echo.NewHTTPError(http.StatusUnauthorized, "credenciales requeridas o inválidas")
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// Whoami devuelve la identidad del token: sirve de prueba de humo de toda la
// cadena (CORS → JWT → handler) sin tocar la base de datos.
func Whoami(c *echo.Context) error {
	claims, ok := FromContext(c.Request().Context())
	if !ok {
		return Unauthorized(c)
	}
	return c.JSON(http.StatusOK, map[string]string{
		"id":    claims.Subject,
		"email": claims.Email,
		"aal":   claims.AAL,
	})
}
