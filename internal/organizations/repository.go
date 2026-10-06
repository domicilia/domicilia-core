package organizations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/roles"
	"github.com/domicilia/domicilia-core/internal/store"
)

// SearchFilter acota el listado de la plataforma. Los campos vacíos no filtran.
type SearchFilter struct {
	Search   string // ya sin escapar: el repositorio escapa los comodines de LIKE
	Status   Status
	PlanTier string
	Limit    int
	Offset   int
}

// NewOrganization son los datos para dar de alta una organización completa.
type NewOrganization struct {
	ID          uuid.UUID
	Name        string
	Slug        string
	Description *string
	PlanTier    string
	Settings    Settings
	Actor       uuid.UUID
	// AdminInvitation, si se envía, se emite en la misma transacción.
	AdminInvitation *NewInvitation
}

// ProfileChange es el cambio al perfil (nombre y descripción).
type ProfileChange struct {
	Name           *string
	SetDescription bool
	Description    *string
}

// TransitionError es una operación que el estado actual no permite.
type TransitionError struct {
	Op   Operation
	From Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%v: %s desde %s", ErrInvalidTransition, e.Op.Label, e.From)
}

// Is hace que errors.Is(err, ErrInvalidTransition) funcione.
func (e *TransitionError) Is(target error) bool { return target == ErrInvalidTransition }

type pgRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool, q: store.New(pool)}
}

func toOrganization(o store.Organization) Organization {
	return Organization{
		ID:           o.ID,
		Name:         o.Name,
		Slug:         o.Slug,
		Description:  o.Description,
		Status:       Status(o.Status),
		IsActive:     Status(o.Status) == StatusActive,
		StatusReason: o.StatusReason,
		PlanTier:     o.PlanTier,
		CreatedAt:    o.CreatedAt,
	}
}

func toOrganizations(rows []store.Organization) []Organization {
	out := make([]Organization, 0, len(rows))
	for _, o := range rows {
		out = append(out, toOrganization(o))
	}
	return out
}

func newMember(userID, orgID uuid.UUID, role roles.Role) Member {
	return Member{UserID: userID, OrganizationID: orgID, Role: role.Code, RoleID: role.ID, RoleName: role.Name}
}

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toSettings(row store.OrganizationSetting) (Settings, error) {
	hours := BusinessHours{}
	if len(row.BusinessHours) > 0 {
		if err := json.Unmarshal(row.BusinessHours, &hours); err != nil {
			return Settings{}, fmt.Errorf("organizations: horarios de %s: %w", row.OrganizationID, err)
		}
	}
	return Settings{
		LegalName: row.LegalName, TaxID: row.TaxID, ContactEmail: row.ContactEmail, ContactPhone: row.ContactPhone,
		Address: row.Address, City: row.City, LogoURL: row.LogoUrl,
		Timezone: row.Timezone, Locale: row.Locale, Currency: row.Currency,
		BusinessHours: hours, UpdatedAt: row.UpdatedAt,
	}, nil
}

func settingsParams(orgID uuid.UUID, s Settings) (store.UpdateOrganizationSettingsParams, error) {
	hours := s.BusinessHours
	if hours == nil {
		hours = BusinessHours{}
	}
	raw, err := json.Marshal(hours)
	if err != nil {
		return store.UpdateOrganizationSettingsParams{}, fmt.Errorf("organizations: codificar horarios: %w", err)
	}
	return store.UpdateOrganizationSettingsParams{
		OrganizationID: orgID,
		LegalName:      s.LegalName, TaxID: s.TaxID, ContactEmail: s.ContactEmail, ContactPhone: s.ContactPhone,
		Address: s.Address, City: s.City, LogoUrl: s.LogoURL,
		Timezone: s.Timezone, Locale: s.Locale, Currency: s.Currency, BusinessHours: raw,
	}, nil
}

// ---------------------------------------------------------------------------
// Organizaciones
// ---------------------------------------------------------------------------

