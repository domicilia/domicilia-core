// Package apperr define los errores de negocio que los servicios devuelven sin
// conocer HTTP. El manejador central de httpserver los traduce a un código y a
// un Problem (RFC 9457); así los servicios no dependen del framework web.
package apperr

import (
	"errors"
	"net/http"
)

// Kind clasifica un error de negocio.
type Kind int

// Clases de error, cada una con su código HTTP.
const (
	KindInvalid         Kind = iota + 1 // 422: la entrada no cumple las reglas
	KindUnauthorized                    // 401: no sabemos quién eres
	KindForbidden                       // 403: sabemos quién eres y no puedes
	KindNotFound                        // 404
	KindConflict                        // 409: choca con el estado actual
	KindPaymentRequired                 // 402: el plan de la organización no lo incluye
	KindUpstream                        // 502: falló un servicio del que dependemos
	KindUnavailable                     // 503: esta función no está configurada en el servidor
)

// Error es un error de negocio. Message es lo que ve el cliente en los 4xx; en
// los 5xx nunca se expone (solo se registra), y Err conserva la causa para el log.
type Error struct {
	Kind    Kind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Unwrap permite errors.Is/As sobre la causa.
func (e *Error) Unwrap() error { return e.Err }

// StatusCode implementa echo.HTTPStatusCoder.
func (e *Error) StatusCode() int {
	switch e.Kind {
	case KindInvalid:
		return http.StatusUnprocessableEntity
	case KindUnauthorized:
		return http.StatusUnauthorized
	case KindForbidden:
		return http.StatusForbidden
	case KindNotFound:
		return http.StatusNotFound
	case KindConflict:
		return http.StatusConflict
	case KindPaymentRequired:
		return http.StatusPaymentRequired
	case KindUpstream:
		return http.StatusBadGateway
	case KindUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Invalid: la entrada no cumple las reglas (422).
func Invalid(msg string) *Error { return &Error{Kind: KindInvalid, Message: msg} }

// Unauthorized: credenciales ausentes o inválidas (401).
func Unauthorized(msg string) *Error { return &Error{Kind: KindUnauthorized, Message: msg} }

// Forbidden: identificado, pero sin permiso (403).
func Forbidden(msg string) *Error { return &Error{Kind: KindForbidden, Message: msg} }

// NotFound: el recurso no existe (404).
func NotFound(msg string) *Error { return &Error{Kind: KindNotFound, Message: msg} }

// Conflict: choca con el estado actual (409).
func Conflict(msg string) *Error { return &Error{Kind: KindConflict, Message: msg} }

// PaymentRequired: la función o el límite no están incluidos en el plan (402).
func PaymentRequired(msg string) *Error { return &Error{Kind: KindPaymentRequired, Message: msg} }

// Upstream: falló un servicio externo (502). cause va al log, no al cliente.
func Upstream(msg string, cause error) *Error {
	return &Error{Kind: KindUpstream, Message: msg, Err: cause}
}

// Unavailable: la función necesita una configuración que el servidor no tiene (503).
func Unavailable(msg string) *Error { return &Error{Kind: KindUnavailable, Message: msg} }

// Is dice si err es un *Error de la clase indicada.
func Is(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}
