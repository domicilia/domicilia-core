package roles

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

// Service reúne las reglas de negocio de roles y permisos.
type Service struct{ repo Repository }

// NewService crea el servicio.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Permissions devuelve el catálogo de permisos.
func (s *Service) Permissions() []access.Def { return access.Catalog }

// ---------------------------------------------------------------------------
// Consulta (consola de plataforma)
// ---------------------------------------------------------------------------

// List devuelve los roles, opcionalmente por alcance u organización. Quien llama
// debe tener PlatformRolesRead (lo exige la ruta).
func (s *Service) List(ctx context.Context, scope string, orgID *uuid.UUID) ([]Role, error) {
	var sc *access.Scope
	if scope != "" {
		v := access.Scope(scope)
		if v != access.ScopePlatform && v != access.ScopeOrganization {
			return nil, apperr.Invalid("scope debe ser platform u organization")
		}
		sc = &v
	}
	return s.repo.List(ctx, sc, orgID)
}

// Get devuelve un rol. Quien llama debe tener PlatformRolesRead.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Role, error) {
	r, err := s.repo.Role(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Role{}, apperr.NotFound("rol no encontrado")
	}
	return r, err
}

// ---------------------------------------------------------------------------
// Roles personalizados de plataforma
// ---------------------------------------------------------------------------

// CreatePlatformRole crea un rol de plataforma. Quien llama debe tener
// PlatformRolesManage (lo exige la ruta) y solo puede incluir permisos que ya tiene.
func (s *Service) CreatePlatformRole(ctx context.Context, actor identity.Principal, in CreateInput) (Role, error) {
	name, perms, err := s.validateCreate(access.ScopePlatform, in)
	if err != nil {
		return Role{}, err
	}
	if err := s.authorizePermissions(actor, uuid.Nil, access.ScopePlatform, perms); err != nil {
		return Role{}, err
	}
	if err := s.ensureCodeFree(ctx, access.ScopePlatform, in.Code); err != nil {
		return Role{}, err
	}
	return s.create(ctx, NewRole{
		Code: in.Code, Name: name, Description: in.Description, Scope: access.ScopePlatform,
		OrgAssignable: false, Permissions: perms,
	}, actor)
}

// UpdatePlatformRole edita un rol personalizado de plataforma.
func (s *Service) UpdatePlatformRole(ctx context.Context, actor identity.Principal, id uuid.UUID, in UpdateInput) (Role, error) {
	cur, err := s.editable(ctx, id, access.ScopePlatform, uuid.Nil)
	if err != nil {
		return Role{}, err
	}
	return s.update(ctx, actor, cur, uuid.Nil, in)
}

// DeletePlatformRole borra un rol personalizado de plataforma que no esté en uso.
func (s *Service) DeletePlatformRole(ctx context.Context, actor identity.Principal, id uuid.UUID) error {
	if _, err := s.editable(ctx, id, access.ScopePlatform, uuid.Nil); err != nil {
		return err
	}
	return s.delete(ctx, actor, id)
}

// ---------------------------------------------------------------------------
// Roles personalizados de una organización
// ---------------------------------------------------------------------------

// ListForOrg devuelve los roles que se pueden dar en una organización: los de
// sistema y los personalizados de ella.
func (s *Service) ListForOrg(ctx context.Context, actor identity.Principal, orgID uuid.UUID) ([]Role, error) {
	if err := s.requireOrg(ctx, actor, orgID, access.OrgRolesRead); err != nil {
		return nil, err
	}
	return s.repo.ListForOrganization(ctx, orgID)
}

// CreateOrgRole crea un rol personalizado de una organización. Un admin de
// organización solo puede incluir permisos delegables que él mismo tiene; el
// superadmin, cualquiera de organización.
func (s *Service) CreateOrgRole(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in CreateInput) (Role, error) {
	if err := s.requireOrg(ctx, actor, orgID, access.OrgRolesManage); err != nil {
		return Role{}, err
	}
	name, perms, err := s.validateCreate(access.ScopeOrganization, in)
	if err != nil {
		return Role{}, err
	}
	if err := s.authorizePermissions(actor, orgID, access.ScopeOrganization, perms); err != nil {
		return Role{}, err
	}
	if err := s.ensureCodeFree(ctx, access.ScopeOrganization, in.Code); err != nil {
		return Role{}, err
	}
	assignable := true
	if in.OrgAssignable != nil {
		assignable = *in.OrgAssignable
	}
	return s.create(ctx, NewRole{
		Code: in.Code, Name: name, Description: in.Description, Scope: access.ScopeOrganization,
		OrganizationID: orgID, OrgAssignable: assignable, Permissions: perms,
	}, actor)
}

