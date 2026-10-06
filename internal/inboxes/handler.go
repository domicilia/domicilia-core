package inboxes

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone las bandejas por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/inboxes", h.list)
	g.POST("/organizations/:org_id/inboxes/whatsapp", h.connect)
	g.GET("/organizations/:org_id/inboxes/:inbox_id", h.get)
	g.PATCH("/organizations/:org_id/inboxes/:inbox_id", h.rename)
	g.DELETE("/organizations/:org_id/inboxes/:inbox_id", h.archive)
	g.PUT("/organizations/:org_id/inboxes/:inbox_id/credentials", h.rotate)
	g.POST("/organizations/:org_id/inboxes/:inbox_id/refresh", h.refresh)
}

type connectRequest struct {
	Name          string `json:"name"`
	WABAID        string `json:"waba_id"`
	PhoneNumberID string `json:"phone_number_id"`
	AccessToken   string `json:"access_token"`
	AppSecret     string `json:"app_secret"`
}

type renameRequest struct {
	Name string `json:"name"`
}

type credentialsRequest struct {
	AccessToken string `json:"access_token"`
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

func actorOrgInbox(c *echo.Context) (identity.Principal, uuid.UUID, uuid.UUID, error) {
	p, org, err := actorOrg(c)
	if err != nil {
		return identity.Principal{}, uuid.Nil, uuid.Nil, err
	}
	id, err := httpserver.UUIDParam(c, "inbox_id")
	if err != nil {
		return identity.Principal{}, uuid.Nil, uuid.Nil, err
	}
	return p, org, id, nil
}

func (h *Handler) list(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	out, err := h.svc.List(c.Request().Context(), p, org, c.QueryParam("include_archived") == "true")
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) connect(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in connectRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Connect(c.Request().Context(), p, org, ConnectInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) get(c *echo.Context) error {
	p, org, id, err := actorOrgInbox(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Get(c.Request().Context(), p, org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) rename(c *echo.Context) error {
	p, org, id, err := actorOrgInbox(c)
	if err != nil {
		return err
	}
	var in renameRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Rename(c.Request().Context(), p, org, id, in.Name)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) archive(c *echo.Context) error {
	p, org, id, err := actorOrgInbox(c)
	if err != nil {
		return err
	}
	if err := h.svc.Archive(c.Request().Context(), p, org, id); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) rotate(c *echo.Context) error {
	p, org, id, err := actorOrgInbox(c)
	if err != nil {
		return err
	}
	var in credentialsRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.RotateToken(c.Request().Context(), p, org, id, in.AccessToken)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) refresh(c *echo.Context) error {
	p, org, id, err := actorOrgInbox(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Refresh(c.Request().Context(), p, org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}
