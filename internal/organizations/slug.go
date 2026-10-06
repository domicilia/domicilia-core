package organizations

import (
	"errors"
	"regexp"
	"strings"
)

// Límites del slug. 63 es el máximo de una etiqueta DNS: el día que cada negocio
// tenga su subdominio, el slug ya cabe.
const (
	minSlug = 3
	maxSlug = 63
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedSlugs son nombres que no pueden ser un inquilino: el primer segmento de la
// URL del frontend es el slug (/{slug}/admin/...), así que uno de estos taparía una
// ruta propia de la aplicación. Incluye lo de lib/routing/reserved.ts del frontend.
var reservedSlugs = map[string]struct{}{
	"login": {}, "signup": {}, "platform": {}, "orders": {}, "payments": {}, "errors": {},
	"auth": {}, "api": {}, "domi": {}, "negocios": {}, "cliente": {}, "r": {}, "pedidos": {},
	"pagos": {}, "errores": {}, "recuperar": {}, "cuenta": {},
	"admin": {}, "www": {}, "app": {}, "static": {}, "public": {}, "v1": {}, "webhooks": {},
	"healthz": {}, "readyz": {}, "docs": {}, "support": {}, "soporte": {},
}

// fold quita los acentos del español y del portugués.
var fold = map[rune]rune{
	'á': 'a', 'à': 'a', 'ä': 'a', 'â': 'a', 'ã': 'a',
	'é': 'e', 'è': 'e', 'ë': 'e', 'ê': 'e',
	'í': 'i', 'ì': 'i', 'ï': 'i', 'î': 'i',
	'ó': 'o', 'ò': 'o', 'ö': 'o', 'ô': 'o', 'õ': 'o',
	'ú': 'u', 'ù': 'u', 'ü': 'u', 'û': 'u',
	'ñ': 'n', 'ç': 'c',
}

// Slugify deriva un slug del nombre: minúsculas, sin acentos, y todo lo que no sea
// letra o dígito pasa a un guion (sin repetirlos ni dejarlos en los extremos).
func Slugify(name string) string {
	var b strings.Builder
	lastDash := true // evita un guion al principio
	for _, r := range strings.ToLower(name) {
		if m, ok := fold[r]; ok {
			r = m
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	s := b.String()
	if len(s) > maxSlug {
		s = s[:maxSlug]
	}
	return strings.Trim(s, "-")
}

// ValidateSlug comprueba un slug ya escrito: forma, largo y palabras reservadas.
func ValidateSlug(slug string) error {
	switch {
	case len(slug) < minSlug || len(slug) > maxSlug:
		return errors.New("el slug debe tener entre 3 y 63 caracteres")
	case !slugPattern.MatchString(slug):
		return errors.New("el slug solo admite minúsculas, dígitos y guiones, sin guiones al principio, al final ni repetidos")
	}
	if _, reserved := reservedSlugs[slug]; reserved {
		return errors.New("ese slug está reservado")
	}
	return nil
}
