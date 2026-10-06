package payments

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone los pagos por HTTP. El webhook de la pasarela es OTRO handler (webhook.go): va
// en la raíz, sin JWT, autenticado con la firma de la pasarela — nunca en este grupo de negocio.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas de negocio. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.POST("/organizations/:org_id/orders/:order_id/pay", h.pay)
	g.GET("/organizations/:org_id/orders/:order_id/payments", h.listForOrder)
}

func (h *Handler) pay(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orderID, err := httpserver.UUIDParam(c, "order_id")
	if err != nil {
		return err
	}
	out, err := h.svc.Initiate(c.Request().Context(), p, orderID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) listForOrder(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	org, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	orderID, err := httpserver.UUIDParam(c, "order_id")
	if err != nil {
		return err
	}
	rows, err := h.svc.ListForOrder(c.Request().Context(), p, org, orderID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rows)
}
