package organizations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/roles"
	"github.com/domicilia/domicilia-core/internal/store"
)

// NewInvitation son los datos de una invitación a emitir.
type NewInvitation struct {
	OrganizationID uuid.UUID
	Email          string // ya normalizado (minúsculas)
	Role           roles.Role
	TokenHash      []byte
	InvitedBy      uuid.UUID
	ExpiresAt      time.Time
}

// AcceptInput son los datos de quien acepta: su identidad sale del JWT, nunca del
// cuerpo de la petición.
type AcceptInput struct {
	TokenHash []byte
	UserID    uuid.UUID
	Email     string
	Now       time.Time
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	return &n.UUID
}

func pending(accepted, revoked pgtype.Timestamptz, expires, now time.Time) bool {
	return !accepted.Valid && !revoked.Valid && expires.After(now)
}

// issueInvitation emite (o reemplaza) la invitación y la audita, con las consultas
// dadas: quien llama decide la transacción.
func issueInvitation(ctx context.Context, q *store.Queries, n NewInvitation) (Invitation, error) {
	row, err := q.UpsertInvitation(ctx, store.UpsertInvitationParams{
		OrganizationID: n.OrganizationID, Email: n.Email, RoleID: n.Role.ID, TokenHash: n.TokenHash,
		InvitedBy: nullUUID(n.InvitedBy), ExpiresAt: n.ExpiresAt,
	})
	if err != nil {
		return Invitation{}, err
	}
	if err := (audit.Entry{
		ActorID: n.InvitedBy, Action: audit.InvitationCreated, OrganizationID: n.OrganizationID,
		RoleID: n.Role.ID, RoleCode: n.Role.Code, Detail: map[string]any{"email": n.Email},
	}).Insert(ctx, q); err != nil {
		return Invitation{}, err
	}
	return Invitation{
		ID: row.ID, OrganizationID: row.OrganizationID, Email: row.Email,
		Role: n.Role.Code, RoleID: n.Role.ID, RoleName: n.Role.Name,
		InvitedBy: uuidPtr(row.InvitedBy), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
	}, nil
}

func (r *pgRepository) IssueInvitation(ctx context.Context, n NewInvitation) (Invitation, error) {
	var inv Invitation
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		inv, err = issueInvitation(ctx, r.q.WithTx(tx), n)
		return err
	})
	if db.IsForeignKeyViolation(err) {
		return Invitation{}, ErrNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("organizations: emitir invitación: %w", err)
	}
	return inv, nil
}

func (r *pgRepository) Invitations(ctx context.Context, orgID uuid.UUID) ([]Invitation, error) {
	rows, err := r.q.ListPendingInvitations(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("organizations: listar invitaciones: %w", err)
	}
	out := make([]Invitation, 0, len(rows))
	for _, row := range rows {
		out = append(out, Invitation{
			ID: row.ID, OrganizationID: row.OrganizationID, Email: row.Email,
			Role: row.RoleCode, RoleID: row.RoleID, RoleName: row.RoleName,
			InvitedBy: uuidPtr(row.InvitedBy), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		})
	}
	return out, nil
}

func (r *pgRepository) PendingInvitationExists(ctx context.Context, orgID uuid.UUID, email string) (bool, error) {
	ok, err := r.q.PendingInvitationExists(ctx, store.PendingInvitationExistsParams{OrganizationID: orgID, Email: email})
	if err != nil {
		return false, fmt.Errorf("organizations: comprobar invitación: %w", err)
	}
	return ok, nil
}

func (r *pgRepository) RevokeInvitation(ctx context.Context, orgID, invID, actor uuid.UUID) (bool, error) {
	revoked := false
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		inv, err := q.GetInvitation(ctx, store.GetInvitationParams{ID: invID, OrganizationID: orgID})
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		n, err := q.RevokeInvitation(ctx, store.RevokeInvitationParams{ID: invID, OrganizationID: orgID})
		if err != nil || n == 0 {
			return err // 0: ya estaba aceptada o revocada
		}
		revoked = true
		return audit.Entry{
			ActorID: actor, Action: audit.InvitationRevoked, OrganizationID: orgID,
			RoleID: inv.RoleID, RoleCode: inv.RoleCode, Detail: map[string]any{"email": inv.Email},
		}.Insert(ctx, q)
	})
	if err != nil {
		return false, fmt.Errorf("organizations: revocar invitación: %w", err)
	}
	return revoked, nil
}

