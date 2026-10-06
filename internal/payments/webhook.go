package payments

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// WebhookHandler expone el webhook de la pasarela de pago. Va en la raíz (/webhooks), no en /v1:
// no lo llama un usuario de negocio, lo llama la pasarela — y se autentica con su propia firma,
// no con un JWT. Mismo criterio que internal/whatsapp.
type WebhookHandler struct{ svc *Service }

// NewWebhookHandler crea el handler del webhook.
func NewWebhookHandler(svc *Service) *WebhookHandler { return &WebhookHandler{svc: svc} }

// WebhookPath es la ruta que se registra en el panel de la pasarela.
const WebhookPath = "/webhooks/payments"

// Register registra el webhook en el servidor raíz.
func (h *WebhookHandler) Register(e *echo.Echo) {
	e.POST(WebhookPath, h.receive)
}

// receive procesa una notificación de la pasarela. 200 solo si quedó procesada (o ya lo estaba):
// ante un error nuestro se responde 5xx para que la pasarela reintente — HandleWebhook es
// idempotente.
func (h *WebhookHandler) receive(c *echo.Context) error {
	if h.svc.gateway == nil {
		return apperr.Unavailable("los pagos no están configurados en este servidor")
	}
	event, err := h.svc.gateway.ParseWebhook(c.Request())
	switch {
	case errors.Is(err, ErrBadSignature):
		return apperr.Unauthorized("firma inválida")
	case errors.Is(err, ErrBadPayload):
		return apperr.Invalid("carga inválida")
	case err != nil:
		return err
	}
	if err := h.svc.HandleWebhook(c.Request().Context(), h.svc.gateway.Name(), event); err != nil {
		return err // 500: la pasarela reintenta, HandleWebhook es idempotente
	}
	return c.NoContent(http.StatusOK)
}
