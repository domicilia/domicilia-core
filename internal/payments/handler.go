package payments

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
	"github.com/domicilia/domicilia-core/internal/pricing"
)

// Handler expone los pagos por HTTP. El webhook de la pasarela es OTRO handler (webhook.go): va
// en la raíz, sin JWT, autenticado con la firma de la pasarela — nunca en este grupo de negocio.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas de negocio. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/orders/:order_id/payment-quote", h.quote)
	g.POST("/organizations/:org_id/orders/:order_id/pay", h.pay)
	g.GET("/organizations/:org_id/orders/:order_id/payments", h.listForOrder)
}

// quote es el desglose ANTES de pagar: GET ...?method=card|card_international|pse|wallet.
func (h *Handler) quote(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orderID, err := httpserver.UUIDParam(c, "order_id")
	if err != nil {
		return err
	}
	out, err := h.svc.Quote(c.Request().Context(), p, orderID, pricing.Method(c.QueryParam("method")))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// payRequest: el medio de pago y el total que el cliente vio y aceptó (ver Service.Quote).
type payRequest struct {
	Method             string `json:"method"`
	ExpectedTotalCents int64  `json:"expected_total_cents"`
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
	var in payRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Initiate(c.Request().Context(), p, orderID, InitiateInput{
		Method: pricing.Method(in.Method), ExpectedTotalCents: in.ExpectedTotalCents,
	})
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
