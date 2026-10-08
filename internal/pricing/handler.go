package pricing

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone las comisiones y tarifas por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
//
// La administración usa platform.organizations.manage (el mismo permiso que cambia el plan de una
// organización): las comisiones son parte de la relación comercial con cada organización. Un
// permiso propio (platform.payments.manage) queda como deuda técnica — docs/pagos.md §17.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/pricing", h.publicRates)

	manage := identity.Require(access.PlatformOrganizationsManage)
	g.GET("/platform/pricing", h.overview, manage)
	g.PUT("/platform/pricing/settings", h.updateSettings, manage)
	g.PUT("/platform/pricing/plans/:code", h.updatePlan, manage)
	g.POST("/platform/pricing/simulate", h.simulate, manage)
	g.GET("/platform/organizations/:org_id/pricing", h.getOrganization, manage)
	g.PUT("/platform/organizations/:org_id/pricing", h.setOrganization, manage)
}

func (h *Handler) publicRates(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	out, err := h.svc.PublicRatesFor(c.Request().Context(), p, orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) overview(c *echo.Context) error {
	out, err := h.svc.Overview(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

type settingsRequest struct {
	PlatformFeeBps      int32  `json:"platform_fee_bps"`
	PromoPlatformFeeBps int32  `json:"promo_platform_fee_bps"`
	CourierFeeBps       int32  `json:"courier_fee_bps"`
	DeliveryFeeCents    int32  `json:"delivery_fee_cents"`
	GatewayPlanCode     string `json:"gateway_plan_code"`
	SplitEnabled        bool   `json:"split_enabled"`
}

func (h *Handler) updateSettings(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	var in settingsRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.UpdateSettings(c.Request().Context(), p, Settings{
		PlatformFeeBps: in.PlatformFeeBps, PromoPlatformFeeBps: in.PromoPlatformFeeBps, CourierFeeBps: in.CourierFeeBps,
		DeliveryFeeCents: in.DeliveryFeeCents, GatewayPlanCode: in.GatewayPlanCode, SplitEnabled: in.SplitEnabled,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

type planRequest struct {
	GatewayPlan
	Notes *string `json:"notes"`
}

func (h *Handler) updatePlan(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	var in planRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.UpdatePlan(c.Request().Context(), p, c.Param("code"), in.GatewayPlan, in.Notes)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) simulate(c *echo.Context) error {
	var in SimulateInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Simulate(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) getOrganization(c *echo.Context) error {
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	out, err := h.svc.GetOrganization(c.Request().Context(), orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// overrideRequest: cada campo null (o ausente) = la organización hereda de la configuración
// general. Es un reemplazo completo de lo propio, no un parche.
type overrideRequest struct {
	PlatformFeeBps      *int32  `json:"platform_fee_bps"`
	PromoPlatformFeeBps *int32  `json:"promo_platform_fee_bps"`
	CourierFeeBps       *int32  `json:"courier_fee_bps"`
	DeliveryFeeCents    *int32  `json:"delivery_fee_cents"`
	GatewayPlanCode     *string `json:"gateway_plan_code"`
	EpaycoMerchantID    *string `json:"epayco_merchant_id"`
}

func (h *Handler) setOrganization(c *echo.Context) error {
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
	out, err := h.svc.SetOrganization(c.Request().Context(), p, orgID, Override{
		PlatformFeeBps: in.PlatformFeeBps, PromoPlatformFeeBps: in.PromoPlatformFeeBps, CourierFeeBps: in.CourierFeeBps,
		DeliveryFeeCents: in.DeliveryFeeCents, GatewayPlanCode: in.GatewayPlanCode, EpaycoMerchantID: in.EpaycoMerchantID,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}