// UpdateOrgRole edita un rol personalizado de la organización.
func (s *Service) UpdateOrgRole(ctx context.Context, actor identity.Principal, orgID, roleID uuid.UUID, in UpdateInput) (Role, error) {
	if err := s.requireOrg(ctx, actor, orgID, access.OrgRolesManage); err != nil {
		return Role{}, err
	}
	cur, err := s.editable(ctx, roleID, access.ScopeOrganization, orgID)
	if err != nil {
		return Role{}, err
	}
	return s.update(ctx, actor, cur, orgID, in)
}

// DeleteOrgRole borra un rol personalizado de la organización que no esté en uso.
func (s *Service) DeleteOrgRole(ctx context.Context, actor identity.Principal, orgID, roleID uuid.UUID) error {
	if err := s.requireOrg(ctx, actor, orgID, access.OrgRolesManage); err != nil {
		return err
	}
	if _, err := s.editable(ctx, roleID, access.ScopeOrganization, orgID); err != nil {
		return err
	}
	return s.delete(ctx, actor, roleID)
}

// ResolveOrgRole convierte una referencia (id o código) en el rol de organización
// que se va a asignar, comprobando que quien asigna puede hacerlo. Lo usa
// organizations al sumar, invitar y cambiar de rol a un miembro.
//
// Reglas: el rol debe ser de organización y estar disponible en ESA organización
// (de sistema, o personalizado de ella). Quien no tiene PlatformRolesManage solo
// asigna roles org_assignable y con permisos que ya tiene: así el rol de
// administrador no se propaga solo.
func (s *Service) ResolveOrgRole(ctx context.Context, actor identity.Principal, orgID uuid.UUID, ref string) (Role, error) {
	if ref == "" {
		return Role{}, apperr.Invalid("role es obligatorio")
	}
	role, err := s.lookupOrgRole(ctx, orgID, ref)
	if errors.Is(err, ErrNotFound) {
		return Role{}, apperr.Invalid("rol desconocido para esta organización: " + ref)
	}
	if err != nil {
		return Role{}, err
	}
	if actor.Can(access.PlatformRolesManage) {
		return role, nil
	}
	if !role.OrgAssignable {
		return Role{}, apperr.Forbidden("solo un administrador general puede asignar el rol " + role.Code)
	}
	if !actor.PermissionsInOrg(orgID).Contains(role.PermissionSet()) {
		return Role{}, apperr.Forbidden("no puedes asignar un rol con más permisos que los tuyos")
	}
	return role, nil
}

func (s *Service) lookupOrgRole(ctx context.Context, orgID uuid.UUID, ref string) (Role, error) {
	var role Role
	var err error
	if id, perr := uuid.Parse(ref); perr == nil {
		role, err = s.repo.Role(ctx, id)
	} else {
		// Un rol personalizado de la organización tiene prioridad sobre el de sistema
		// del mismo código; por eso no se permite crear uno que choque (ensureCodeFree).
		role, err = s.repo.OrgRole(ctx, orgID, ref)
		if errors.Is(err, ErrNotFound) {
			role, err = s.repo.SystemRole(ctx, access.ScopeOrganization, ref)
		}
	}
	if err != nil {
		return Role{}, err
	}
	if role.Scope != access.ScopeOrganization || (role.OrganizationID != nil && *role.OrganizationID != orgID) {
		return Role{}, ErrNotFound // de otra organización o de plataforma: no se revela que existe
	}
	return role, nil
}

// ---------------------------------------------------------------------------
// Roles de plataforma de un usuario
// ---------------------------------------------------------------------------

// GrantPlatformRole da un rol de plataforma (por id o código). Quien llama debe
// tener PlatformRolesManage (lo exige la ruta) y no puede dar un rol con más
// permisos de los que tiene. Es idempotente.
func (s *Service) GrantPlatformRole(ctx context.Context, actor identity.Principal, userID uuid.UUID, ref string) (PlatformRoles, error) {
	if ref == "" {
		return PlatformRoles{}, apperr.Invalid("role es obligatorio")
	}
	role, err := s.platformRole(ctx, ref)
	if errors.Is(err, ErrNotFound) {
		return PlatformRoles{}, apperr.Invalid("rol de plataforma desconocido: " + ref)
	}
	if err != nil {
		return PlatformRoles{}, err
	}
	if !actor.Permissions.Contains(role.PermissionSet()) {
		return PlatformRoles{}, apperr.Forbidden("no puedes otorgar un rol con más permisos que los tuyos")
	}
	if err := s.requireUser(ctx, userID); err != nil {
		return PlatformRoles{}, err
	}
	if _, err := s.repo.Grant(ctx, userID, role, actor.ID); errors.Is(err, ErrNotFound) {
		return PlatformRoles{}, apperr.NotFound("usuario no encontrado")
	} else if err != nil {
		return PlatformRoles{}, err
	}
	return s.platformRoles(ctx, userID)
}