func (r *pgRepository) OrganizationByID(ctx context.Context, id uuid.UUID) (Organization, error) {
	o, err := r.q.GetOrganization(ctx, id)
	if db.IsNoRows(err) {
		return Organization{}, ErrNotFound
	}
	if err != nil {
		return Organization{}, fmt.Errorf("organizations: leer por id: %w", err)
	}
	return toOrganization(o), nil
}

func (r *pgRepository) OrganizationBySlug(ctx context.Context, slug string) (Organization, error) {
	o, err := r.q.GetOrganizationBySlug(ctx, slug)
	if db.IsNoRows(err) {
		return Organization{}, ErrNotFound
	}
	if err != nil {
		return Organization{}, fmt.Errorf("organizations: leer por slug: %w", err)
	}
	return toOrganization(o), nil
}

func (r *pgRepository) NameExists(ctx context.Context, name string, exceptID uuid.UUID) (bool, error) {
	ok, err := r.q.OrganizationNameExists(ctx, store.OrganizationNameExistsParams{Name: name, ExceptID: nullUUID(exceptID)})
	if err != nil {
		return false, fmt.Errorf("organizations: comprobar nombre: %w", err)
	}
	return ok, nil
}

func (r *pgRepository) SlugExists(ctx context.Context, slug string) (bool, error) {
	ok, err := r.q.OrganizationSlugExists(ctx, slug)
	if err != nil {
		return false, fmt.Errorf("organizations: comprobar slug: %w", err)
	}
	return ok, nil
}

func (r *pgRepository) ListAll(ctx context.Context) ([]Organization, error) {
	rows, err := r.q.ListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("organizations: listar: %w", err)
	}
	return toOrganizations(rows), nil
}

func (r *pgRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]Organization, error) {
	rows, err := r.q.ListOrganizationsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("organizations: listar las del usuario: %w", err)
	}
	return toOrganizations(rows), nil
}

func (r *pgRepository) Search(ctx context.Context, f SearchFilter) ([]Organization, int64, error) {
	var search, status, plan *string
	if f.Search != "" {
		s := db.EscapeLike(f.Search)
		search = &s
	}
	if f.Status != "" {
		s := string(f.Status)
		status = &s
	}
	plan = optString(f.PlanTier)
	rows, err := r.q.SearchOrganizations(ctx, store.SearchOrganizationsParams{
		Search: search, Status: status, PlanTier: plan,
		PageSize: int32(f.Limit), PageOffset: int32(f.Offset), //nolint:gosec // acotados por el paginador
	})
	if err != nil {
		return nil, 0, fmt.Errorf("organizations: buscar: %w", err)
	}
	total, err := r.q.CountSearchOrganizations(ctx, store.CountSearchOrganizationsParams{Search: search, Status: status, PlanTier: plan})
	if err != nil {
		return nil, 0, fmt.Errorf("organizations: contar: %w", err)
	}
	return toOrganizations(rows), total, nil
}

func (r *pgRepository) PublicList(ctx context.Context, search string, limit, offset int) ([]PublicOrganizationSummary, int64, error) {
	var s *string
	if search != "" {
		v := db.EscapeLike(search)
		s = &v
	}
	rows, err := r.q.ListPublicOrganizations(ctx, store.ListPublicOrganizationsParams{
		Search: s, PageSize: int32(limit), PageOffset: int32(offset), //nolint:gosec // acotados por el paginador
	})
	if err != nil {
		return nil, 0, fmt.Errorf("organizations: directorio público: %w", err)
	}
	total, err := r.q.CountPublicOrganizations(ctx, s)
	if err != nil {
		return nil, 0, fmt.Errorf("organizations: contar directorio público: %w", err)
	}
	out := make([]PublicOrganizationSummary, len(rows))
	for i, row := range rows {
		out[i] = PublicOrganizationSummary{
			Name: row.Name, Slug: row.Slug, Description: row.Description,
			LogoURL: row.LogoUrl, City: row.City,
		}
	}
	return out, total, nil
}

