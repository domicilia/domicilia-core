package contacts

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone los contactos por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/contacts", h.list)
	g.POST("/organizations/:org_id/contacts", h.create)
	g.GET("/organizations/:org_id/contacts/:contact_id", h.get)
	g.PATCH("/organizations/:org_id/contacts/:contact_id", h.update)
}

type createRequest struct {
	Phone            string         `json:"phone"`
	Name             *string        `json:"name"`
	Email            *string        `json:"email"`
	CustomAttributes map[string]any `json:"custom_attributes"`
}

type updateRequest struct {
	Name             *string        `json:"name"`
	Email            *string        `json:"email"`
	CustomAttributes map[string]any `json:"custom_attributes"`
	Blocked          *bool          `json:"blocked"`
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

func (h *Handler) list(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	limit, cur, err := httpserver.CursorParams(c)
	if err != nil {
		return err
	}
	in := ListInput{Search: c.QueryParam("q"), Limit: limit}
	if cur != nil {
		in.Before = &Cursor{At: cur.At, ID: cur.ID}
	}
	rows, err := h.svc.List(c.Request().Context(), p, org, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewCursorPage(rows, limit, func(x Contact) httpserver.Cursor {
		return httpserver.Cursor{At: x.CreatedAt, ID: x.ID}
	}))
}

func (h *Handler) create(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in createRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Create(c.Request().Context(), p, org, CreateInput(in))
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
	id, err := httpserver.UUIDParam(c, "contact_id")
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
	id, err := httpserver.UUIDParam(c, "contact_id")
	if err != nil {
		return err
	}
	var in updateRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Update(c.Request().Context(), p, org, id, UpdateInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}
