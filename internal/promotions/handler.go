package promotions

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone las promociones por HTTP: el CRUD del negocio y, sobre el carrito del cliente
// (mismo prefijo que internal/orders, /organizations/:org_id/cart), aplicar/quitar un código. Ver
// el comentario del paquete: promotions actúa sobre el carrito a través de OrdersGateway, nunca al
// revés, así que estas rutas viven aquí y no en orders.Handler.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/promotions", h.list)
	g.POST("/organizations/:org_id/promotions", h.create)
	g.GET("/organizations/:org_id/promotions/:promotion_id", h.get)
	g.PATCH("/organizations/:org_id/promotions/:promotion_id", h.update)

	g.POST("/organizations/:org_id/cart/promotion", h.applyCode)
	g.DELETE("/organizations/:org_id/cart/promotion", h.removeCode)
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

type newPromotionRequest struct {
	Code             string     `json:"code"`
	DiscountType     string     `json:"discount_type"`
	Value            int32      `json:"value"`
	MinOrderCents    int32      `json:"min_order_cents"`
	StartsAt         *time.Time `json:"starts_at"`
	EndsAt           *time.Time `json:"ends_at"`
	MaxUses          *int       `json:"max_uses"`
	PerCustomerLimit *int       `json:"per_customer_limit"`
}

type updatePromotionRequest struct {
	Code             *string                     `json:"code"`
	DiscountType     *string                     `json:"discount_type"`
	Value            *int32                      `json:"value"`
	MinOrderCents    *int32                      `json:"min_order_cents"`
	StartsAt         httpserver.Field[time.Time] `json:"starts_at"`
	EndsAt           httpserver.Field[time.Time] `json:"ends_at"`
	MaxUses          httpserver.Field[int]       `json:"max_uses"`
	PerCustomerLimit httpserver.Field[int]       `json:"per_customer_limit"`
	IsActive         *bool                       `json:"is_active"`
}

type applyCodeRequest struct {
	Code string `json:"code"`
}

func (h *Handler) list(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	rows, err := h.svc.List(c.Request().Context(), p, org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *Handler) create(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in newPromotionRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Create(c.Request().Context(), p, org, NewPromotion(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) get(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "promotion_id")
	if err != nil {
		return err
	}
	out, err := h.svc.Get(c.Request().Context(), p, org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) update(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "promotion_id")
	if err != nil {
		return err
	}
	var in updatePromotionRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Update(c.Request().Context(), p, org, id, Patch{
		Code: in.Code, DiscountType: in.DiscountType, Value: in.Value, MinOrderCents: in.MinOrderCents,
		SetStartsAt: in.StartsAt.Set, StartsAt: in.StartsAt.Value,
		SetEndsAt: in.EndsAt.Set, EndsAt: in.EndsAt.Value,
		SetMaxUses: in.MaxUses.Set, MaxUses: in.MaxUses.Value,
		SetPerCustomerLimit: in.PerCustomerLimit.Set, PerCustomerLimit: in.PerCustomerLimit.Value,
		IsActive: in.IsActive,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) applyCode(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in applyCodeRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.ApplyCode(c.Request().Context(), p, org, in.Code)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) removeCode(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	out, err := h.svc.RemoveCode(c.Request().Context(), p, org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}
