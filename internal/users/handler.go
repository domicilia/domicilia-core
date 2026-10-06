package users

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/auth"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone los usuarios por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterProvision registra el alta de perfil. Va en un grupo con solo JWT
// (auth.Middleware), NO con identity.RequireUser: es justo el endpoint que crea
// la fila que RequireUser exige.
func (h *Handler) RegisterProvision(g *echo.Group) {
	g.POST("/users", h.signUp)
}

// Register registra el resto de rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/users/me", h.me)
	g.PATCH("/users/me", h.updateMe)
	g.PATCH("/users/:user_id/promote", h.promote, identity.Require(access.PlatformRolesManage))

	g.GET("/platform/users", h.list, identity.Require(access.PlatformUsersRead))
	g.GET("/platform/users/:user_id", h.detail, identity.Require(access.PlatformUsersRead))
	g.PATCH("/platform/users/:user_id/active", h.setActive, identity.Require(access.PlatformUsersManage))
}

type activeRequest struct {
	IsActive *bool `json:"is_active"`
}

func (h *Handler) signUp(c *echo.Context) error {
	claims, ok := auth.FromContext(c.Request().Context())
	if !ok {
		return auth.Unauthorized(c)
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return auth.Unauthorized(c)
	}
	var in ProfileInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	u, err := h.svc.SignUp(c.Request().Context(), id, claims.Email, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, u)
}

func (h *Handler) me(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	m, err := h.svc.Me(c.Request().Context(), p)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

func (h *Handler) updateMe(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	var in ProfileInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	u, err := h.svc.UpdateMe(c.Request().Context(), p, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}

func (h *Handler) promote(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	u, err := h.svc.PromoteToGeneralAdmin(c.Request().Context(), p, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}

func (h *Handler) list(c *echo.Context) error {
	page, err := httpserver.PageParams(c)
	if err != nil {
		return err
	}
	f := ListFilter{
		Search: c.QueryParam("search"), Role: c.QueryParam("role"),
		Limit: page.Limit, Offset: page.Offset,
	}
	if s := c.QueryParam("organization_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return apperr.Invalid("organization_id no es un identificador válido")
		}
		f.OrganizationID = &id
	}
	if s := c.QueryParam("is_active"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return apperr.Invalid("is_active debe ser true o false")
		}
		f.IsActive = &b
	}
	users, total, err := h.svc.List(c.Request().Context(), f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(users, total, page))
}

func (h *Handler) detail(c *echo.Context) error {
	id, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	d, err := h.svc.Detail(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, d)
}

func (h *Handler) setActive(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	var in activeRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	if in.IsActive == nil {
		return apperr.Invalid("is_active es obligatorio")
	}
	u, err := h.svc.SetActive(c.Request().Context(), p, id, *in.IsActive)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}
