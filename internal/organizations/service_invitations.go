package organizations

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/mail"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

// acceptURL arma el enlace que recibe quien se invita. El frontend lo atiende en
// /auth/accept-invite.
func (s *Service) acceptURL(token string) string {
	return s.appURL + "/auth/accept-invite?token=" + url.QueryEscape(token)
}

func (s *Service) issued(inv Invitation, token string) IssuedInvitation {
	return IssuedInvitation{Invitation: inv, AcceptURL: s.acceptURL(token)}
}

// notifyInvitation intenta mandar el enlace por correo. Un fallo no revierte la
// invitación: quien invita ya recibió el enlace en la respuesta y puede compartirlo.
func (s *Service) notifyInvitation(ctx context.Context, orgName string, inv IssuedInvitation) {
	err := s.mailer.Send(ctx, mail.Message{
		To:      inv.Email,
		Subject: "Te invitaron a " + orgName + " en Domicilia",
		Text: "Te invitaron a unirte a " + orgName + " como " + inv.RoleName + ".\n\n" +
			"Acepta la invitación aquí: " + inv.AcceptURL + "\n\n" +
			"El enlace vence el " + inv.ExpiresAt.Format("2006-01-02") + ".",
	})
	if err != nil {
		s.log.WarnContext(ctx, "no se pudo enviar el correo de la invitación", "organization", inv.OrganizationID, "error", err)
	}
}

// Invite invita a un correo a unirse a la organización con un rol. Reinvitar a un
// correo con una invitación pendiente la reemplaza (nuevo enlace, nueva vigencia).
// Devuelve el enlace: es la única vez que sale del servidor hacia quien invita.
//
// Mismas reglas de permisos que AddMember: solo platform.roles.manage nombra
// administradores.
func (s *Service) Invite(ctx context.Context, actor identity.Principal, orgID uuid.UUID, rawEmail, role string) (IssuedInvitation, error) {
	org, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, true)
	if err != nil {
		return IssuedInvitation{}, err
	}
	email, err := validate.Email("email", rawEmail)
	if err != nil {
		return IssuedInvitation{}, err
	}
	return s.issue(ctx, actor, org, email, role)
}

// issue emite la invitación ya validada. roleRef se resuelve otra vez: cada emisión
// se autoriza con quien la pide, incluido el reenvío.
func (s *Service) issue(ctx context.Context, actor identity.Principal, org Organization, email, roleRef string) (IssuedInvitation, error) {
	role, err := s.roles.ResolveOrgRole(ctx, actor, org.ID, roleRef)
	if err != nil {
		return IssuedInvitation{}, err
	}
	if member, err := s.repo.IsMemberByEmail(ctx, org.ID, email); err != nil {
		return IssuedInvitation{}, err
	} else if member {
		return IssuedInvitation{}, apperr.Conflict("esa persona ya es miembro de la organización")
	}
	// Reemplazar una invitación vigente no ocupa un lugar nuevo.
	if replacing, err := s.repo.PendingInvitationExists(ctx, org.ID, email); err != nil {
		return IssuedInvitation{}, err
	} else if !replacing {
		if err := s.capacity.CheckMemberCapacity(ctx, org.ID, 1); err != nil {
			return IssuedInvitation{}, err
		}
	}

	token, hash, err := newToken()
	if err != nil {
		return IssuedInvitation{}, err
	}
	inv, err := s.repo.IssueInvitation(ctx, NewInvitation{
		OrganizationID: org.ID, Email: email, Role: role, TokenHash: hash,
		InvitedBy: actor.ID, ExpiresAt: s.now().Add(InvitationTTL),
	})
	if errors.Is(err, ErrNotFound) {
		return IssuedInvitation{}, apperr.NotFound("organización no encontrada")
	}
	if err != nil {
		return IssuedInvitation{}, err
	}
	issued := s.issued(inv, token)
	s.notifyInvitation(ctx, org.Name, issued)
	return issued, nil
}

// Invitations lista las invitaciones pendientes (sin el enlace: el token no se
// guarda, así que no se puede volver a mostrar; para obtener uno nuevo se reenvía).
func (s *Service) Invitations(ctx context.Context, actor identity.Principal, orgID uuid.UUID) ([]Invitation, error) {
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, false); err != nil {
		return nil, err
	}
	return s.repo.Invitations(ctx, orgID)
}

