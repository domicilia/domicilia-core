// Package audit registra quién cambió qué y cuándo: roles, miembros, estado, plan y
// ajustes de las organizaciones, invitaciones. Cada repositorio escribe su entrada
// DENTRO de la misma transacción que el cambio: o quedan ambos o ninguno, así la
// auditoría no puede quedarse atrás.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/store"
)

// Action es el tipo de cambio auditado.
type Action string

// Acciones auditadas.
const (
	RoleCreated         Action = "role.created"
	RoleUpdated         Action = "role.updated"
	RoleDeleted         Action = "role.deleted"
	PlatformRoleGranted Action = "platform_role.granted"
	PlatformRoleRevoked Action = "platform_role.revoked"
	MemberAdded         Action = "member.added"
	MemberInvited       Action = "member.invited"
	MemberRoleChanged   Action = "member.role_changed"
	MemberRemoved       Action = "member.removed"
	UserActivated       Action = "user.activated"
	UserDeactivated     Action = "user.deactivated"

	OrganizationCreated         Action = "organization.created"
	OrganizationUpdated         Action = "organization.updated"
	OrganizationSettingsChanged Action = "organization.settings_changed"
	OrganizationSuspended       Action = "organization.suspended"
	OrganizationReactivated     Action = "organization.reactivated"
	OrganizationArchived        Action = "organization.archived"
	OrganizationRestored        Action = "organization.restored"
	OrganizationPlanChanged     Action = "organization.plan_changed"
	FeatureOverrideSet          Action = "organization.feature_override_set"
	FeatureOverrideCleared      Action = "organization.feature_override_cleared"
	InvitationCreated           Action = "invitation.created"
	InvitationRevoked           Action = "invitation.revoked"
	InvitationAccepted          Action = "invitation.accepted"
	InboxConnected              Action = "inbox.connected"
	InboxRenamed                Action = "inbox.renamed"
	InboxCredentialsRotated     Action = "inbox.credentials_rotated" //nolint:gosec // es el nombre de una acción de auditoría, no una credencial
	InboxArchived               Action = "inbox.archived"

	// Comisiones y tarifas (internal/pricing, docs/pagos.md).
	PricingSettingsChanged     Action = "pricing.settings_changed"
	GatewayFeePlanChanged      Action = "pricing.gateway_plan_changed"
	OrganizationPricingChanged Action = "organization.pricing_changed"
)

// Entry es un cambio a registrar. Los ids en uuid.Nil se guardan como NULL.
type Entry struct {
	ActorID        uuid.UUID
	Action         Action
	TargetUserID   uuid.UUID
	OrganizationID uuid.UUID
	RoleID         uuid.UUID
	// RoleCode es el código del rol al momento del cambio: el rol puede borrarse después.
	RoleCode string
	Detail   map[string]any
}

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

// Insert escribe la entrada con las consultas dadas (normalmente ligadas a la
// transacción del cambio).
func (e Entry) Insert(ctx context.Context, q *store.Queries) error {
	var detail []byte
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("audit: codificar detalle: %w", err)
		}
		detail = b
	}
	var code *string
	if e.RoleCode != "" {
		code = &e.RoleCode
	}
	if err := q.InsertAudit(ctx, store.InsertAuditParams{
		ActorID:        nullUUID(e.ActorID),
		Action:         string(e.Action),
		TargetUserID:   nullUUID(e.TargetUserID),
		OrganizationID: nullUUID(e.OrganizationID),
		RoleID:         nullUUID(e.RoleID),
		RoleCode:       code,
		Detail:         detail,
	}); err != nil {
		return fmt.Errorf("audit: registrar %s: %w", e.Action, err)
	}
	return nil
}

// Record es una entrada tal como se lista.
type Record struct {
	ID             uuid.UUID      `json:"id"`
	ActorID        *uuid.UUID     `json:"actor_id"`
	Action         string         `json:"action"`
	TargetUserID   *uuid.UUID     `json:"target_user_id"`
	OrganizationID *uuid.UUID     `json:"organization_id"`
	RoleID         *uuid.UUID     `json:"role_id"`
	RoleCode       *string        `json:"role_code"`
	Detail         map[string]any `json:"detail"`
	CreatedAt      time.Time      `json:"created_at"`
}

// Filter acota el listado. Los campos nil no filtran.
type Filter struct {
	OrganizationID *uuid.UUID
	TargetUserID   *uuid.UUID
	Action         *string
	Limit          int
	Offset         int
}

func optUUID(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	return &n.UUID
}

func toNull(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

// List devuelve una página de la auditoría (la más reciente primero) y el total.
func List(ctx context.Context, q *store.Queries, f Filter) ([]Record, int64, error) {
	rows, err := q.ListAudit(ctx, store.ListAuditParams{
		OrganizationID: toNull(f.OrganizationID),
		TargetUserID:   toNull(f.TargetUserID),
		Action:         f.Action,
		PageSize:       int32(f.Limit),  //nolint:gosec // acotado por el paginador
		PageOffset:     int32(f.Offset), //nolint:gosec // acotado por el paginador
	})
	if err != nil {
		return nil, 0, fmt.Errorf("audit: listar: %w", err)
	}
	total, err := q.CountAudit(ctx, store.CountAuditParams{
		OrganizationID: toNull(f.OrganizationID),
		TargetUserID:   toNull(f.TargetUserID),
		Action:         f.Action,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("audit: contar: %w", err)
	}
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		rec := Record{
			ID:             r.ID,
			ActorID:        optUUID(r.ActorID),
			Action:         r.Action,
			TargetUserID:   optUUID(r.TargetUserID),
			OrganizationID: optUUID(r.OrganizationID),
			RoleID:         optUUID(r.RoleID),
			RoleCode:       r.RoleCode,
			CreatedAt:      r.CreatedAt,
		}
		if len(r.Detail) > 0 {
			if err := json.Unmarshal(r.Detail, &rec.Detail); err != nil {
				return nil, 0, fmt.Errorf("audit: detalle de %s: %w", r.ID, err)
			}
		}
		out = append(out, rec)
	}
	return out, total, nil
}
