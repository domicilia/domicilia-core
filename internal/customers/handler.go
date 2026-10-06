package customers

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone los clientes por HTTP. El alta (POST /v1/users) la sirve `users`,
// que es quien conoce el token sin perfil.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/customers/me", h.get)
	g.PATCH("/customers/me", h.update)
}

func (h *Handler) get(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	prof, err := h.svc.Profile(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	if prof == nil {
		return apperr.NotFound("no tienes un perfil de cliente")
	}
	return c.JSON(http.StatusOK, prof)
}

func (h *Handler) update(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	var in ProfileInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	prof, err := h.svc.UpdateProfile(c.Request().Context(), p.ID, in)
	if err != nil {
		return err
	}
	if prof == nil {
		return apperr.NotFound("no tienes un perfil de cliente")
	}
	return c.JSON(http.StatusOK, prof)
}
