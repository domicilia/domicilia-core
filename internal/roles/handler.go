package roles

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone los roles por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register registra las rutas. g ya exige un usuario de negocio activo. Las de
// /platform exigen su permiso de plataforma en la ruta; las de /organizations lo
// comprueban en el servicio, porque dependen de la organización.
func (h *Handler) Register(g *echo.Group) {
	read := identity.Require(access.PlatformRolesRead)
	manage := identity.Require(access.PlatformRolesManage)

	g.GET("/platform/permissions", h.permissions, read)
	g.GET("/platform/roles", h.list, read)
	g.POST("/platform/roles", h.createPlatform, manage)
	g.GET("/platform/roles/:role_id", h.get, read)
	g.PATCH("/platform/roles/:role_id", h.updatePlatform, manage)
	g.DELETE("/platform/roles/:role_id", h.deletePlatform, manage)
	g.POST("/platform/users/:user_id/roles", h.grant, manage)
	g.DELETE("/platform/users/:user_id/roles/:role", h.revoke, manage)
	g.GET("/platform/audit", h.platformAudit, identity.Require(access.PlatformAuditRead))

	g.GET("/organizations/:org_id/roles", h.listForOrg)
	g.POST("/organizations/:org_id/roles", h.createOrg)
	g.PATCH("/organizations/:org_id/roles/:role_id", h.updateOrg)
	g.DELETE("/organizations/:org_id/roles/:role_id", h.deleteOrg)
	g.GET("/organizations/:org_id/audit", h.orgAudit)
}

type grantRequest struct {
	Role string `json:"role"`
}

func (h *Handler) permissions(c *echo.Context) error {
	return c.JSON(http.StatusOK, h.svc.Permissions())
}

func (h *Handler) list(c *echo.Context) error {
	var orgID *uuid.UUID
	if s := c.QueryParam("organization_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return apperr.Invalid("organization_id no es un identificador válido")
		}
		orgID = &id
	}
	rs, err := h.svc.List(c.Request().Context(), c.QueryParam("scope"), orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rs)
}

func (h *Handler) get(c *echo.Context) error {
	id, err := httpserver.UUIDParam(c, "role_id")
	if err != nil {
		return err
	}
	r, err := h.svc.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, r)
}

func (h *Handler) createPlatform(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	var in CreateInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	r, err := h.svc.CreatePlatformRole(c.Request().Context(), p, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, r)
}

func (h *Handler) updatePlatform(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "role_id")
	if err != nil {
		return err
	}
	var in UpdateInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	r, err := h.svc.UpdatePlatformRole(c.Request().Context(), p, id, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, r)
}

func (h *Handler) deletePlatform(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "role_id")
	if err != nil {
		return err
	}
	if err := h.svc.DeletePlatformRole(c.Request().Context(), p, id); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) grant(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	userID, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	var in grantRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	res, err := h.svc.GrantPlatformRole(c.Request().Context(), p, userID, in.Role)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

func (h *Handler) revoke(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	userID, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	res, err := h.svc.RevokePlatformRole(c.Request().Context(), p, userID, c.Param("role"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

func (h *Handler) listForOrg(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	rs, err := h.svc.ListForOrg(c.Request().Context(), p, orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rs)
}

func (h *Handler) createOrg(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	var in CreateInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	r, err := h.svc.CreateOrgRole(c.Request().Context(), p, orgID, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, r)
}

func (h *Handler) updateOrg(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	roleID, err := httpserver.UUIDParam(c, "role_id")
	if err != nil {
		return err
	}
	var in UpdateInput
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	r, err := h.svc.UpdateOrgRole(c.Request().Context(), p, orgID, roleID, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, r)
}

func (h *Handler) deleteOrg(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	roleID, err := httpserver.UUIDParam(c, "role_id")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteOrgRole(c.Request().Context(), p, orgID, roleID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// auditFilter lee los filtros y la paginación de un listado de auditoría.
func auditFilter(c *echo.Context) (audit.Filter, httpserver.Page, error) {
	page, err := httpserver.PageParams(c)
	if err != nil {
		return audit.Filter{}, page, err
	}
	f := audit.Filter{Limit: page.Limit, Offset: page.Offset}
	if s := c.QueryParam("user_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return f, page, apperr.Invalid("user_id no es un identificador válido")
		}
		f.TargetUserID = &id
	}
	if s := c.QueryParam("organization_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return f, page, apperr.Invalid("organization_id no es un identificador válido")
		}
		f.OrganizationID = &id
	}
	if s := c.QueryParam("action"); s != "" {
		f.Action = &s
	}
	return f, page, nil
}

func (h *Handler) platformAudit(c *echo.Context) error {
	f, page, err := auditFilter(c)
	if err != nil {
		return err
	}
	recs, total, err := h.svc.PlatformAudit(c.Request().Context(), f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(recs, total, page))
}

func (h *Handler) orgAudit(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgID, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	f, page, err := auditFilter(c)
	if err != nil {
		return err
	}
	recs, total, err := h.svc.OrgAudit(c.Request().Context(), p, orgID, f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(recs, total, page))
}
