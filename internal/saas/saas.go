// Package saas son los controles de la plataforma sobre los inquilinos y los
// usuarios: el panel del operador (solo superadmin). Suspender una organización,
// cambiarle el plan y marcar a alguien como domiciliario son palancas de
// negocio del SaaS, no de un inquilino.
//
// No tiene repositorio propio: orquesta los servicios de organizations, plans y users.
// (Se llama saas y no platform para no chocar con internal/platform, que es lo
// transversal; la ruta HTTP sí es /v1/platform.)
package saas

import (
	"context"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/organizations"
	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/users"
)

// Organizations es lo que necesita de organizations.
type Organizations interface {
	ListAll(ctx context.Context) ([]organizations.Organization, error)
	Get(ctx context.Context, actor identity.Principal, id uuid.UUID) (organizations.Organization, error)
	Suspend(ctx context.Context, actor identity.Principal, id uuid.UUID, reason string) (organizations.Organization, error)
	Reactivate(ctx context.Context, actor identity.Principal, id uuid.UUID, reason *string) (organizations.Organization, error)
}

// Plans es lo que necesita de plans.
type Plans interface {
	ChangePlan(ctx context.Context, actor identity.Principal, orgID uuid.UUID, tier string, reason *string) (plans.Subscription, error)
}

// Users es lo que necesita de users.
type Users interface {
	Counts(ctx context.Context) (total, delivery int64, err error)
	SetDelivery(ctx context.Context, actor identity.Principal, id uuid.UUID, isDelivery bool) (users.User, error)
}

// Overview es la foto del panel de plataforma.
type Overview struct {
	Organizations      []organizations.Organization `json:"organizations"`
	DeliveryUsersCount int64                        `json:"delivery_users_count"`
	TotalUsersCount    int64                        `json:"total_users_count"`
}

// Service orquesta las operaciones de plataforma.
type Service struct {
	orgs  Organizations
	plans Plans
	users Users
}

// NewService crea el servicio.
func NewService(orgs Organizations, plans Plans, users Users) *Service {
	return &Service{orgs: orgs, plans: plans, users: users}
}

// Overview devuelve todas las organizaciones y los contadores de usuarios.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	orgs, err := s.orgs.ListAll(ctx)
	if err != nil {
		return Overview{}, err
	}
	total, delivery, err := s.users.Counts(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{Organizations: orgs, DeliveryUsersCount: delivery, TotalUsersCount: total}, nil
}

// panelReason es el motivo que queda en la auditoría cuando el cambio viene del
// PATCH del panel, que no pide uno.
const panelReason = "cambio desde el panel de plataforma"

// UpdateOrganization suspende o reactiva un inquilino, o le cambia el plan.
//
// Es la ruta que usa el frontend actual (PATCH /platform/organizations/{id}); se
// conserva por compatibilidad y delega en las operaciones con motivo y auditoría
// (POST .../suspend, PUT .../plan). A diferencia de ellas es idempotente: pedir el
// estado o el plan que ya tiene no es un error.
func (s *Service) UpdateOrganization(ctx context.Context, actor identity.Principal, id uuid.UUID, isActive *bool, planTier *string) (organizations.Organization, error) {
	cur, err := s.orgs.Get(ctx, actor, id)
	if err != nil {
		return organizations.Organization{}, err
	}
	if planTier != nil && *planTier != cur.PlanTier {
		reason := panelReason
		if _, err := s.plans.ChangePlan(ctx, actor, id, *planTier, &reason); err != nil {
			return organizations.Organization{}, err
		}
	}
	if isActive != nil && *isActive != cur.IsActive {
		reason := panelReason
		if *isActive {
			_, err = s.orgs.Reactivate(ctx, actor, id, &reason)
		} else {
			_, err = s.orgs.Suspend(ctx, actor, id, reason)
		}
		if err != nil {
			return organizations.Organization{}, err
		}
	}
	return s.orgs.Get(ctx, actor, id)
}

// SetDelivery marca o desmarca a un usuario como domiciliario independiente.
func (s *Service) SetDelivery(ctx context.Context, actor identity.Principal, id uuid.UUID, isDelivery bool) (users.User, error) {
	return s.users.SetDelivery(ctx, actor, id, isDelivery)
}
