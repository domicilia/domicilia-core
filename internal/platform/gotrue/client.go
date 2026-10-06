// Package gotrue es un cliente mínimo del admin API de auth-domicilia (GoTrue).
// Sirve para crear la identidad de alguien que entra sin signup público (staff
// invitado, domiciliario aprobado). Para verificar tokens no hay llamada de red:
// eso lo hace el paquete auth, con el secreto compartido.
package gotrue

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	requestTimeout = 10 * time.Second
	tokenTTL       = 5 * time.Minute
	maxBody        = 1 << 20
	// passwordBytes da una contraseña temporal de 24 caracteres (base64 url).
	passwordBytes = 18
)

// Error es una respuesta de rechazo del admin API.
type Error struct {
	Status int
	Code   string // error_code de GoTrue, p. ej. "email_exists"
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("auth-domicilia: %d %s", e.Status, e.Body)
}

// IsEmailExists dice si el rechazo fue porque el correo ya tiene identidad.
func IsEmailExists(err error) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.Code == "email_exists"
}

// User es lo que este servicio usa de un usuario de GoTrue.
type User struct {
	ID    uuid.UUID
	Email string
}

// Client habla con el admin API de GoTrue.
type Client struct {
	baseURL string
	secret  []byte
	http    *http.Client
}

// New crea el cliente. secret es el mismo HS256 con el que GoTrue firma sus
// tokens: con él se firma un token `service_role` de vida corta, que es lo único
// que acepta el admin API (no hay endpoint de login para eso).
func New(baseURL, secret string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	return &Client{baseURL: baseURL, secret: []byte(secret), http: hc}
}

// NewTemporaryPassword genera una contraseña temporal aleatoria.
func NewTemporaryPassword() (string, error) {
	b := make([]byte, passwordBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gotrue: generar contraseña: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (c *Client) serviceToken() (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"role": "service_role",
		"exp":  time.Now().Add(tokenTTL).Unix(),
	})
	s, err := t.SignedString(c.secret)
	if err != nil {
		return "", fmt.Errorf("gotrue: firmar token: %w", err)
	}
	return s, nil
}

// CreateUser crea una cuenta con correo ya confirmado. Devuelve *Error si GoTrue
// la rechaza (incluye el caso de correo ya registrado: ver IsEmailExists).
func (c *Client) CreateUser(ctx context.Context, email, password string) (User, error) {
	body, err := json.Marshal(map[string]any{"email": email, "password": password, "email_confirm": true})
	if err != nil {
		return User{}, fmt.Errorf("gotrue: codificar petición: %w", err)
	}
	var out struct {
		ID    uuid.UUID `json:"id"`
		Email string    `json:"email"`
	}
	if err := c.do(ctx, http.MethodPost, "/admin/users", body, &out); err != nil {
		return User{}, err
	}
	return User{ID: out.ID, Email: out.Email}, nil
}

// UserByEmail busca una cuenta por correo. Devuelve (nil, nil) si no existe.
func (c *Client) UserByEmail(ctx context.Context, email string) (*User, error) {
	var out struct {
		Users []struct {
			ID    uuid.UUID `json:"id"`
			Email string    `json:"email"`
		} `json:"users"`
	}
	if err := c.do(ctx, http.MethodGet, "/admin/users?filter="+url.QueryEscape(email), nil, &out); err != nil {
		return nil, err
	}
	// El filtro de GoTrue es por coincidencia parcial: se exige el correo exacto.
	for _, u := range out.Users {
		if u.Email == email {
			return &User{ID: u.ID, Email: u.Email}, nil
		}
	}
	return nil, nil //nolint:nilnil // "no existe" no es un error
}

// DeleteUser borra una cuenta. Se usa para deshacer un alta cuando falla el
// paso siguiente (guardar el perfil de negocio).
func (c *Client) DeleteUser(ctx context.Context, id uuid.UUID) error {
	return c.do(ctx, http.MethodDelete, "/admin/users/"+id.String(), nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	tok, err := c.serviceToken()
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("gotrue: armar petición: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gotrue: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("gotrue: leer respuesta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		ge := &Error{Status: resp.StatusCode, Body: string(raw)}
		var eb struct {
			Code string `json:"error_code"`
		}
		if json.Unmarshal(raw, &eb) == nil {
			ge.Code = eb.Code
		}
		return ge
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gotrue: respuesta inválida: %w", err)
	}
	return nil
}
