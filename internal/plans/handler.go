package plans

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone los planes por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/plans", h.catalog)
	g.GET("/organizations/:org_id/entitlements", h.entitlements)
	g.GET("/organizations/:org_id/subscription", h.subscriptions)

	manage := identity.Require(access.PlatformOrganizationsManage)
	g.PUT("/platform/organizations/:org_id/plan", h.changePlan, manage)
	g.PUT("/platform/organizations/:org_id/feature-overrides/:feature", h.setOverride, manage)
	g.DELETE("/platform/organizations/:org_id/feature-overrides/:feature", h.clearOverride, manage)
}

type changePlanRequest struct {
	PlanTier string  `json:"plan_tier"`
	Reason   *string `json:"reason"`
}

type overrideRequest struct {
	Enabled *bool   `json:"enabled"`
	Reason  *string `json:"reason"`
}

func (h *Handler) catalog(c *echo.Context) error {
	return c.JSON(http.StatusOK, h.svc.Catalog())
}

func (h *Handler) entitlements(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	e, err := h.svc.Entitlements(c.Request().Context(), p, orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, e)
}

func (h *Handler) subscriptions(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	subs, err := h.svc.Subscriptions(c.Request().Context(), p, orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, subs)
}

func (h *Handler) changePlan(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	var in changePlanRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	sub, err := h.svc.ChangePlan(c.Request().Context(), p, orgID, in.PlanTier, in.Reason)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, sub)
}

func (h *Handler) setOverride(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	var in overrideRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	o, err := h.svc.SetOverride(c.Request().Context(), p, orgID, c.Param("feature"), in.Enabled, in.Reason)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, o)
}

func (h *Handler) clearOverride(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	if err := h.svc.ClearOverride(c.Request().Context(), p, orgID, c.Param("feature")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