// Resend reemplaza una invitación pendiente por otra igual con un enlace nuevo.
func (s *Service) Resend(ctx context.Context, actor identity.Principal, orgID, invID uuid.UUID) (IssuedInvitation, error) {
	org, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, true)
	if err != nil {
		return IssuedInvitation{}, err
	}
	pending, err := s.repo.Invitations(ctx, orgID)
	if err != nil {
		return IssuedInvitation{}, err
	}
	for _, inv := range pending {
		if inv.ID == invID {
			return s.issue(ctx, actor, org, inv.Email, inv.RoleID.String())
		}
	}
	return IssuedInvitation{}, apperr.NotFound("invitación no encontrada")
}

// RevokeInvitation cancela una invitación pendiente: su enlace deja de servir.
func (s *Service) RevokeInvitation(ctx context.Context, actor identity.Principal, orgID, invID uuid.UUID) error {
	if _, err := s.openOrg(ctx, actor, orgID, access.OrgMembersManage, true); err != nil {
		return err
	}
	revoked, err := s.repo.RevokeInvitation(ctx, orgID, invID, actor.ID)
	if err != nil {
		return err
	}
	if !revoked {
		return apperr.NotFound("invitación no encontrada")
	}
	return nil
}

func invitationNotFound() *apperr.Error {
	return apperr.NotFound("la invitación no existe o ya no es válida")
}

// Preview muestra a quien abre el enlace a qué organización y con qué rol lo
// invitaron. Es público: el token es la credencial. Caducada, revocada, aceptada,
// de una organización no activa o inexistente responden igual.
func (s *Service) Preview(ctx context.Context, token string) (InvitationPreview, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return InvitationPreview{}, apperr.Invalid("token es obligatorio")
	}
	p, err := s.repo.InvitationPreview(ctx, hashToken(token), s.now())
	if errors.Is(err, ErrInvitationNotFound) {
		return InvitationPreview{}, invitationNotFound()
	}
	return p, err
}

// Accept acepta una invitación en nombre de quien tiene sesión. Su identidad (id y
// correo) sale del JWT: el correo debe coincidir con el de la invitación, así un
// enlace reenviado por error no sirve a otra persona.
func (s *Service) Accept(ctx context.Context, userID uuid.UUID, email, token string) (Member, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Member{}, apperr.Invalid("token es obligatorio")
	}
	if strings.TrimSpace(email) == "" {
		return Member{}, apperr.Unauthorized("la sesión no trae un correo")
	}
	m, err := s.repo.AcceptInvitation(ctx, AcceptInput{TokenHash: hashToken(token), UserID: userID, Email: email, Now: s.now()})
	switch {
	case errors.Is(err, ErrInvitationNotFound), errors.Is(err, ErrOrgNotAcceptable):
		// Una organización no activa responde igual que un token inválido: no se revela
		// que existe ni en qué estado está.
		return Member{}, invitationNotFound()
	case errors.Is(err, ErrEmailMismatch):
		return Member{}, apperr.Forbidden("esta invitación es para otra cuenta")
	case errors.Is(err, ErrUserInactive):
		return Member{}, apperr.Forbidden("tu cuenta está desactivada")
	case errors.Is(err, ErrAlreadyMember):
		return Member{}, apperr.Conflict("ya eres miembro de esta organización con otro rol")
	case errors.Is(err, ErrEmailTaken):
		return Member{}, apperr.Conflict("ese correo ya pertenece a otra cuenta")
	}
	return m, err
}

// undoIdentity borra una cuenta recién creada en GoTrue. Usa un contexto propio:
// si la petición se canceló, la limpieza debe correr igual.
func (s *Service) undoIdentity(ctx context.Context, id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensateTimeout)
	defer cancel()
	if err := s.idp.DeleteUser(ctx, id); err != nil {
		s.log.Error("no se pudo deshacer el alta en auth-domicilia; queda una cuenta huérfana",
			"user_id", id, "error", err)
	}
}