// RevokePlatformRole quita un rol de plataforma. No se puede quitar uno más
// poderoso que los propios, ni el propio rol de superadmin, ni el del último
// superadmin activo: la plataforma no puede quedarse sin operador.
func (s *Service) RevokePlatformRole(ctx context.Context, actor identity.Principal, userID uuid.UUID, ref string) (PlatformRoles, error) {
	role, err := s.platformRole(ctx, ref)
	if errors.Is(err, ErrNotFound) {
		return PlatformRoles{}, apperr.NotFound("rol de plataforma no encontrado")
	}
	if err != nil {
		return PlatformRoles{}, err
	}
	if !actor.Permissions.Contains(role.PermissionSet()) {
		return PlatformRoles{}, apperr.Forbidden("no puedes quitar un rol con más permisos que los tuyos")
	}
	if actor.ID == userID && role.Code == access.RoleSuperadmin {
		return PlatformRoles{}, apperr.Conflict("no puedes quitarte tu propio rol de superadmin")
	}
	if err := s.requireUser(ctx, userID); err != nil {
		return PlatformRoles{}, err
	}
	if _, err := s.repo.Revoke(ctx, userID, role, actor.ID); errors.Is(err, ErrLastSuperadmin) {
		return PlatformRoles{}, apperr.Conflict("es el último superadmin activo: la plataforma no puede quedarse sin uno")
	} else if err != nil {
		return PlatformRoles{}, err
	}
	return s.platformRoles(ctx, userID)
}

func (s *Service) platformRole(ctx context.Context, ref string) (Role, error) {
	var role Role
	var err error
	if id, perr := uuid.Parse(ref); perr == nil {
		role, err = s.repo.Role(ctx, id)
	} else {
		role, err = s.repo.SystemRole(ctx, access.ScopePlatform, ref)
	}
	if err != nil {
		return Role{}, err
	}
	if role.Scope != access.ScopePlatform {
		return Role{}, ErrNotFound
	}
	return role, nil
}

func (s *Service) platformRoles(ctx context.Context, userID uuid.UUID) (PlatformRoles, error) {
	rs, err := s.repo.PlatformRoles(ctx, userID)
	if err != nil {
		return PlatformRoles{}, err
	}
	return PlatformRoles{UserID: userID, Roles: rs}, nil
}

func (s *Service) requireUser(ctx context.Context, id uuid.UUID) error {
	ok, err := s.repo.UserExists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("usuario no encontrado")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Auditoría
// ---------------------------------------------------------------------------

// PlatformAudit lista la auditoría de toda la plataforma. Quien llama debe tener
// PlatformAuditRead (lo exige la ruta).
func (s *Service) PlatformAudit(ctx context.Context, f audit.Filter) ([]audit.Record, int64, error) {
	return s.repo.Audit(ctx, f)
}

// OrgAudit lista la auditoría de una organización.
func (s *Service) OrgAudit(ctx context.Context, actor identity.Principal, orgID uuid.UUID, f audit.Filter) ([]audit.Record, int64, error) {
	if err := s.requireOrg(ctx, actor, orgID, access.OrgAuditRead); err != nil {
		return nil, 0, err
	}
	f.OrganizationID = &orgID
	return s.repo.Audit(ctx, f)
}

// ---------------------------------------------------------------------------
// Compartido
// ---------------------------------------------------------------------------

// requireOrg exige un permiso de organización. Responde 403 tanto si la
// organización no existe como si no se tiene el permiso: quien no puede no
// averigua si existe.
func (s *Service) requireOrg(ctx context.Context, actor identity.Principal, orgID uuid.UUID, perm access.Permission) error {
	if !actor.CanInOrg(orgID, perm) {
		return apperr.Forbidden("acceso denegado")
	}
	ok, err := s.repo.OrganizationExists(ctx, orgID)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NotFound("organización no encontrada")
	}
	return nil
}

// validateCreate valida el cuerpo de creación y devuelve el nombre recortado y los
// permisos normalizados.
func (s *Service) validateCreate(scope access.Scope, in CreateInput) (string, []string, error) {
	if !access.ValidCode(in.Code) {
		return "", nil, apperr.Invalid("code debe tener de 2 a 50 caracteres: minúsculas, dígitos, guion y guion bajo, empezando con una letra")
	}
	name, err := validate.Required("name", in.Name, maxName)
	if err != nil {
		return "", nil, err
	}
	if err := validate.OptionalMaxLen("description", in.Description, maxDescription); err != nil {
		return "", nil, err
	}
	perms, err := normalizePermissions(scope, in.Permissions)
	if err != nil {
		return "", nil, err
	}
	return name, perms, nil
}