func (r *pgRepository) Create(ctx context.Context, n NewOrganization) (Organization, *Invitation, error) {
	var org store.Organization
	var inv *Invitation
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		var err error
		if org, err = q.InsertOrganization(ctx, store.InsertOrganizationParams{
			ID: n.ID, Name: n.Name, Slug: n.Slug, Description: n.Description, PlanTier: n.PlanTier,
		}); err != nil {
			return err
		}
		if _, err = q.InsertOrganizationSettings(ctx, n.ID); err != nil {
			return err
		}
		params, err := settingsParams(n.ID, n.Settings)
		if err != nil {
			return err
		}
		if _, err = q.UpdateOrganizationSettings(ctx, params); err != nil {
			return err
		}
		if _, err = q.InsertSubscription(ctx, store.InsertSubscriptionParams{
			OrganizationID: n.ID, PlanTier: n.PlanTier, ChangedBy: nullUUID(n.Actor), Reason: optString("alta de la organización"),
		}); err != nil {
			return err
		}
		if err = (audit.Entry{
			ActorID: n.Actor, Action: audit.OrganizationCreated, OrganizationID: n.ID,
			Detail: map[string]any{"name": n.Name, "slug": n.Slug, "plan_tier": n.PlanTier},
		}).Insert(ctx, q); err != nil {
			return err
		}
		if n.AdminInvitation != nil {
			i, err := issueInvitation(ctx, q, *n.AdminInvitation)
			if err != nil {
				return err
			}
			inv = &i
		}
		return nil
	})
	if db.IsUniqueViolation(err) {
		return Organization{}, nil, ErrDuplicate
	}
	if err != nil {
		return Organization{}, nil, fmt.Errorf("organizations: crear: %w", err)
	}
	return toOrganization(org), inv, nil
}

func (r *pgRepository) UpdateProfile(ctx context.Context, id, actor uuid.UUID, c ProfileChange) (Organization, error) {
	var org store.Organization
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		before, err := q.GetOrganizationForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if org, err = q.UpdateOrganizationProfile(ctx, store.UpdateOrganizationProfileParams{
			ID: id, Name: c.Name, SetDescription: c.SetDescription, Description: c.Description,
		}); err != nil {
			return err
		}
		var changed []string
		if c.Name != nil && *c.Name != before.Name {
			changed = append(changed, "name")
		}
		if c.SetDescription {
			changed = append(changed, "description")
		}
		return audit.Entry{
			ActorID: actor, Action: audit.OrganizationUpdated, OrganizationID: id,
			Detail: map[string]any{"changed": changed},
		}.Insert(ctx, q)
	})
	switch {
	case db.IsNoRows(err):
		return Organization{}, ErrNotFound
	case db.IsUniqueViolation(err):
		return Organization{}, ErrDuplicate
	case err != nil:
		return Organization{}, fmt.Errorf("organizations: actualizar perfil: %w", err)
	}
	return toOrganization(org), nil
}

func (r *pgRepository) Transition(ctx context.Context, id uuid.UUID, op Operation, reason *string, actor uuid.UUID) (Organization, error) {
	var org store.Organization
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		cur, err := q.GetOrganizationForUpdate(ctx, id)
		if err != nil {
			return err
		}
		from := Status(cur.Status)
		if !op.CanApply(from) {
			return &TransitionError{Op: op, From: from}
		}
		if org, err = q.SetOrganizationStatus(ctx, store.SetOrganizationStatusParams{ID: id, Status: string(op.To), Reason: reason}); err != nil {
			return err
		}
		detail := map[string]any{"from": string(from), "to": string(op.To)}
		if reason != nil {
			detail["reason"] = *reason
		}
		return audit.Entry{ActorID: actor, Action: op.Action, OrganizationID: id, Detail: detail}.Insert(ctx, q)
	})
	var te *TransitionError
	switch {
	case db.IsNoRows(err):
		return Organization{}, ErrNotFound
	case errors.As(err, &te):
		return Organization{}, te
	case err != nil:
		return Organization{}, fmt.Errorf("organizations: cambiar estado: %w", err)
	}
	return toOrganization(org), nil
}

// ---------------------------------------------------------------------------
// Ajustes
// ---------------------------------------------------------------------------

