package organizations

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/platform/mail"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
	"github.com/domicilia/domicilia-core/internal/roles"
)

// compensateTimeout acota el intento de deshacer un alta en GoTrue.
const compensateTimeout = 5 * time.Second

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	OrganizationByID(ctx context.Context, id uuid.UUID) (Organization, error)
	OrganizationBySlug(ctx context.Context, slug string) (Organization, error)
	// NameExists no distingue mayúsculas; exceptID (uuid.Nil: ninguno) excluye a esa
	// organización, para poder renombrarla.
	NameExists(ctx context.Context, name string, exceptID uuid.UUID) (bool, error)
	SlugExists(ctx context.Context, slug string) (bool, error)
	// ListAll y ListByUser no incluyen las archivadas.
	ListAll(ctx context.Context) ([]Organization, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]Organization, error)
	Search(ctx context.Context, f SearchFilter) ([]Organization, int64, error)
	// PublicList es el directorio para la app de cliente: solo organizaciones
	// activas, sin campos internos. search vacío no filtra.
	PublicList(ctx context.Context, search string, limit, offset int) ([]PublicOrganizationSummary, int64, error)
	// Create da de alta la organización, sus ajustes, su suscripción inicial y, si
	// viene, la invitación del primer administrador, en una transacción.
	// Devuelve ErrDuplicate si el nombre o el slug ya existen.
	Create(ctx context.Context, n NewOrganization) (Organization, *Invitation, error)
	UpdateProfile(ctx context.Context, id, actor uuid.UUID, c ProfileChange) (Organization, error)
	// Transition aplica la operación si el estado actual la permite; si no, devuelve un
	// *TransitionError (ErrInvalidTransition). Devuelve ErrNotFound si no existe.
	Transition(ctx context.Context, id uuid.UUID, op Operation, reason *string, actor uuid.UUID) (Organization, error)
	Settings(ctx context.Context, id uuid.UUID) (Settings, error)
	UpdateSettings(ctx context.Context, id, actor uuid.UUID, apply func(Settings) (Settings, []string, error)) (Settings, error)

	// AddMember devuelve ErrAlreadyMember, o ErrNotFound si el usuario o la
	// organización desaparecieron entre la comprobación y el alta. Audita.
	AddMember(ctx context.Context, userID, orgID uuid.UUID, role roles.Role, actor uuid.UUID) (Member, error)
	// ChangeMemberRole devuelve ErrNotFound si no es miembro, o ErrLastManager. Audita.
	ChangeMemberRole(ctx context.Context, userID, orgID uuid.UUID, role roles.Role, actor uuid.UUID) (Member, error)
	// RemoveMember devuelve false si no era miembro, o ErrLastManager. Audita.
	RemoveMember(ctx context.Context, userID, orgID, actor uuid.UUID) (removed bool, err error)
	Members(ctx context.Context, orgID uuid.UUID) ([]Member, error)

	UserExists(ctx context.Context, id uuid.UUID) (bool, error)
	EmailRegistered(ctx context.Context, email string) (bool, error)
	IsMemberByEmail(ctx context.Context, orgID uuid.UUID, email string) (bool, error)
	// CreateStaff crea el usuario de negocio y su membresía en una transacción y lo
	// audita. Devuelve ErrEmailTaken si el correo (o el id) ya tiene perfil.
	CreateStaff(ctx context.Context, id uuid.UUID, email string, fullName *string, orgID uuid.UUID, role roles.Role, actor uuid.UUID) (Member, error)

	// IssueInvitation emite la invitación; si el correo ya tenía una pendiente, la
	// reemplaza. Audita.
	IssueInvitation(ctx context.Context, n NewInvitation) (Invitation, error)
	Invitations(ctx context.Context, orgID uuid.UUID) ([]Invitation, error)
	// PendingInvitationExists dice si hay una vigente para ese correo.
	PendingInvitationExists(ctx context.Context, orgID uuid.UUID, email string) (bool, error)
	// RevokeInvitation devuelve false si no existía o ya no estaba pendiente. Audita.
	RevokeInvitation(ctx context.Context, orgID, invID, actor uuid.UUID) (bool, error)
	InvitationPreview(ctx context.Context, tokenHash []byte, now time.Time) (InvitationPreview, error)
	AcceptInvitation(ctx context.Context, in AcceptInput) (Member, error)
}

// IdentityProvider crea y borra cuentas en auth-domicilia (GoTrue).
type IdentityProvider interface {
	CreateUser(ctx context.Context, email, password string) (gotrue.User, error)
	DeleteUser(ctx context.Context, id uuid.UUID) error
}

