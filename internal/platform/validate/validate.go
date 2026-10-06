// Package validate agrupa las reglas de entrada compartidas por los dominios.
// Devuelve apperr.Invalid (422) con un mensaje pensado para el cliente.
package validate

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

const maxEmailLen = 254

// reservedTLDs son dominios de uso especial (RFC 6761/6762): no reciben correo
// real, así que un alta con ellos es un error de digitación o una prueba.
var reservedTLDs = map[string]bool{
	"test": true, "local": true, "localhost": true, "invalid": true, "example": true, "onion": true,
}

// Email valida una dirección y la devuelve normalizada en minúsculas. GoTrue
// guarda los correos en minúsculas: normalizar aquí evita dos filas para la
// misma persona.
func Email(field, raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", apperr.Invalid(field + " es obligatorio")
	}
	bad := apperr.Invalid(field + " no es un correo válido")
	if len(s) > maxEmailLen {
		return "", bad
	}
	addr, err := mail.ParseAddress(s)
	// ParseAddress acepta "Nombre <a@b.c>": aquí solo vale la dirección sola.
	if err != nil || addr.Address != s {
		return "", bad
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", bad
	}
	tld := domain[strings.LastIndex(domain, ".")+1:]
	if reservedTLDs[strings.ToLower(tld)] {
		return "", bad
	}
	return strings.ToLower(s), nil
}

// Required exige un texto no vacío (tras recortar espacios) de hasta maxLen
// caracteres, y devuelve el texto recortado.
func Required(field, s string, maxLen int) (string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", apperr.Invalid(field + " es obligatorio")
	}
	return t, MaxLen(field, t, maxLen)
}

// RequiredMin es Required con una longitud mínima.
func RequiredMin(field, s string, minLen, maxLen int) (string, error) {
	t, err := Required(field, s, maxLen)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(t) < minLen {
		return "", apperr.Invalid(fmt.Sprintf("%s debe tener al menos %d caracteres", field, minLen))
	}
	return t, nil
}

// MaxLen limita la longitud en caracteres (no en bytes).
func MaxLen(field, s string, maxLen int) error {
	if utf8.RuneCountInString(s) > maxLen {
		return apperr.Invalid(fmt.Sprintf("%s admite como máximo %d caracteres", field, maxLen))
	}
	return nil
}

// OptionalMaxLen limita la longitud de un texto opcional.
func OptionalMaxLen(field string, s *string, maxLen int) error {
	if s == nil {
		return nil
	}
	return MaxLen(field, *s, maxLen)
}