// Settings devuelve los ajustes; una organización sin fila (anterior a la migración,
// o creada por fuera del servicio) devuelve los de por omisión sin escribir nada.
func (r *pgRepository) Settings(ctx context.Context, id uuid.UUID) (Settings, error) {
	row, err := r.q.GetOrganizationSettings(ctx, id)
	if db.IsNoRows(err) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("organizations: leer ajustes: %w", err)
	}
	return toSettings(row)
}

func (r *pgRepository) UpdateSettings(ctx context.Context, id, actor uuid.UUID, apply func(Settings) (Settings, []string, error)) (Settings, error) {
	var out Settings
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.GetOrganizationForUpdate(ctx, id); err != nil {
			return err
		}
		// Autocura: crea la fila si falta (sin efecto si ya existe).
		row, err := q.InsertOrganizationSettings(ctx, id)
		if err != nil {
			return err
		}
		cur, err := toSettings(row)
		if err != nil {
			return err
		}
		next, changed, err := apply(cur)
		if err != nil {
			return err
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		params, err := settingsParams(id, next)
		if err != nil {
			return err
		}
		saved, err := q.UpdateOrganizationSettings(ctx, params)
		if err != nil {
			return err
		}
		if out, err = toSettings(saved); err != nil {
			return err
		}
		return audit.Entry{
			ActorID: actor, Action: audit.OrganizationSettingsChanged, OrganizationID: id,
			Detail: map[string]any{"changed": changed},
		}.Insert(ctx, q)
	})
	switch {
	case db.IsNoRows(err):
		return Settings{}, ErrNotFound
	case err != nil && isBusiness(err):
		return Settings{}, err
	case err != nil:
		return Settings{}, fmt.Errorf("organizations: actualizar ajustes: %w", err)
	}
	return out, nil
}

// isBusiness dice si el error viene de la regla de negocio (apply) y no de la base:
// debe llegar tal cual al servicio.
func isBusiness(err error) bool {
	var ae *apperr.Error
	return errors.As(err, &ae)
}

// ---------------------------------------------------------------------------
// Miembros
// ---------------------------------------------------------------------------

// guardLastManager ejecuta un cambio de miembros y lo deshace si deja a la
// organización sin nadie que pueda administrarlos. Un bloqueo por organización
// serializa los cambios simultáneos: sin él, dos peticiones que cada una "deja a
// uno" dejarían a cero.
//
// Solo protege a una organización que TIENE administrador: una sin ninguno (solo
// empleados) no queda bloqueada para siempre.
func guardLastManager(ctx context.Context, q *store.Queries, orgID uuid.UUID, change func() error) error {
	if err := q.LockOrganizationMembers(ctx, orgID.String()); err != nil {
		return err
	}
	before, err := q.CountOrgManagers(ctx, orgID)
	if err != nil {
		return err
	}
	if err := change(); err != nil {
		return err
	}
	after, err := q.CountOrgManagers(ctx, orgID)
	if err != nil {
		return err
	}
	if before > 0 && after == 0 {
		return ErrLastManager
	}
	return nil
}

func (r *pgRepository) AddMember(ctx context.Context, userID, orgID uuid.UUID, role roles.Role, actor uuid.UUID) (Member, error) {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if err := q.InsertMembership(ctx, store.InsertMembershipParams{UserID: userID, OrganizationID: orgID, RoleID: role.ID}); err != nil {
			return err
		}
		return audit.Entry{
			ActorID: actor, Action: audit.MemberAdded, TargetUserID: userID, OrganizationID: orgID,
			RoleID: role.ID, RoleCode: role.Code,
		}.Insert(ctx, q)
	})
	if db.IsUniqueViolation(err) {
		return Member{}, ErrAlreadyMember
	}
	if db.IsForeignKeyViolation(err) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("organizations: añadir miembro: %w", err)
	}
	return newMember(userID, orgID, role), nil
}