// RoleResolver resuelve y autoriza el rol que se va a asignar a un miembro.
type RoleResolver interface {
	ResolveOrgRole(ctx context.Context, actor identity.Principal, orgID uuid.UUID, ref string) (roles.Role, error)
}

// Capacity comprueba los límites del plan (paquete plans).
type Capacity interface {
	CheckMemberCapacity(ctx context.Context, orgID uuid.UUID, adding int64) error
}

// Options son los ajustes del servicio que no son dependencias.
type Options struct {
	// AppURL es la dirección pública del frontend, para armar el enlace de las
	// invitaciones ({AppURL}/auth/accept-invite?token=...).
	AppURL string
	// Now es el reloj; nil usa time.Now. Las pruebas lo fijan.
	Now func() time.Time
}

// Service reúne las reglas de negocio de las organizaciones.
type Service struct {
	repo     Repository
	idp      IdentityProvider
	roles    RoleResolver
	capacity Capacity
	mailer   mail.Sender
	log      *slog.Logger
	appURL   string
	now      func() time.Time
}

// NewService crea el servicio.
func NewService(repo Repository, idp IdentityProvider, roles RoleResolver, capacity Capacity, mailer mail.Sender, log *slog.Logger, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		repo: repo, idp: idp, roles: roles, capacity: capacity, mailer: mailer, log: log,
		appURL: strings.TrimRight(opts.AppURL, "/"), now: now,
	}
}

// ---------------------------------------------------------------------------
// Alta
// ---------------------------------------------------------------------------

// CreateInput son los datos para crear una organización. Solo Name es obligatorio.
type CreateInput struct {
	Name        string
	Slug        *string
	Description *string
	PlanTier    *string
	// AdminEmail, si viene, recibe una invitación como administrador. Nombrar a un
	// administrador exige platform.roles.manage, igual que en cualquier otra vía.
	AdminEmail *string
	Settings   *SettingsPatch
}

// Created es la organización creada y, si se pidió, la invitación de su administrador.
type Created struct {
	Organization
	AdminInvitation *IssuedInvitation `json:"admin_invitation,omitempty"`
}

// Create da de alta una organización. Quien llama debe tener
// PlatformOrganizationsCreate (lo exige la ruta): solo el operador crea inquilinos.
func (s *Service) Create(ctx context.Context, actor identity.Principal, in CreateInput) (Created, error) {
	name, err := validate.Required("name", in.Name, maxName)
	if err != nil {
		return Created{}, err
	}
	if err := validate.OptionalMaxLen("description", in.Description, maxDescription); err != nil {
		return Created{}, err
	}

	slug, err := s.slugFor(name, in.Slug)
	if err != nil {
		return Created{}, err
	}

	plan := plans.Default
	if in.PlanTier != nil {
		p, ok := plans.Lookup(strings.TrimSpace(*in.PlanTier))
		if !ok {
			return Created{}, apperr.Invalid("plan_tier desconocido: " + *in.PlanTier)
		}
		plan = p.Tier
	}

	settings := DefaultSettings()
	if in.Settings != nil {
		if settings, _, err = in.Settings.Apply(settings); err != nil {
			return Created{}, err
		}
	}

	if taken, err := s.repo.NameExists(ctx, name, uuid.Nil); err != nil {
		return Created{}, err
	} else if taken {
		return Created{}, apperr.Conflict("ya existe una organización con ese nombre")
	}
	if taken, err := s.repo.SlugExists(ctx, slug); err != nil {
		return Created{}, err
	} else if taken {
		return Created{}, apperr.Conflict("ya existe una organización con ese slug")
	}

	n := NewOrganization{
		ID: uuid.New(), Name: name, Slug: slug, Description: blankToNil(in.Description), PlanTier: string(plan),
		Settings: settings, Actor: actor.ID,
	}
	var token string
	if in.AdminEmail != nil {
		if n.AdminInvitation, token, err = s.adminInvitation(ctx, actor, n.ID, *in.AdminEmail); err != nil {
			return Created{}, err
		}
	}

	org, inv, err := s.repo.Create(ctx, n)
	if errors.Is(err, ErrDuplicate) { // carrera entre dos altas simultáneas
		return Created{}, apperr.Conflict("ya existe una organización con ese nombre o slug")
	}
	if err != nil {
		return Created{}, err
	}
	out := Created{Organization: org}
	if inv != nil {
		issued := s.issued(*inv, token)
		out.AdminInvitation = &issued
		s.notifyInvitation(ctx, org.Name, issued)
	}
	return out, nil
}

