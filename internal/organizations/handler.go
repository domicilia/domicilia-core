package organizations

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/auth"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone las organizaciones por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterPublic registra las rutas sin autenticación: la cara del negocio y la
// vista previa de una invitación (el token es la credencial).
func (h *Handler) RegisterPublic(g *echo.Group) {
	g.GET("/public/organizations", h.publicList)
	g.GET("/public/organizations/:org_slug", h.public)
	g.POST("/invitations/preview", h.previewInvitation)
}

// RegisterAuthenticated registra las rutas que piden un JWT válido pero no un perfil
// de negocio: aceptar una invitación es justo lo que puede crearlo.
func (h *Handler) RegisterAuthenticated(g *echo.Group) {
	g.POST("/invitations/accept", h.acceptInvitation)
}

// Register registra las rutas de negocio. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.POST("/organizations", h.create, identity.Require(access.PlatformOrganizationsCreate))
	g.GET("/organizations", h.list)
	// La ruta estática by-slug tiene prioridad sobre :org_id.
	g.GET("/organizations/by-slug/:org_slug", h.getBySlug)
	g.GET("/organizations/:org_id", h.get)
	g.PATCH("/organizations/:org_id", h.updateProfile)
	g.GET("/organizations/:org_id/settings", h.getSettings)
	g.PATCH("/organizations/:org_id/settings", h.updateSettings)

	g.POST("/organizations/:org_id/members", h.addMember)
	g.POST("/organizations/:org_id/members/invite", h.inviteMember) // obsoleta: ver InviteMember
	g.PATCH("/organizations/:org_id/members/:user_id", h.changeMemberRole)
	g.DELETE("/organizations/:org_id/members/:user_id", h.removeMember)
	g.GET("/organizations/:org_id/members", h.listMembers)

	g.POST("/organizations/:org_id/invitations", h.invite)
	g.GET("/organizations/:org_id/invitations", h.listInvitations)
	g.POST("/organizations/:org_id/invitations/:invitation_id/resend", h.resendInvitation)
	g.DELETE("/organizations/:org_id/invitations/:invitation_id", h.revokeInvitation)

	// Panel de plataforma. Los cambios de plan y de excepciones viven en el paquete plans.
	viewAll := identity.Require(access.PlatformOrganizationsAccessAll)
	manage := identity.Require(access.PlatformOrganizationsManage)
	g.GET("/platform/organizations", h.search, viewAll)
	g.POST("/platform/organizations/:org_id/suspend", h.suspend, manage)
	g.POST("/platform/organizations/:org_id/reactivate", h.reactivate, manage)
	g.POST("/platform/organizations/:org_id/archive", h.archive, manage)
	g.POST("/platform/organizations/:org_id/restore", h.restore, manage)
}

type createRequest struct {
	Name        string         `json:"name"`
	Slug        *string        `json:"slug"`
	Description *string        `json:"description"`
	PlanTier    *string        `json:"plan_tier"`
	AdminEmail  *string        `json:"admin_email"`
	Settings    *SettingsPatch `json:"settings"`
}

type profileRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type addMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

type changeRoleRequest struct {
	Role string `json:"role"`
}

type inviteRequest struct {
	Email    string  `json:"email"`
	FullName *string `json:"full_name"`
	Role     string  `json:"role"`
}

type invitationRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type tokenRequest struct {
	Token string `json:"token"`
}

type transitionRequest struct {
	Reason string `json:"reason"`
}

// orgAndActor lee al usuario y el :org_id de la ruta.
func orgAndActor(c *echo.Context) (identity.Principal, uuid.UUID, error) {
	p, err := identity.Current(c)
	if err != nil {
		return identity.Principal{}, uuid.Nil, err
	}
	id, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return identity.Principal{}, uuid.Nil, err
	}
	return p, id, nil
}

func (h *Handler) create(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	var in createRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.Create(c.Request().Context(), p, CreateInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) list(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	orgs, err := h.svc.List(c.Request().Context(), p)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orgs)
}

