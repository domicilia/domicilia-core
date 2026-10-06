package orders

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone el carrito y los pedidos por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
//
// Dos familias de rutas, cada una con su propia autorización (ver el comentario de orders.go):
//   - /organizations/{org_id}/cart/*: el cliente arma y confirma SU carrito. No exige ningún
//     permiso de organización — solo que exista y esté activa.
//   - /organizations/{org_id}/orders/*: el negocio gestiona lo que le llegó. Exige
//     org.orders.read / org.orders.manage.
//   - /orders/mine, /orders/{id}, /orders/{id}/cancel, /orders/{id}/retry-payment: el cliente ve,
//     cancela y reintenta el pago de SUS pedidos ya confirmados, sin importar de qué organización
//     — por eso no cuelgan de /organizations/{org_id}. Pagar de verdad (POST .../pay) vive en
//     internal/payments, no aquí: orders solo conoce la máquina de estados, no la pasarela.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/cart", h.getCart)
	g.POST("/organizations/:org_id/cart/items", h.addItem)
	g.PATCH("/organizations/:org_id/cart/items/:item_id", h.updateItemQuantity)
	g.DELETE("/organizations/:org_id/cart/items/:item_id", h.removeItem)
	g.POST("/organizations/:org_id/cart/place", h.place)

	g.GET("/orders/mine", h.myOrders)
	g.GET("/carts/mine", h.myDrafts)
	g.GET("/orders/:order_id", h.getMyOrder)
	g.POST("/orders/:order_id/cancel", h.cancelMyOrder)
	g.POST("/orders/:order_id/retry-payment", h.retryPayment)

	g.GET("/organizations/:org_id/orders", h.listForOrg)
	g.GET("/organizations/:org_id/orders/:order_id", h.getForOrg)
	g.POST("/organizations/:org_id/orders/:order_id/accept", h.accept)
	g.POST("/organizations/:org_id/orders/:order_id/reject", h.reject)
	g.POST("/organizations/:org_id/orders/:order_id/start-preparing", h.startPreparing)
	g.POST("/organizations/:org_id/orders/:order_id/dispatch", h.dispatch)
	g.POST("/organizations/:org_id/orders/:order_id/mark-delivered", h.markDelivered)
	g.POST("/organizations/:org_id/orders/:order_id/cancel", h.cancelForOrg)
}

func actorOrg(c *echo.Context) (identity.Principal, uuid.UUID, error) {
	p, err := identity.Current(c)
	if err != nil {
		return identity.Principal{}, uuid.Nil, err
	}
	org, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return identity.Principal{}, uuid.Nil, err
	}
	return p, org, nil
}

// ---------------------------------------------------------------------------
// Carrito (lado del cliente)
// ---------------------------------------------------------------------------

func (h *Handler) getCart(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	out, err := h.svc.GetCart(c.Request().Context(), p, org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

type addItemRequest struct {
	ProductID uuid.UUID   `json:"product_id"`
	VariantID uuid.UUID   `json:"variant_id"`
	Quantity  int         `json:"quantity"`
	OptionIDs []uuid.UUID `json:"option_ids"`
}

func (h *Handler) addItem(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in addItemRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.AddItem(c.Request().Context(), p, org, AddItemInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

type quantityRequest struct {
	Quantity int `json:"quantity"`
}

func (h *Handler) updateItemQuantity(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	itemID, err := httpserver.UUIDParam(c, "item_id")
	if err != nil {
		return err
	}
	var in quantityRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.UpdateItemQuantity(c.Request().Context(), p, org, itemID, in.Quantity)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) removeItem(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	itemID, err := httpserver.UUIDParam(c, "item_id")
	if err != nil {
		return err
	}
	out, err := h.svc.RemoveItem(c.Request().Context(), p, org, itemID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) place(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Place(c.Request().Context(), p, org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Mis pedidos (lado del cliente, cruza organizaciones)
// ---------------------------------------------------------------------------

func (h *Handler) myOrders(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	page, err := httpserver.PageParams(c)
	if err != nil {
		return err
	}
	rows, total, err := h.svc.MyOrders(c.Request().Context(), p, page.Limit, page.Offset)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(rows, total, page))
}

func (h *Handler) myDrafts(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	rows, err := h.svc.MyDrafts(c.Request().Context(), p)
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []DraftSummary{}
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *Handler) getMyOrder(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "order_id")
	if err != nil {
		return err
	}
	out, err := h.svc.GetMyOrder(c.Request().Context(), p, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) cancelMyOrder(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "order_id")
	if err != nil {
		return err
	}
	out, err := h.svc.CancelMyOrder(c.Request().Context(), p, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) retryPayment(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "order_id")
	if err != nil {
		return err
	}
	out, err := h.svc.RetryPayment(c.Request().Context(), p, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Pedidos (lado del negocio)
// ---------------------------------------------------------------------------

func (h *Handler) listForOrg(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	page, err := httpserver.PageParams(c)
	if err != nil {
		return err
	}
	var status *Status
	if s := c.QueryParam("status"); s != "" {
		v := Status(s)
		status = &v
	}
	rows, total, err := h.svc.ListForOrg(c.Request().Context(), p, org, status, page.Limit, page.Offset)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(rows, total, page))
}

func orderIDParam(c *echo.Context) (uuid.UUID, error) { return httpserver.UUIDParam(c, "order_id") }

func (h *Handler) getForOrg(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := orderIDParam(c)
	if err != nil {
		return err
	}
	out, err := h.svc.GetForOrg(c.Request().Context(), p, org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// accept, reject, startPreparing, dispatch, markDelivered, cancelForOrg, markPaid y
// markPaymentFailed comparten la misma forma (actor + org + id → pedido actualizado): cada una
// solo dice qué método del servicio llamar — ver doTransition.
func (h *Handler) accept(c *echo.Context) error { return h.doTransition(c, h.svc.Accept) }
func (h *Handler) reject(c *echo.Context) error { return h.doTransition(c, h.svc.Reject) }
func (h *Handler) startPreparing(c *echo.Context) error {
	return h.doTransition(c, h.svc.StartPreparing)
}
func (h *Handler) dispatch(c *echo.Context) error      { return h.doTransition(c, h.svc.Dispatch) }
func (h *Handler) markDelivered(c *echo.Context) error { return h.doTransition(c, h.svc.MarkDelivered) }
func (h *Handler) cancelForOrg(c *echo.Context) error  { return h.doTransition(c, h.svc.CancelForOrg) }

// doTransition es el cuerpo común de toda acción del negocio sobre un pedido: leer actor + org +
// id, llamar a fn, devolver el pedido actualizado.
func (h *Handler) doTransition(c *echo.Context, fn func(context.Context, identity.Principal, uuid.UUID, uuid.UUID) (Order, error)) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := orderIDParam(c)
	if err != nil {
		return err
	}
	out, err := fn(c.Request().Context(), p, org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}