// normalizePermissions quita duplicados, ordena y comprueba que cada permiso
// exista y sea del alcance del rol.
func normalizePermissions(scope access.Scope, codes []string) ([]string, error) {
	out := slices.Clone(codes)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	for _, c := range out {
		def, ok := access.Lookup(c)
		if !ok {
			return nil, apperr.Invalid("permiso desconocido: " + c)
		}
		if def.Scope != scope {
			return nil, apperr.Invalid(fmt.Sprintf("el permiso %s es de alcance %s y el rol es de alcance %s", c, def.Scope, scope))
		}
	}
	return out, nil
}

// authorizePermissions aplica "nadie otorga más de lo que tiene".
func (s *Service) authorizePermissions(actor identity.Principal, orgID uuid.UUID, scope access.Scope, perms []string) error {
	requested := access.NewSet(perms...)
	if scope == access.ScopePlatform {
		if !actor.Permissions.Contains(requested) {
			return apperr.Forbidden("no puedes otorgar permisos que no tienes")
		}
		return nil
	}
	if actor.Can(access.PlatformRolesManage) {
		return nil // el operador puede armar cualquier rol de organización
	}
	held := actor.PermissionsInOrg(orgID)
	for _, c := range perms {
		def, _ := access.Lookup(c)
		if !def.Delegable {
			return apperr.Forbidden("el permiso " + c + " no se puede delegar en un rol personalizado")
		}
		if !held.Has(access.Permission(c)) {
			return apperr.Forbidden("no puedes otorgar un permiso que no tienes: " + c)
		}
	}
	return nil
}

// ensureCodeFree impide que un rol personalizado use el código de uno de sistema:
// se confundirían al asignarlos por código.
func (s *Service) ensureCodeFree(ctx context.Context, scope access.Scope, code string) error {
	_, err := s.repo.SystemRole(ctx, scope, code)
	if err == nil {
		return apperr.Conflict("ya existe un rol con el código " + code)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (s *Service) create(ctx context.Context, n NewRole, actor identity.Principal) (Role, error) {
	r, err := s.repo.Create(ctx, n, actor.ID)
	if errors.Is(err, ErrDuplicate) {
		return Role{}, apperr.Conflict("ya existe un rol con el código " + n.Code)
	}
	return r, err
}

// editable devuelve un rol personalizado del ámbito indicado: de sistema no se
// edita, y uno de otra organización o alcance se trata como inexistente.
func (s *Service) editable(ctx context.Context, id uuid.UUID, scope access.Scope, orgID uuid.UUID) (Role, error) {
	r, err := s.repo.Role(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Role{}, apperr.NotFound("rol no encontrado")
	}
	if err != nil {
		return Role{}, err
	}
	inScope := r.Scope == scope
	sameOrg := (orgID == uuid.Nil && r.OrganizationID == nil) || (r.OrganizationID != nil && *r.OrganizationID == orgID)
	if r.IsSystem {
		// Un rol de sistema no se edita; pero no se revela un rol de otro ámbito.
		if !inScope {
			return Role{}, apperr.NotFound("rol no encontrado")
		}
		return Role{}, apperr.Conflict("los roles de sistema no se pueden editar ni borrar")
	}
	if !inScope || !sameOrg {
		return Role{}, apperr.NotFound("rol no encontrado")
	}
	return r, nil
}

func (s *Service) update(ctx context.Context, actor identity.Principal, cur Role, orgID uuid.UUID, in UpdateInput) (Role, error) {
	p := Patch{Description: in.Description, OrgAssignable: in.OrgAssignable}
	if err := validate.OptionalMaxLen("description", in.Description, maxDescription); err != nil {
		return Role{}, err
	}
	if in.Name != nil {
		n, err := validate.Required("name", *in.Name, maxName)
		if err != nil {
			return Role{}, err
		}
		p.Name = &n
	}
	if in.Permissions != nil {
		perms, err := normalizePermissions(cur.Scope, *in.Permissions)
		if err != nil {
			return Role{}, err
		}
		if err := s.authorizePermissions(actor, orgID, cur.Scope, perms); err != nil {
			return Role{}, err
		}
		p.Permissions = &perms
	}
	r, err := s.repo.Update(ctx, cur.ID, p, actor.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		return Role{}, apperr.NotFound("rol no encontrado")
	case errors.Is(err, ErrSystemRole):
		return Role{}, apperr.Conflict("los roles de sistema no se pueden editar ni borrar")
	}
	return r, err
}

func (s *Service) delete(ctx context.Context, actor identity.Principal, id uuid.UUID) error {
	err := s.repo.Delete(ctx, id, actor.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		return apperr.NotFound("rol no encontrado")
	case errors.Is(err, ErrSystemRole):
		return apperr.Conflict("los roles de sistema no se pueden editar ni borrar")
	case errors.Is(err, ErrInUse):
		return apperr.Conflict("el rol está en uso: quita primero a quienes lo tienen")
	}
	return err
}