func (h *Handler) search(c *echo.Context) error {
	page, err := httpserver.PageParams(c)
	if err != nil {
		return err
	}
	orgs, total, err := h.svc.Search(c.Request().Context(), SearchInput{
		Query: c.QueryParam("q"), Status: c.QueryParam("status"), PlanTier: c.QueryParam("plan_tier"),
		Limit: page.Limit, Offset: page.Offset,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(orgs, total, page))
}

func (h *Handler) get(c *echo.Context) error {
	p, id, err := orgAndActor(c)
	if err != nil {
		return err
	}
	org, err := h.svc.Get(c.Request().Context(), p, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, org)
}

func (h *Handler) getBySlug(c *echo.Context) error {
	p, err := identity.Current(c)
	if err != nil {
		return err
	}
	org, err := h.svc.GetBySlug(c.Request().Context(), p, c.Param("org_slug"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, org)
}

func (h *Handler) publicList(c *echo.Context) error {
	page, err := httpserver.PageParams(c)
	if err != nil {
		return err
	}
	orgs, total, err := h.svc.PublicList(c.Request().Context(), c.QueryParam("q"), page.Limit, page.Offset)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(orgs, total, page))
}

func (h *Handler) public(c *echo.Context) error {
	org, err := h.svc.Public(c.Request().Context(), c.Param("org_slug"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, org)
}

func (h *Handler) updateProfile(c *echo.Context) error {
	p, id, err := orgAndActor(c)
	if err != nil {
		return err
	}
	var in profileRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	org, err := h.svc.UpdateProfile(c.Request().Context(), p, id, ProfileInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, org)
}

func (h *Handler) getSettings(c *echo.Context) error {
	p, id, err := orgAndActor(c)
	if err != nil {
		return err
	}
	set, err := h.svc.Settings(c.Request().Context(), p, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, set)
}

func (h *Handler) updateSettings(c *echo.Context) error {
	p, id, err := orgAndActor(c)
	if err != nil {
		return err
	}
	var in SettingsPatch
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	set, err := h.svc.UpdateSettings(c.Request().Context(), p, id, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, set)
}

func (h *Handler) transition(c *echo.Context, do func(ctx *echo.Context, p identity.Principal, id uuid.UUID, reason string) (Organization, error)) error {
	p, id, err := orgAndActor(c)
	if err != nil {
		return err
	}
	var in transitionRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	org, err := do(c, p, id, in.Reason)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, org)
}

func optionalReason(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *Handler) suspend(c *echo.Context) error {
	return h.transition(c, func(c *echo.Context, p identity.Principal, id uuid.UUID, reason string) (Organization, error) {
		return h.svc.Suspend(c.Request().Context(), p, id, reason)
	})
}

func (h *Handler) archive(c *echo.Context) error {
	return h.transition(c, func(c *echo.Context, p identity.Principal, id uuid.UUID, reason string) (Organization, error) {
		return h.svc.Archive(c.Request().Context(), p, id, reason)
	})
}

func (h *Handler) reactivate(c *echo.Context) error {
	return h.transition(c, func(c *echo.Context, p identity.Principal, id uuid.UUID, reason string) (Organization, error) {
		return h.svc.Reactivate(c.Request().Context(), p, id, optionalReason(reason))
	})
}

func (h *Handler) restore(c *echo.Context) error {
	return h.transition(c, func(c *echo.Context, p identity.Principal, id uuid.UUID, reason string) (Organization, error) {
		return h.svc.Restore(c.Request().Context(), p, id, optionalReason(reason))
	})
}

func (h *Handler) addMember(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	var in addMemberRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	userID, err := uuid.Parse(in.UserID)
	if err != nil {
		return apperr.Invalid("user_id no es un identificador válido")
	}
	m, err := h.svc.AddMember(c.Request().Context(), p, orgID, userID, in.Role)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, m)
}

func (h *Handler) inviteMember(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	var in inviteRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	m, err := h.svc.InviteMember(c.Request().Context(), p, orgID, in.Email, in.FullName, in.Role)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, m)
}

func (h *Handler) changeMemberRole(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	userID, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	var in changeRoleRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	m, err := h.svc.ChangeMemberRole(c.Request().Context(), p, orgID, userID, in.Role)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

func (h *Handler) removeMember(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	userID, err := httpserver.UUIDParam(c, "user_id")
	if err != nil {
		return err
	}
	if err := h.svc.RemoveMember(c.Request().Context(), p, orgID, userID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) listMembers(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	members, err := h.svc.Members(c.Request().Context(), p, orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, members)
}

func (h *Handler) invite(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	var in invitationRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	inv, err := h.svc.Invite(c.Request().Context(), p, orgID, in.Email, in.Role)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, inv)
}

func (h *Handler) listInvitations(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	invs, err := h.svc.Invitations(c.Request().Context(), p, orgID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, invs)
}

func (h *Handler) resendInvitation(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	invID, err := httpserver.UUIDParam(c, "invitation_id")
	if err != nil {
		return err
	}
	inv, err := h.svc.Resend(c.Request().Context(), p, orgID, invID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, inv)
}

func (h *Handler) revokeInvitation(c *echo.Context) error {
	p, orgID, err := orgAndActor(c)
	if err != nil {
		return err
	}
	invID, err := httpserver.UUIDParam(c, "invitation_id")
	if err != nil {
		return err
	}
	if err := h.svc.RevokeInvitation(c.Request().Context(), p, orgID, invID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) previewInvitation(c *echo.Context) error {
	var in tokenRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	p, err := h.svc.Preview(c.Request().Context(), in.Token)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

func (h *Handler) acceptInvitation(c *echo.Context) error {
	claims, ok := auth.FromContext(c.Request().Context())
	if !ok {
		return auth.Unauthorized(c)
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return auth.Unauthorized(c)
	}
	var in tokenRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	m, err := h.svc.Accept(c.Request().Context(), userID, claims.Email, in.Token)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}