// slugFor decide el slug: el enviado, validado, o el derivado del nombre.
func (s *Service) slugFor(name string, explicit *string) (string, error) {
	if explicit != nil && strings.TrimSpace(*explicit) != "" {
		slug := strings.ToLower(strings.TrimSpace(*explicit))
		if err := ValidateSlug(slug); err != nil {
			return "", apperr.Invalid(err.Error())
		}
		return slug, nil
	}
	slug := Slugify(name)
	if err := ValidateSlug(slug); err != nil {
		return "", apperr.Invalid("el nombre no genera un slug válido (" + err.Error() + "): envía uno en el campo slug")
	}
	return slug, nil
}

func (s *Service) adminInvitation(ctx context.Context, actor identity.Principal, orgID uuid.UUID, rawEmail string) (*NewInvitation, string, error) {
	email, err := validate.Email("admin_email", rawEmail)
	if err != nil {
		return nil, "", err
	}
	role, err := s.roles.ResolveOrgRole(ctx, actor, orgID, access.RoleAdmin)
	if err != nil {
		return nil, "", err
	}
	token, hash, err := newToken()
	if err != nil {
		return nil, "", err
	}
	return &NewInvitation{
		OrganizationID: orgID, Email: email, Role: role, TokenHash: hash,
		InvitedBy: actor.ID, ExpiresAt: s.now().Add(InvitationTTL),
	}, token, nil
}

func blankToNil(s *string) *string {
	if s == nil {
		return nil
	}
	if t := strings.TrimSpace(*s); t != "" {
		return &t
	}
	return nil
}

// ---------------------------------------------------------------------------
// Lectura
// ---------------------------------------------------------------------------

// List devuelve todas las organizaciones (menos las archivadas) a quien puede acceder
// a todas (el operador), y a los demás solo las suyas.
func (s *Service) List(ctx context.Context, actor identity.Principal) ([]Organization, error) {
	if actor.Can(access.PlatformOrganizationsAccessAll) {
		return s.repo.ListAll(ctx)
	}
	return s.repo.ListByUser(ctx, actor.ID)
}

// ListAll devuelve todas las organizaciones no archivadas (panel de plataforma).
func (s *Service) ListAll(ctx context.Context) ([]Organization, error) {
	return s.repo.ListAll(ctx)
}

// SearchInput son los filtros del listado de plataforma.
type SearchInput struct {
	Query    string
	Status   string
	PlanTier string
	Limit    int
	Offset   int
}

// Search es el listado paginado de la plataforma, con búsqueda por nombre o slug y
// filtros de estado y plan. Es el único que incluye las archivadas (si se pide).
// Quien llama debe poder acceder a todas las organizaciones (lo exige la ruta).
func (s *Service) Search(ctx context.Context, in SearchInput) ([]Organization, int64, error) {
	f := SearchFilter{Search: strings.TrimSpace(in.Query), Limit: in.Limit, Offset: in.Offset}
	if in.Status != "" {
		st := Status(in.Status)
		if st != StatusActive && st != StatusSuspended && st != StatusArchived {
			return nil, 0, apperr.Invalid("status debe ser active, suspended o archived")
		}
		f.Status = st
	}
	if in.PlanTier != "" {
		if !plans.ValidTier(in.PlanTier) {
			return nil, 0, apperr.Invalid("plan_tier desconocido: " + in.PlanTier)
		}
		f.PlanTier = in.PlanTier
	}
	return s.repo.Search(ctx, f)
}

// Get devuelve una organización por id, con aislamiento entre inquilinos.
//
// Distingue "no existe" (404) de "no es tuya" (403): permite enumerar qué
// organizaciones hay. Es el comportamiento heredado de domicilia-api, fijado a
// propósito por una prueba; la práctica habitual sería un 404 en ambos casos.
func (s *Service) Get(ctx context.Context, actor identity.Principal, id uuid.UUID) (Organization, error) {
	org, err := s.repo.OrganizationByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Organization{}, apperr.NotFound("organización no encontrada")
	}
	if err != nil {
		return Organization{}, err
	}
	if err := s.ensureAccessible(actor, org); err != nil {
		return Organization{}, err
	}
	return org, nil
}

// GetBySlug es Get por slug. Mismo aislamiento y misma advertencia.
func (s *Service) GetBySlug(ctx context.Context, actor identity.Principal, slug string) (Organization, error) {
	org, err := s.repo.OrganizationBySlug(ctx, strings.ToLower(slug))
	if errors.Is(err, ErrNotFound) {
		return Organization{}, apperr.NotFound("organización no encontrada")
	}
	if err != nil {
		return Organization{}, err
	}
	if err := s.ensureAccessible(actor, org); err != nil {
		return Organization{}, err
	}
	return org, nil
}

