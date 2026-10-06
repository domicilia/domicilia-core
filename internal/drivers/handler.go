package drivers

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone las postulaciones por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterPublic registra la postulación, que es PÚBLICA a propósito: quien
// quiere ser domiciliario todavía no tiene cuenta. Por eso va en un grupo sin
// JWT. No crea ninguna identidad: solo deja una solicitud pendiente.
func (h *Handler) RegisterPublic(g *echo.Group) {
	g.POST("/driver-applications", h.apply)
}

// Register registra la revisión, solo para el superadmin. g ya exige un usuario
// de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	review := identity.Require(access.PlatformDriversReview)
	g.GET("/driver-applications", h.list, review)
	g.POST("/driver-applications/:application_id/approve", h.approve, review)
	g.POST("/driver-applications/:application_id/reject", h.reject, review)
}

func (h *Handler) apply(c *echo.Context) error {
	var in ApplyInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	a, err := h.svc.Apply(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, a)
}

func (h *Handler) list(c *echo.Context) error {
	apps, err := h.svc.List(c.Request().Context(), c.QueryParam("status_filter"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, apps)
}

func (h *Handler) approve(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "application_id")
	if err != nil {
		return err
	}
	a, err := h.svc.Approve(c.Request().Context(), p, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, a)
}

func (h *Handler) reject(c *echo.Context) error {
	id, err := httpserver.UUIDParam(c, "application_id")
	if err != nil {
		return err
	}
	a, err := h.svc.Reject(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, a)
}
