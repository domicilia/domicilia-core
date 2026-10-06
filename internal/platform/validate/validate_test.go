package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

func TestEmail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" = debe rechazarse
	}{
		{"correo normal", "ana@ejemplo.com", "ana@ejemplo.com"},
		{"se normaliza a minúsculas", "Ana.Perez@Ejemplo.COM", "ana.perez@ejemplo.com"},
		{"se recortan los espacios", "  ana@ejemplo.com  ", "ana@ejemplo.com"},
		{"subdominios", "ana@mail.ejemplo.com.co", "ana@mail.ejemplo.com.co"},
		{"con +etiqueta", "ana+pedidos@ejemplo.com", "ana+pedidos@ejemplo.com"},

		{"vacío", "", ""},
		{"solo espacios", "   ", ""},
		{"sin arroba", "ana.ejemplo.com", ""},
		{"sin parte local", "@ejemplo.com", ""},
		{"sin dominio", "ana@", ""},
		{"dominio sin punto", "ana@localhost", ""},
		{"dominio que empieza con punto", "ana@.com", ""},
		{"dominio que termina con punto", "ana@ejemplo.com.", ""},
		{"con nombre y corchetes", "Ana <ana@ejemplo.com>", ""},
		{"dos direcciones", "a@ejemplo.com, b@ejemplo.com", ""},
		{"con espacio adentro", "ana perez@ejemplo.com", ""},
		{"dominio reservado .test", "ana@ejemplo.test", ""},
		{"dominio reservado .local", "ana@servidor.local", ""},
		{"dominio reservado .invalid", "ana@ejemplo.invalid", ""},
		{"dominio reservado .example", "ana@ejemplo.example", ""},
		{"más de 254 caracteres", strings.Repeat("a", 250) + "@ejemplo.com", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validate.Email("email", tc.in)
			if tc.want == "" {
				if !apperr.Is(err, apperr.KindInvalid) {
					t.Fatalf("Email(%q) debía dar un error de entrada inválida, dio %q, %v", tc.in, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Email(%q) = %q, %v; quería %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestEmailNombraElCampoEnElError(t *testing.T) {
	_, err := validate.Email("correo", "no-es-correo")
	var ae *apperr.Error
	if !errors.As(err, &ae) || !strings.Contains(ae.Message, "correo") {
		t.Fatalf("el mensaje debe nombrar el campo: %v", err)
	}
}

func TestRequired(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		max     int
		want    string
		wantErr bool
	}{
		{"texto normal", "Juan", 10, "Juan", false},
		{"se recorta", "  Juan  ", 10, "Juan", false},
		{"vacío", "", 10, "", true},
		{"solo espacios", "   ", 10, "", true},
		{"justo en el límite", "abcde", 5, "abcde", false},
		{"se pasa del límite", "abcdef", 5, "", true},
		// Los límites son de caracteres, no de bytes: "ñ" ocupa 2 bytes.
		{"cuenta caracteres y no bytes", strings.Repeat("ñ", 5), 5, strings.Repeat("ñ", 5), false},
		{"el límite se aplica al texto recortado", "  abcde  ", 5, "abcde", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validate.Required("campo", tc.in, tc.max)
			if tc.wantErr {
				if !apperr.Is(err, apperr.KindInvalid) {
					t.Fatalf("debía dar error de entrada inválida, dio %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Required = %q, %v; quería %q", got, err, tc.want)
			}
		})
	}
}

func TestRequiredMin(t *testing.T) {
	if _, err := validate.RequiredMin("phone", "1234", 5, 50); !apperr.Is(err, apperr.KindInvalid) {
		t.Fatalf("4 caracteres con mínimo 5 debía rechazarse: %v", err)
	}
	if got, err := validate.RequiredMin("phone", " 30012 ", 5, 50); err != nil || got != "30012" {
		t.Fatalf("RequiredMin = %q, %v", got, err)
	}
	if _, err := validate.RequiredMin("phone", strings.Repeat("9", 51), 5, 50); !apperr.Is(err, apperr.KindInvalid) {
		t.Fatalf("51 caracteres con máximo 50 debía rechazarse: %v", err)
	}
}

func TestOptionalMaxLen(t *testing.T) {
	if err := validate.OptionalMaxLen("x", nil, 3); err != nil {
		t.Fatalf("un opcional ausente es válido: %v", err)
	}
	ok, largo := "abc", "abcd"
	if err := validate.OptionalMaxLen("x", &ok, 3); err != nil {
		t.Fatalf("en el límite es válido: %v", err)
	}
	if err := validate.OptionalMaxLen("x", &largo, 3); !apperr.Is(err, apperr.KindInvalid) {
		t.Fatalf("pasado del límite debía rechazarse: %v", err)
	}
}
