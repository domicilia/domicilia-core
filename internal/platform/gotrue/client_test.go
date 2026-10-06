package gotrue_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
)

const secret = "0123456789abcdef0123456789abcdef" // gitleaks:allow — valor falso de prueba

// admin simula el admin API de auth-domicilia y verifica lo que el cliente manda.
func admin(t *testing.T, handler http.HandlerFunc) *gotrue.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkServiceToken(t, r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return gotrue.New(srv.URL, secret, nil)
}

// checkServiceToken exige que la petición lleve un token `service_role` HS256,
// firmado con el secreto compartido y de vida corta: es lo único que acepta el
// admin API, y un token de otra forma sería rechazado.
func checkServiceToken(t *testing.T, r *http.Request) {
	t.Helper()
	raw := r.Header.Get("Authorization")
	if len(raw) < 8 || raw[:7] != "Bearer " {
		t.Errorf("Authorization = %q, quería Bearer", raw)
		return
	}
	var claims jwt.MapClaims
	tok, err := jwt.ParseWithClaims(raw[7:], &claims, func(*jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !tok.Valid {
		t.Errorf("token de servicio inválido: %v", err)
		return
	}
	if claims["role"] != "service_role" {
		t.Errorf("role = %v, quería service_role", claims["role"])
	}
	exp, _ := claims.GetExpirationTime()
	if exp == nil || time.Until(exp.Time) > 6*time.Minute {
		t.Errorf("el token de servicio debe vivir pocos minutos, exp = %v", exp)
	}
}

func TestCreateUser(t *testing.T) {
	id := uuid.New()
	c := admin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/users" {
			t.Errorf("%s %s, quería POST /admin/users", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("cuerpo: %v", err)
		}
		if body["email"] != "ana@ejemplo.com" || body["password"] != "temporal-123" || body["email_confirm"] != true {
			t.Errorf("cuerpo = %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "email": "ana@ejemplo.com"})
	})

	u, err := c.CreateUser(context.Background(), "ana@ejemplo.com", "temporal-123")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID != id || u.Email != "ana@ejemplo.com" {
		t.Fatalf("usuario = %+v", u)
	}
}

func TestCreateUserRechazado(t *testing.T) {
	c := admin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":422,"error_code":"email_exists","msg":"A user with this email address has already been registered"}`))
	})

	_, err := c.CreateUser(context.Background(), "ana@ejemplo.com", "x")
	var ge *gotrue.Error
	if !errors.As(err, &ge) || ge.Status != http.StatusUnprocessableEntity {
		t.Fatalf("debía ser *gotrue.Error 422, fue %v", err)
	}
	if !gotrue.IsEmailExists(err) {
		t.Fatal("IsEmailExists debía reconocer el correo ya registrado")
	}
}

func TestIsEmailExistsNoConfundeOtrosErrores(t *testing.T) {
	c := admin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error_code":"unexpected_failure"}`))
	})
	_, err := c.CreateUser(context.Background(), "ana@ejemplo.com", "x")
	if err == nil || gotrue.IsEmailExists(err) {
		t.Fatalf("un 500 no es email_exists: %v", err)
	}
	if gotrue.IsEmailExists(errors.New("otro")) || gotrue.IsEmailExists(nil) {
		t.Fatal("un error ajeno no es email_exists")
	}
}

func TestUnaRespuestaQueNoEsJSONEsUnError(t *testing.T) {
	c := admin(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>proxy</html>")) })
	if _, err := c.CreateUser(context.Background(), "ana@ejemplo.com", "x"); err == nil {
		t.Fatal("una respuesta 200 que no es JSON debía ser un error")
	}
}

func TestUserByEmailExigeElCorreoExacto(t *testing.T) {
	id := uuid.New()
	c := admin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/admin/users" || r.URL.Query().Get("filter") != "ana@ejemplo.com" {
			t.Errorf("%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		// El filtro de GoTrue es por coincidencia parcial: devuelve también a otras personas.
		_ = json.NewEncoder(w).Encode(map[string]any{"users": []map[string]any{
			{"id": uuid.New(), "email": "mariana@ejemplo.com"},
			{"id": id, "email": "ana@ejemplo.com"},
		}})
	})

	u, err := c.UserByEmail(context.Background(), "ana@ejemplo.com")
	if err != nil || u == nil || u.ID != id {
		t.Fatalf("UserByEmail = %+v, %v; quería la coincidencia exacta", u, err)
	}
}

func TestUserByEmailInexistente(t *testing.T) {
	c := admin(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"users": []map[string]any{{"id": uuid.New(), "email": "mariana@ejemplo.com"}}})
	})
	u, err := c.UserByEmail(context.Background(), "ana@ejemplo.com")
	if err != nil || u != nil {
		t.Fatalf("UserByEmail = %+v, %v; quería (nil, nil) porque no hay coincidencia exacta", u, err)
	}
}

func TestUserByEmailEscapaElCorreo(t *testing.T) {
	c := admin(t, func(w http.ResponseWriter, r *http.Request) {
		// Un correo con + o & no debe colarse como otro parámetro.
		if got := r.URL.Query().Get("filter"); got != "a+b&x=1@ejemplo.com" {
			t.Errorf("filter = %q", got)
		}
		if r.URL.Query().Has("x") {
			t.Error("el correo inyectó un parámetro")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"users": []any{}})
	})
	if _, err := c.UserByEmail(context.Background(), "a+b&x=1@ejemplo.com"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteUser(t *testing.T) {
	id := uuid.New()
	var called bool
	c := admin(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodDelete || r.URL.Path != "/admin/users/"+id.String() {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.DeleteUser(context.Background(), id); err != nil || !called {
		t.Fatalf("DeleteUser: %v (llamado=%v)", err, called)
	}
}

func TestErrorDeRedYCancelacion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // ya nadie escucha

	c := gotrue.New(url, secret, nil)
	if _, err := c.CreateUser(context.Background(), "ana@ejemplo.com", "x"); err == nil {
		t.Fatal("con el servidor caído debía dar error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slow := admin(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := slow.CreateUser(ctx, "ana@ejemplo.com", "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("un contexto cancelado debía cortar la llamada: %v", err)
	}
}

func TestContrasenaTemporal(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		p, err := gotrue.NewTemporaryPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 24 {
			t.Fatalf("len = %d, quería 24 (18 bytes en base64 url)", len(p))
		}
		if seen[p] {
			t.Fatal("contraseña repetida: no es aleatoria")
		}
		seen[p] = true
	}
}