// Public es la cara del negocio para quien va a pedir: sin iniciar sesión, por slug.
// Una organización que no existe, o que no está activa, responde igual (404): no se
// revela si un negocio suspendido o archivado existe.
func (s *Service) Public(ctx context.Context, slug string) (PublicOrganization, error) {
	notFound := apperr.NotFound("organización no encontrada")
	org, err := s.repo.OrganizationBySlug(ctx, strings.ToLower(strings.TrimSpace(slug)))
	if errors.Is(err, ErrNotFound) {
		return PublicOrganization{}, notFound
	}
	if err != nil {
		return PublicOrganization{}, err
	}
	if org.Status != StatusActive {
		return PublicOrganization{}, notFound
	}
	set, err := s.repo.Settings(ctx, org.ID)
	if err != nil {
		return PublicOrganization{}, err
	}
	loc, lerr := time.LoadLocation(set.Timezone)
	if lerr != nil {
		loc = time.UTC
	}
	return PublicOrganization{
		Name: org.Name, Slug: org.Slug, Description: org.Description,
		LogoURL: set.LogoURL, City: set.City,
		Timezone: set.Timezone, Locale: set.Locale, Currency: set.Currency,
		BusinessHours: set.BusinessHours, OpenNow: set.BusinessHours.OpenAt(s.now(), loc),
	}, nil
}

// PublicList es el directorio de la app de cliente: organizaciones activas
// para elegir con cuál pedir. Sin iniciar sesión, igual que Public.
func (s *Service) PublicList(ctx context.Context, query string, limit, offset int) ([]PublicOrganizationSummary, int64, error) {
	return s.repo.PublicList(ctx, strings.TrimSpace(query), limit, offset)
}

// ---------------------------------------------------------------------------
// Perfil y ajustes
// ---------------------------------------------------------------------------

// ProfileInput es el cambio al perfil. nil no toca el campo; una descripción vacía la
// deja en blanco.
type ProfileInput struct {
	Name        *string
	Description *string
}

// UpdateProfile cambia el nombre y la descripción. El slug no cambia nunca: es la
// dirección pública del negocio.
func (s *Service) UpdateProfile(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in ProfileInput) (Organization, error) {
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgSettingsManage, true); err != nil {
		return Organization{}, err
	}
	var change ProfileChange
	if in.Name == nil && in.Description == nil {
		return Organization{}, apperr.Invalid("envía name o description")
	}
	if in.Name != nil {
		name, err := validate.Required("name", *in.Name, maxName)
		if err != nil {
			return Organization{}, err
		}
		if taken, err := s.repo.NameExists(ctx, name, orgID); err != nil {
			return Organization{}, err
		} else if taken {
			return Organization{}, apperr.Conflict("ya existe una organización con ese nombre")
		}
		change.Name = &name
	}
	if in.Description != nil {
		if err := validate.MaxLen("description", *in.Description, maxDescription); err != nil {
			return Organization{}, err
		}
		change.SetDescription = true
		change.Description = blankToNil(in.Description)
	}
	org, err := s.repo.UpdateProfile(ctx, orgID, actor.ID, change)
	switch {
	case errors.Is(err, ErrNotFound):
		return Organization{}, apperr.NotFound("organización no encontrada")
	case errors.Is(err, ErrDuplicate):
		return Organization{}, apperr.Conflict("ya existe una organización con ese nombre")
	}
	return org, err
}

// Settings devuelve los ajustes del negocio.
func (s *Service) Settings(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (Settings, error) {
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgSettingsRead, false); err != nil {
		return Settings{}, err
	}
	return s.repo.Settings(ctx, orgID)
}

// UpdateSettings aplica un cambio parcial a los ajustes.
func (s *Service) UpdateSettings(ctx context.Context, actor identity.Principal, orgID uuid.UUID, patch SettingsPatch) (Settings, error) {
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgSettingsManage, true); err != nil {
		return Settings{}, err
	}
	set, err := s.repo.UpdateSettings(ctx, orgID, actor.ID, patch.Apply)
	if errors.Is(err, ErrNotFound) {
		return Settings{}, apperr.NotFound("organización no encontrada")
	}
	return set, err
}

// ---------------------------------------------------------------------------
// Ciclo de vida (solo plataforma: lo exigen las rutas)
// ---------------------------------------------------------------------------

// Suspend suspende una organización activa (impago, revisión). Exige un motivo.
func (s *Service) Suspend(ctx context.Context, actor identity.Principal, id uuid.UUID, reason string) (Organization, error) {
	return s.transitionWithReason(ctx, actor, id, OpSuspend, reason)
}

