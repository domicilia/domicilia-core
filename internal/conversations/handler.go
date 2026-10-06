package conversations

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone las conversaciones por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/conversations", h.list)
	g.GET("/organizations/:org_id/conversations/counts", h.counts)
	g.GET("/organizations/:org_id/conversations/:conversation_id", h.get)
	g.PATCH("/organizations/:org_id/conversations/:conversation_id", h.update)
	g.POST("/organizations/:org_id/conversations/:conversation_id/read", h.read)
	g.GET("/organizations/:org_id/conversations/:conversation_id/messages", h.messages)
	g.POST("/organizations/:org_id/conversations/:conversation_id/messages", h.send)
}

type updateRequest struct {
	Status       *string                     `json:"status"`
	Priority     httpserver.Field[string]    `json:"priority"`
	AssigneeID   httpserver.Field[uuid.UUID] `json:"assignee_id"`
	HandledBy    *string                     `json:"handled_by"`
	SnoozedUntil httpserver.Field[time.Time] `json:"snoozed_until"`
}

type sendRequest struct {
	Body             string     `json:"body"`
	ReplyToMessageID *uuid.UUID `json:"reply_to_message_id"`
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

func actorOrgConv(c *echo.Context) (identity.Principal, uuid.UUID, uuid.UUID, error) {
	p, org, err := actorOrg(c)
	if err != nil {
		return identity.Principal{}, uuid.Nil, uuid.Nil, err
	}
	id, err := httpserver.UUIDParam(c, "conversation_id")
	if err != nil {
		return identity.Principal{}, uuid.Nil, uuid.Nil, err
	}
	return p, org, id, nil
}

// optionalUUID lee un identificador opcional de la consulta.
func optionalUUID(c *echo.Context, name string) (*uuid.UUID, error) {
	s := c.QueryParam(name)
	if s == "" {
		return nil, nil //nolint:nilnil // ausente es un valor válido
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, apperr.Invalid(name + " no es un identificador válido")
	}
	return &id, nil
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
	inbox, err := optionalUUID(c, "inbox_id")
	if err != nil {
		return err
	}
	in := ListInput{InboxID: inbox, Status: c.QueryParam("status"), Assignee: c.QueryParam("assignee"), Search: c.QueryParam("q"), Limit: limit}
	if cur != nil {
		in.Before = &Cursor{At: cur.At, ID: cur.ID}
	}
	rows, err := h.svc.List(c.Request().Context(), p, org, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewCursorPage(rows, limit, func(x Conversation) httpserver.Cursor {
		return httpserver.Cursor{At: x.LastActivityAt, ID: x.ID}
	}))
}

func (h *Handler) counts(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	inbox, err := optionalUUID(c, "inbox_id")
	if err != nil {
		return err
	}
	out, err := h.svc.Tabs(c.Request().Context(), p, org, inbox, c.QueryParam("status"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) get(c *echo.Context) error {
	p, org, id, err := actorOrgConv(c)
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
	p, org, id, err := actorOrgConv(c)
	if err != nil {
		return err
	}
	var in updateRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Update(c.Request().Context(), p, org, id, UpdateInput{
		Status: in.Status, Priority: in.Priority, Assignee: in.AssigneeID, HandledBy: in.HandledBy, SnoozedUntil: in.SnoozedUntil,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) read(c *echo.Context) error {
	p, org, id, err := actorOrgConv(c)
	if err != nil {
		return err
	}
	if err := h.svc.MarkRead(c.Request().Context(), p, org, id); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) messages(c *echo.Context) error {
	p, org, id, err := actorOrgConv(c)
	if err != nil {
		return err
	}
	limit, cur, err := httpserver.CursorParams(c)
	if err != nil {
		return err
	}
	var before *Cursor
	if cur != nil {
		before = &Cursor{At: cur.At, ID: cur.ID}
	}
	rows, err := h.svc.Messages(c.Request().Context(), p, org, id, before, limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewCursorPage(rows, limit, func(x Message) httpserver.Cursor {
		return httpserver.Cursor{At: x.CreatedAt, ID: x.ID}
	}))
}

func (h *Handler) send(c *echo.Context) error {
	p, org, id, err := actorOrgConv(c)
	if err != nil {
		return err
	}
	var in sendRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Send(c.Request().Context(), p, org, id, SendInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, out)
}
