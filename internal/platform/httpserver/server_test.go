package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

func newServer(t *testing.T, ready func(context.Context) error) *echo.Echo {
	t.Helper()
	return httpserver.New(httpserver.Options{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: time.Second,
		MaxBodyBytes:   1024,
		Ready:          ready,
	})
}

func do(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) httpserver.Problem {
	t.Helper()
	var p httpserver.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("el cuerpo no es un Problem JSON: %v (%q)", err, rec.Body.String())
	}
	return p
}

func TestHealthz(t *testing.T) {
	rec := do(newServer(t, nil), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, quería 200", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name  string
		ready func(context.Context) error
		want  int
	}{
		{"sin comprobación", nil, http.StatusOK},
		{"dependencia ok", func(context.Context) error { return nil }, http.StatusOK},
		{"dependencia caída", func(context.Context) error { return errors.New("db down") }, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newServer(t, tc.ready), http.MethodGet, "/readyz", "")
			if rec.Code != tc.want {
				t.Fatalf("readyz = %d, quería %d", rec.Code, tc.want)
			}
		})
	}
}

func TestNotFoundEsProblemJSON(t *testing.T) {
	rec := do(newServer(t, nil), http.MethodGet, "/no-existe", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("código = %d, quería 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	p := decodeProblem(t, rec)
	if p.Status != http.StatusNotFound || p.Instance != "/no-existe" {
		t.Fatalf("problem inesperado: %+v", p)
	}
	if p.RequestID == "" {
		t.Fatal("falta request_id en el error")
	}
}

func TestRequestIDSePropaga(t *testing.T) {
	e := newServer(t, nil)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	req.Header.Set(echo.HeaderXRequestID, "abc-123")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get(echo.HeaderXRequestID); got != "abc-123" {
		t.Fatalf("X-Request-ID = %q, quería abc-123", got)
	}
}

func TestPanicNoFiltraDetalle(t *testing.T) {
	e := newServer(t, nil)
	e.GET("/boom", func(*echo.Context) error { panic("secreto-interno") })

	rec := do(e, http.MethodGet, "/boom", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("código = %d, quería 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secreto-interno") {
		t.Fatalf("el 500 filtra el detalle interno: %s", rec.Body.String())
	}
	if p := decodeProblem(t, rec); p.Detail != "" {
		t.Fatalf("un 5xx no debe traer detail: %+v", p)
	}
}

func TestCuerpoDemasiadoGrande(t *testing.T) {
	e := newServer(t, nil)
	e.POST("/eco", func(c *echo.Context) error {
		if _, err := io.ReadAll(c.Request().Body); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	rec := do(e, http.MethodPost, "/eco", strings.Repeat("x", 4096))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("código = %d, quería 413", rec.Code)
	}
}

func TestV1AplicaTimeoutSinAfectarSalud(t *testing.T) {
	e := newServer(t, nil)
	httpserver.V1(e, 50*time.Millisecond).GET("/lento", func(c *echo.Context) error {
		select {
		case <-c.Request().Context().Done():
			return c.Request().Context().Err()
		case <-time.After(time.Second):
			return c.NoContent(http.StatusNoContent)
		}
	})

	start := time.Now()
	rec := do(e, http.MethodGet, "/v1/lento", "")

	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("el timeout de /v1 no cortó la petición lenta")
	}
	if rec.Code < http.StatusInternalServerError {
		t.Fatalf("código = %d, esperaba un 5xx por timeout", rec.Code)
	}
}

func TestReadyzLogueaConNuestroLogger(t *testing.T) {
	var buf bytes.Buffer
	e := httpserver.New(httpserver.Options{
		Logger:       slog.New(slog.NewTextHandler(&buf, nil)),
		MaxBodyBytes: 1024,
		Ready:        func(context.Context) error { return errors.New("db down") },
	})

	do(e, http.MethodGet, "/readyz", "")

	// El handler de texto escribe `level=WARN`; el logger por defecto de Echo
	// escribiría JSON y este buffer quedaría vacío.
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("el aviso no pasó por nuestro logger: %q", buf.String())
	}
}
