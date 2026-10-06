package organizations

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

// AddMember suma a un usuario que ya tiene perfil. role es un código o un id de
// rol; ver roles.ResolveOrgRole para quién puede asignar qué.
func (s *Service) AddMember(ctx context.Context, actor identity.Principal, orgID, userID uuid.UUID, role string) (Member, error) {
	if err := s.requirePerm(actor, orgID, access.OrgMembersManage); err != nil {
		return Member{}, err
	}
	r, err := s.roles.ResolveOrgRole(ctx, actor, orgID, role)
	if err != nil {
		return Member{}, err
	}
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, true); err != nil {
		return Member{}, err
	}
	if ok, err := s.repo.UserExists(ctx, userID); err != nil {
		return Member{}, err
	} else if !ok {
		return Member{}, apperr.NotFound("usuario no encontrado")
	}
	if err := s.capacity.CheckMemberCapacity(ctx, orgID, 1); err != nil {
		return Member{}, err
	}

	m, err := s.repo.AddMember(ctx, userID, orgID, r, actor.ID)
	switch {
	case errors.Is(err, ErrAlreadyMember):
		return Member{}, apperr.Conflict("el usuario ya es miembro")
	case errors.Is(err, ErrNotFound):
		return Member{}, apperr.NotFound("usuario u organización no encontrados")
	}
	return m, err
}

// ChangeMemberRole cambia el rol de un miembro. Mismas reglas de asignación que
// AddMember. No puede dejar a la organización sin administrador.
func (s *Service) ChangeMemberRole(ctx context.Context, actor identity.Principal, orgID, userID uuid.UUID, role string) (Member, error) {
	if err := s.requirePerm(actor, orgID, access.OrgMembersManage); err != nil {
		return Member{}, err
	}
	r, err := s.roles.ResolveOrgRole(ctx, actor, orgID, role)
	if err != nil {
		return Member{}, err
	}
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, true); err != nil {
		return Member{}, err
	}
	m, err := s.repo.ChangeMemberRole(ctx, userID, orgID, r, actor.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		return Member{}, apperr.NotFound("miembro no encontrado")
	case errors.Is(err, ErrLastManager):
		return Member{}, apperr.Conflict("la organización debe conservar al menos un administrador")
	}
	return m, err
}

// InviteMember crea una identidad nueva en GoTrue y la suma a la organización en
// un solo paso, con una contraseña temporal.
//
// OBSOLETO: se conserva solo mientras no haya correo. Las invitaciones con token
// (Invite) no dejan una contraseña viajando por la respuesta. Mismas reglas de
// permisos que AddMember.
//
// Si guardar el perfil falla después de crear la cuenta, la cuenta se borra: sin
// eso el correo quedaría ocupado en GoTrue y no se podría reintentar.
func (s *Service) InviteMember(ctx context.Context, actor identity.Principal, orgID uuid.UUID, email string, fullName *string, role string) (InvitedMember, error) {
	// Primero el permiso: quien no puede administrar no recibe información sobre la
	// validez de lo que mandó.
	if err := s.requirePerm(actor, orgID, access.OrgMembersManage); err != nil {
		return InvitedMember{}, err
	}
	email, err := validate.Email("email", email)
	if err != nil {
		return InvitedMember{}, err
	}
	if err := validate.OptionalMaxLen("full_name", fullName, maxFullName); err != nil {
		return InvitedMember{}, err
	}
	r, err := s.roles.ResolveOrgRole(ctx, actor, orgID, role)
	if err != nil {
		return InvitedMember{}, err
	}
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, true); err != nil {
		return InvitedMember{}, err
	}
	if taken, err := s.repo.EmailRegistered(ctx, email); err != nil {
		return InvitedMember{}, err
	} else if taken {
		return InvitedMember{}, apperr.Conflict("el correo ya está registrado")
	}
	if err := s.capacity.CheckMemberCapacity(ctx, orgID, 1); err != nil {
		return InvitedMember{}, err
	}

	password, err := gotrue.NewTemporaryPassword()
	if err != nil {
		return InvitedMember{}, err
	}
	created, err := s.idp.CreateUser(ctx, email, password)
	if err != nil {
		return InvitedMember{}, apperr.Upstream("no se pudo crear la cuenta en auth-domicilia", err)
	}

	m, err := s.repo.CreateStaff(ctx, created.ID, email, fullName, orgID, r, actor.ID)
	if err != nil {
		s.undoIdentity(ctx, created.ID)
		if errors.Is(err, ErrEmailTaken) {
			return InvitedMember{}, apperr.Conflict("el correo ya está registrado")
		}
		return InvitedMember{}, err
	}
	return InvitedMember{Member: m, TemporaryPassword: password}, nil
}

// RemoveMember saca a un miembro de la organización. No puede dejarla sin
// administrador.
func (s *Service) RemoveMember(ctx context.Context, actor identity.Principal, orgID, userID uuid.UUID) error {
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, true); err != nil {
		return err
	}
	removed, err := s.repo.RemoveMember(ctx, userID, orgID, actor.ID)
	if errors.Is(err, ErrLastManager) {
		return apperr.Conflict("la organización debe conservar al menos un administrador")
	}
	if err != nil {
		return err
	}
	if !removed {
		return apperr.NotFound("miembro no encontrado")
	}
	return nil
}

// Members lista los miembros de una organización.
func (s *Service) Members(ctx context.Context, actor identity.Principal, orgID uuid.UUID) ([]Member, error) {
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgMembersRead, false); err != nil {
		return nil, err
	}
	return s.repo.Members(ctx, orgID)
}
