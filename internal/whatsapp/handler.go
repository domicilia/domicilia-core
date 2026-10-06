package whatsapp

import (
	"errors"
	"io"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Handler expone el webhook de Meta.
type Handler struct{ ingest *Ingest }

// NewHandler crea el handler.
func NewHandler(ingest *Ingest) *Handler { return &Handler{ingest: ingest} }

// WebhookPath es la ruta que se registra en la app de Meta. Cuelga de /webhooks (y no de /v1) porque no la
// llama un usuario: la llama Meta, y se autentica con firma, no con un JWT.
const WebhookPath = "/webhooks/whatsapp"

// Register registra las rutas del webhook en el servidor raíz.
func (h *Handler) Register(e *echo.Echo) {
	e.GET(WebhookPath, h.verify)
	e.POST(WebhookPath, h.receive)
}

// verify responde el desafío con el que Meta comprueba la suscripción.
func (h *Handler) verify(c *echo.Context) error {
	if !h.ingest.Configured() {
		return apperr.Unavailable("el webhook de WhatsApp no está configurado")
	}
	q := c.QueryParams()
	challenge, ok := h.ingest.VerifyChallenge(q.Get("hub.mode"), q.Get("hub.verify_token"), q.Get("hub.challenge"))
	if !ok {
		return apperr.Forbidden("verificación rechazada")
	}
	return c.String(http.StatusOK, challenge)
}

// receive procesa una entrega. 200 solo si quedó procesada: ante un error nuestro se responde 5xx para que Meta
// reintente (el procesamiento es idempotente).
func (h *Handler) receive(c *echo.Context) error {
	// Se lee el cuerpo EXACTO: la firma se calcula sobre esos bytes, no sobre una reserialización.
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return apperr.Invalid("no se pudo leer el cuerpo")
	}
	err = h.ingest.Handle(c.Request().Context(), body, c.Request().Header.Get("X-Hub-Signature-256"))
	switch {
	case err == nil:
		return c.NoContent(http.StatusOK)
	case errors.Is(err, ErrBadSignature):
		return apperr.Unauthorized("firma inválida")
	case errors.Is(err, ErrBadPayload):
		return apperr.Invalid("carga inválida")
	}
	return err // 500: el manejador central registra el detalle y no lo expone
}