func (r *pgRepository) ChangeMemberRole(ctx context.Context, userID, orgID uuid.UUID, role roles.Role, actor uuid.UUID) (Member, error) {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		return guardLastManager(ctx, q, orgID, func() error {
			before, err := q.GetMembership(ctx, store.GetMembershipParams{UserID: userID, OrganizationID: orgID})
			if err != nil {
				return err
			}
			if _, err := q.UpdateMembershipRole(ctx, store.UpdateMembershipRoleParams{UserID: userID, OrganizationID: orgID, RoleID: role.ID}); err != nil {
				return err
			}
			return audit.Entry{
				ActorID: actor, Action: audit.MemberRoleChanged, TargetUserID: userID, OrganizationID: orgID,
				RoleID: role.ID, RoleCode: role.Code, Detail: map[string]any{"from": before.RoleCode, "to": role.Code},
			}.Insert(ctx, q)
		})
	})
	if db.IsNoRows(err) {
		return Member{}, ErrNotFound
	}
	if errors.Is(err, ErrLastManager) {
		return Member{}, err
	}
	if err != nil {
		return Member{}, fmt.Errorf("organizations: cambiar rol: %w", err)
	}
	return newMember(userID, orgID, role), nil
}

func (r *pgRepository) RemoveMember(ctx context.Context, userID, orgID, actor uuid.UUID) (bool, error) {
	removed := false
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		return guardLastManager(ctx, q, orgID, func() error {
			before, err := q.GetMembership(ctx, store.GetMembershipParams{UserID: userID, OrganizationID: orgID})
			if db.IsNoRows(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{UserID: userID, OrganizationID: orgID}); err != nil {
				return err
			}
			removed = true
			return audit.Entry{
				ActorID: actor, Action: audit.MemberRemoved, TargetUserID: userID, OrganizationID: orgID,
				RoleID: before.RoleID, RoleCode: before.RoleCode,
			}.Insert(ctx, q)
		})
	})
	if errors.Is(err, ErrLastManager) {
		return false, err
	}
	if err != nil {
		return false, fmt.Errorf("organizations: quitar miembro: %w", err)
	}
	return removed, nil
}

func (r *pgRepository) Members(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	rows, err := r.q.ListMembers(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("organizations: listar miembros: %w", err)
	}
	out := make([]Member, 0, len(rows))
	for _, m := range rows {
		out = append(out, Member{
			UserID: m.UserID, OrganizationID: m.OrganizationID,
			Role: m.RoleCode, RoleID: m.RoleID, RoleName: m.RoleName,
		})
	}
	return out, nil
}

func (r *pgRepository) UserExists(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := r.q.GetUser(ctx, id)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("organizations: comprobar usuario: %w", err)
	}
	return true, nil
}

func (r *pgRepository) EmailRegistered(ctx context.Context, email string) (bool, error) {
	ok, err := r.q.UserEmailExists(ctx, email)
	if err != nil {
		return false, fmt.Errorf("organizations: comprobar correo: %w", err)
	}
	return ok, nil
}

func (r *pgRepository) IsMemberByEmail(ctx context.Context, orgID uuid.UUID, email string) (bool, error) {
	u, err := r.q.GetUserByEmail(ctx, email)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("organizations: buscar usuario: %w", err)
	}
	_, err = r.q.GetMembership(ctx, store.GetMembershipParams{UserID: u.ID, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("organizations: comprobar membresía: %w", err)
	}
	return true, nil
}

func (r *pgRepository) CreateStaff(ctx context.Context, id uuid.UUID, email string, fullName *string, orgID uuid.UUID, role roles.Role, actor uuid.UUID) (Member, error) {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.InsertUser(ctx, store.InsertUserParams{ID: id, Email: email, FullName: fullName}); err != nil {
			return err
		}
		if err := q.InsertMembership(ctx, store.InsertMembershipParams{UserID: id, OrganizationID: orgID, RoleID: role.ID}); err != nil {
			return err
		}
		return audit.Entry{
			ActorID: actor, Action: audit.MemberInvited, TargetUserID: id, OrganizationID: orgID,
			RoleID: role.ID, RoleCode: role.Code,
		}.Insert(ctx, q)
	})
	if db.IsUniqueViolation(err) {
		return Member{}, ErrEmailTaken
	}
	if err != nil {
		return Member{}, fmt.Errorf("organizations: crear staff: %w", err)
	}
	return newMember(id, orgID, role), nil
}
