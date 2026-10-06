package apperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

func TestCadaClaseTieneSuCodigoHTTP(t *testing.T) {
	tests := []struct {
		name string
		err  *apperr.Error
		want int
	}{
		{"entrada inválida", apperr.Invalid("x"), http.StatusUnprocessableEntity},
		{"sin credenciales", apperr.Unauthorized("x"), http.StatusUnauthorized},
		{"sin permiso", apperr.Forbidden("x"), http.StatusForbidden},
		{"no existe", apperr.NotFound("x"), http.StatusNotFound},
		{"choca con el estado", apperr.Conflict("x"), http.StatusConflict},
		{"el plan no lo incluye", apperr.PaymentRequired("x"), http.StatusPaymentRequired},
		{"falló un servicio externo", apperr.Upstream("x", errors.New("causa")), http.StatusBadGateway},
		{"función sin configurar", apperr.Unavailable("x"), http.StatusServiceUnavailable},
		{"clase desconocida: falla cerrado", &apperr.Error{Message: "x"}, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.StatusCode(); got != tc.want {
				t.Fatalf("StatusCode = %d, quería %d", got, tc.want)
			}
		})
	}
}

func TestLaCausaSeConservaParaElLogPeroNoEnElMensaje(t *testing.T) {
	causa := errors.New("conexión rechazada en 10.0.0.5")
	err := apperr.Upstream("no se pudo crear la cuenta", causa)

	if err.Message != "no se pudo crear la cuenta" {
		t.Fatalf("Message = %q: es lo único que ve el cliente", err.Message)
	}
	if !errors.Is(err, causa) {
		t.Fatal("errors.Is debe alcanzar la causa")
	}
	if got := err.Error(); got != "no se pudo crear la cuenta: conexión rechazada en 10.0.0.5" {
		t.Fatalf("Error() = %q: para el log debe incluir la causa", got)
	}
}

func TestIsReconoceLaClaseAunEnvuelta(t *testing.T) {
	envuelto := fmt.Errorf("servicio: %w", apperr.NotFound("no está"))

	if !apperr.Is(envuelto, apperr.KindNotFound) {
		t.Fatal("debía reconocer NotFound envuelto")
	}
	if apperr.Is(envuelto, apperr.KindConflict) {
		t.Fatal("no debía confundir las clases")
	}
	if apperr.Is(errors.New("otro"), apperr.KindNotFound) {
		t.Fatal("un error ajeno no es de ninguna clase")
	}
	if apperr.Is(nil, apperr.KindNotFound) {
		t.Fatal("nil no es de ninguna clase")
	}
}