// Archive da de baja una organización (baja lógica: sus datos se conservan). Exige un
// motivo.
func (s *Service) Archive(ctx context.Context, actor identity.Principal, id uuid.UUID, reason string) (Organization, error) {
	return s.transitionWithReason(ctx, actor, id, OpArchive, reason)
}

// Reactivate vuelve a activar una suspendida.
func (s *Service) Reactivate(ctx context.Context, actor identity.Principal, id uuid.UUID, reason *string) (Organization, error) {
	return s.transition(ctx, actor, id, OpReactivate, reason)
}

// Restore saca a una organización del archivo. Vuelve SUSPENDIDA: reactivarla es una
// decisión aparte, no un efecto colateral de restaurarla.
func (s *Service) Restore(ctx context.Context, actor identity.Principal, id uuid.UUID, reason *string) (Organization, error) {
	return s.transition(ctx, actor, id, OpRestore, reason)
}

func (s *Service) transitionWithReason(ctx context.Context, actor identity.Principal, id uuid.UUID, op Operation, reason string) (Organization, error) {
	r, err := validate.Required("reason", reason, maxReason)
	if err != nil {
		return Organization{}, err
	}
	return s.transition(ctx, actor, id, op, &r)
}

func (s *Service) transition(ctx context.Context, actor identity.Principal, id uuid.UUID, op Operation, reason *string) (Organization, error) {
	if reason != nil {
		if err := validate.MaxLen("reason", *reason, maxReason); err != nil {
			return Organization{}, err
		}
		reason = blankToNil(reason)
	}
	org, err := s.repo.Transition(ctx, id, op, reason, actor.ID)
	var te *TransitionError
	switch {
	case errors.Is(err, ErrNotFound):
		return Organization{}, apperr.NotFound("organización no encontrada")
	case errors.As(err, &te):
		return Organization{}, apperr.Conflict("no se puede " + te.Op.Label + " una organización en estado " + string(te.From))
	}
	return org, err
}

// ---------------------------------------------------------------------------
// Reglas de acceso
// ---------------------------------------------------------------------------

// ensureAccessible exige pertenecer a la organización y que esté disponible. Quien
// puede acceder a todas (el operador) entra siempre: debe poder inspeccionar lo que
// suspendió o archivó.
func (s *Service) ensureAccessible(actor identity.Principal, org Organization) error {
	if !actor.IsMember(org.ID) {
		return apperr.Forbidden("acceso denegado")
	}
	if actor.Can(access.PlatformOrganizationsAccessAll) {
		return nil
	}
	switch org.Status {
	case StatusArchived:
		return apperr.NotFound("organización no encontrada")
	case StatusSuspended:
		return apperr.Forbidden("la organización está suspendida")
	}
	return nil
}

// requirePerm exige un permiso de organización. Responde 403 tanto si la
// organización no existe como si no se tiene el permiso: quien no puede no
// averigua si existe.
func (s *Service) requirePerm(actor identity.Principal, orgID uuid.UUID, perm access.Permission) error {
	if !actor.CanInOrg(orgID, perm) {
		return apperr.Forbidden("no tienes permiso para esta acción en la organización")
	}
	return nil
}

// openOrg es la puerta de todo lo que se hace DENTRO de una organización: exige el
// permiso (403 primero, así quien no puede no averigua si existe) y que la
// organización esté disponible.
//
//   - archivada: para un miembro "no existe" (404); el operador la lee, pero no la
//     modifica (409): primero hay que restaurarla.
//   - suspendida: sus miembros no la abren ni la administran (403); el operador sí.
func (s *Service) openOrg(ctx context.Context, actor identity.Principal, orgID uuid.UUID, perm access.Permission, write bool) (Organization, error) {
	if err := s.requirePerm(actor, orgID, perm); err != nil {
		return Organization{}, err
	}
	org, err := s.organizationOrNotFound(ctx, orgID)
	if err != nil {
		return Organization{}, err
	}
	operator := actor.Can(access.PlatformOrganizationsAccessAll)
	switch org.Status {
	case StatusArchived:
		if !operator {
			return Organization{}, apperr.NotFound("organización no encontrada")
		}
		if write {
			return Organization{}, apperr.Conflict("la organización está archivada: restáurala primero")
		}
	case StatusSuspended:
		if !operator {
			return Organization{}, apperr.Forbidden("la organización está suspendida")
		}
	}
	return org, nil
}

func (s *Service) organizationOrNotFound(ctx context.Context, id uuid.UUID) (Organization, error) {
	org, err := s.repo.OrganizationByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Organization{}, apperr.NotFound("organización no encontrada")
	}
	return org, err
}
