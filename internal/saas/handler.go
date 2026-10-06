package saas

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone los controles de plataforma por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas /platform, todas solo para el superadmin. g ya
// exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/platform/overview", h.overview, identity.Require(access.PlatformOverviewRead))
	g.PATCH("/platform/organizations/:org_id", h.updateOrganization, identity.Require(access.PlatformOrganizationsManage))
	g.PATCH("/platform/users/:user_id/delivery", h.setDelivery, identity.Require(access.PlatformRolesManage))
}

type updateOrganizationRequest struct {
	IsActive *bool   `json:"is_active"`
	PlanTier *string `json:"plan_tier"`
}

type deliveryRequest struct {
	IsDelivery *bool `json:"is_delivery"`
}

func (h *Handler) overview(c *echo.Context) error {
	o, err := h.svc.Overview(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, o)
}

func (h *Handler) updateOrganization(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	var in updateOrganizationRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	org, err := h.svc.UpdateOrganization(c.Request().Context(), p, id, in.IsActive, in.PlanTier)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, org)
}

func (h *Handler) setDelivery(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	var in deliveryRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	if in.IsDelivery == nil {
		return apperr.Invalid("is_delivery es obligatorio")
	}
	u, err := h.svc.SetDelivery(c.Request().Context(), p, id, *in.IsDelivery)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}