func (r *pgRepository) InvitationPreview(ctx context.Context, tokenHash []byte, now time.Time) (InvitationPreview, error) {
	row, err := r.q.GetInvitationByTokenHash(ctx, tokenHash)
	if db.IsNoRows(err) {
		return InvitationPreview{}, ErrInvitationNotFound
	}
	if err != nil {
		return InvitationPreview{}, fmt.Errorf("organizations: leer invitación: %w", err)
	}
	if !pending(row.AcceptedAt, row.RevokedAt, row.ExpiresAt, now) || Status(row.OrganizationStatus) != StatusActive {
		return InvitationPreview{}, ErrInvitationNotFound
	}
	return InvitationPreview{
		OrganizationName: row.OrganizationName, OrganizationSlug: row.OrganizationSlug,
		Email: row.Email, RoleName: row.RoleName, ExpiresAt: row.ExpiresAt,
	}, nil
}

// AcceptInvitation consume la invitación, crea el perfil de negocio si la persona
// aún no lo tiene, la suma a la organización y lo audita, todo en una transacción.
func (r *pgRepository) AcceptInvitation(ctx context.Context, in AcceptInput) (Member, error) {
	var member Member
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		inv, err := q.GetInvitationByTokenHashForUpdate(ctx, in.TokenHash)
		if db.IsNoRows(err) {
			return ErrInvitationNotFound
		}
		if err != nil {
			return err
		}
		if !pending(inv.AcceptedAt, inv.RevokedAt, inv.ExpiresAt, in.Now) {
			return ErrInvitationNotFound
		}
		org, err := q.GetOrganization(ctx, inv.OrganizationID)
		if err != nil {
			return err
		}
		if Status(org.Status) != StatusActive {
			return ErrOrgNotAcceptable
		}
		if !strings.EqualFold(inv.Email, in.Email) {
			return ErrEmailMismatch
		}

		user, err := q.GetUser(ctx, in.UserID)
		switch {
		case db.IsNoRows(err):
			if _, err := q.InsertUser(ctx, store.InsertUserParams{ID: in.UserID, Email: strings.ToLower(in.Email)}); err != nil {
				return err
			}
		case err != nil:
			return err
		case !user.IsActive:
			return ErrUserInactive
		}

		if err := q.LockOrganizationMembers(ctx, inv.OrganizationID.String()); err != nil {
			return err
		}
		current, err := q.GetMembership(ctx, store.GetMembershipParams{UserID: in.UserID, OrganizationID: inv.OrganizationID})
		switch {
		case db.IsNoRows(err):
			if err := q.InsertMembership(ctx, store.InsertMembershipParams{
				UserID: in.UserID, OrganizationID: inv.OrganizationID, RoleID: inv.RoleID,
			}); err != nil {
				return err
			}
		case err != nil:
			return err
		case current.RoleID != inv.RoleID:
			return ErrAlreadyMember // ya es miembro con otro rol: no se le cambia el rol por invitación
		}

		n, err := q.AcceptInvitation(ctx, store.AcceptInvitationParams{ID: inv.ID, AcceptedBy: nullUUID(in.UserID)})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvitationNotFound
		}
		m, err := q.GetMembership(ctx, store.GetMembershipParams{UserID: in.UserID, OrganizationID: inv.OrganizationID})
		if err != nil {
			return err
		}
		member = Member{UserID: in.UserID, OrganizationID: inv.OrganizationID, Role: m.RoleCode, RoleID: m.RoleID, RoleName: m.RoleName}
		return audit.Entry{
			ActorID: in.UserID, Action: audit.InvitationAccepted, TargetUserID: in.UserID, OrganizationID: inv.OrganizationID,
			RoleID: m.RoleID, RoleCode: m.RoleCode,
		}.Insert(ctx, q)
	})
	if db.IsUniqueViolation(err) {
		return Member{}, ErrEmailTaken // el correo ya pertenece a otra cuenta
	}
	if err != nil {
		return Member{}, wrapAccept(err)
	}
	return member, nil
}

// wrapAccept deja pasar los errores de negocio sin envolver y envuelve el resto.
func wrapAccept(err error) error {
	for _, known := range []error{ErrInvitationNotFound, ErrEmailMismatch, ErrOrgNotAcceptable, ErrUserInactive, ErrAlreadyMember} {
		if errors.Is(err, known) {
			return err
		}
	}
	return fmt.Errorf("organizations: aceptar invitación: %w", err)
}
