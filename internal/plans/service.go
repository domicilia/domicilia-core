package plans

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

const maxReason = 500

// Service reúne las reglas de los planes.
type Service struct{ repo Repository }

// NewService crea el servicio.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// CatalogView es el catálogo público de planes y funciones.
type CatalogView struct {
	Plans    []Plan       `json:"plans"`
	Features []FeatureDef `json:"features"`
}

// Catalog devuelve los planes y las funciones (para la pantalla de precios).
func (s *Service) Catalog() CatalogView { return CatalogView{Plans: Catalog, Features: Features} }

// EntitlementsUsage es lo consumido frente a los límites del plan.
type EntitlementsUsage struct {
	Members            int64 `json:"members"`
	PendingInvitations int64 `json:"pending_invitations"`
	// MaxMembers es -1 (Unlimited) si el plan no tiene tope.
	MaxMembers int `json:"max_members"`
}

// Entitlements es lo que una organización puede usar hoy.
type Entitlements struct {
	PlanTier Tier `json:"plan_tier"`
	Plan     Plan `json:"plan"`
	// Features son las funciones efectivas: las del plan más/menos las excepciones.
	Features  []Feature         `json:"features"`
	Overrides []Override        `json:"overrides"`
	Usage     EntitlementsUsage `json:"usage"`
}

func (s *Service) state(ctx context.Context, orgID uuid.UUID) (State, error) {
	st, err := s.repo.State(ctx, orgID)
	if errors.Is(err, ErrNotFound) {
		return State{}, apperr.NotFound("organización no encontrada")
	}
	return st, err
}

// visible aplica el aislamiento de lectura: hay que pertenecer a la organización;
// una archivada "no existe" y una suspendida no se abre para sus miembros. El
// operador entra siempre.
func visible(actor identity.Principal, orgID uuid.UUID, st State) error {
	if !actor.IsMember(orgID) {
		return apperr.Forbidden("acceso denegado")
	}
	if actor.Can(access.PlatformOrganizationsAccessAll) {
		return nil
	}
	switch st.Status {
	case "archived":
		return apperr.NotFound("organización no encontrada")
	case "suspended":
		return apperr.Forbidden("la organización está suspendida")
	}
	return nil
}

// Entitlements devuelve el plan, las funciones efectivas y el uso de la organización.
// Basta con pertenecer a ella: la interfaz lo necesita para saber qué mostrar.
func (s *Service) Entitlements(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (Entitlements, error) {
	if !actor.IsMember(orgID) { // antes de tocar la base: un extraño no averigua si existe
		return Entitlements{}, apperr.Forbidden("acceso denegado")
	}
	st, err := s.state(ctx, orgID)
	if err != nil {
		return Entitlements{}, err
	}
	if err := visible(actor, orgID, st); err != nil {
		return Entitlements{}, err
	}
	usage, err := s.repo.Usage(ctx, orgID)
	if err != nil {
		return Entitlements{}, err
	}
	plan, _ := Lookup(string(st.Tier))
	return Entitlements{
		PlanTier:  st.Tier,
		Plan:      plan,
		Features:  Effective(st.Tier, st.Overrides),
		Overrides: st.Overrides,
		Usage: EntitlementsUsage{
			Members: usage.Members, PendingInvitations: usage.PendingInvitations, MaxMembers: plan.Limits.MaxMembers,
		},
	}, nil
}

// Require es la puerta de las funciones premium: la organización debe estar activa y
// su plan (o una excepción) debe incluir la función. Cada endpoint premium la llama
// en su servicio; no basta con ocultar el botón en el frontend.
func (s *Service) Require(ctx context.Context, orgID uuid.UUID, f Feature) error {
	def, ok := LookupFeature(string(f))
	if !ok {
		return fmt.Errorf("plans: función desconocida %q", f) // error de programación, no del cliente
	}
	st, err := s.state(ctx, orgID)
	if err != nil {
		return err
	}
	if st.Status != "active" {
		return apperr.Forbidden("la organización no está activa")
	}
	if !Has(Effective(st.Tier, st.Overrides), f) {
		return apperr.PaymentRequired("tu plan no incluye: " + def.Label)
	}
	return nil
}

// CheckMemberCapacity comprueba que quepan `adding` miembros más (contando las
// invitaciones pendientes) dentro del límite del plan.
func (s *Service) CheckMemberCapacity(ctx context.Context, orgID uuid.UUID, adding int64) error {
	st, err := s.state(ctx, orgID)
	if err != nil {
		return err
	}
	plan, _ := Lookup(string(st.Tier))
	if plan.Limits.MaxMembers == Unlimited {
		return nil
	}
	usage, err := s.repo.Usage(ctx, orgID)
	if err != nil {
		return err
	}
	if usage.Members+usage.PendingInvitations+adding > int64(plan.Limits.MaxMembers) {
		return apperr.PaymentRequired(fmt.Sprintf("el plan %s admite hasta %d miembros", plan.Name, plan.Limits.MaxMembers))
	}
	return nil
}

// Subscriptions devuelve el historial de planes (el primero es el vigente).
func (s *Service) Subscriptions(ctx context.Context, actor identity.Principal, orgID uuid.UUID) ([]Subscription, error) {
	if !actor.CanInOrg(orgID, access.OrgBillingRead) {
		return nil, apperr.Forbidden("no tienes permiso para esta acción en la organización")
	}
	if _, err := s.state(ctx, orgID); err != nil {
		return nil, err
	}
	return s.repo.Subscriptions(ctx, orgID)
}

// ChangePlan cambia el plan. Quien llama debe tener PlatformOrganizationsManage (lo
// exige la ruta). Bajar a un plan cuyo límite ya se excede se rechaza: primero hay
// que liberar miembros o invitaciones.
func (s *Service) ChangePlan(ctx context.Context, actor identity.Principal, orgID uuid.UUID, tier string, reason *string) (Subscription, error) {
	plan, ok := Lookup(tier)
	if !ok {
		return Subscription{}, apperr.Invalid("plan_tier desconocido: " + tier)
	}
	if err := validate.OptionalMaxLen("reason", reason, maxReason); err != nil {
		return Subscription{}, err
	}
	if plan.Limits.MaxMembers != Unlimited {
		usage, err := s.repo.Usage(ctx, orgID)
		if err != nil {
			return Subscription{}, err
		}
		if used := usage.Members + usage.PendingInvitations; used > int64(plan.Limits.MaxMembers) {
			return Subscription{}, apperr.Conflict(fmt.Sprintf(
				"la organización tiene %d miembros e invitaciones y el plan %s admite %d", used, plan.Name, plan.Limits.MaxMembers))
		}
	}
	sub, err := s.repo.ChangePlan(ctx, orgID, plan.Tier, actor.ID, reason)
	switch {
	case errors.Is(err, ErrNotFound):
		return Subscription{}, apperr.NotFound("organización no encontrada")
	case errors.Is(err, ErrArchived):
		return Subscription{}, apperr.Conflict("la organización está archivada")
	case errors.Is(err, ErrSamePlan):
		return Subscription{}, apperr.Conflict("la organización ya tiene ese plan")
	}
	return sub, err
}

// SetOverride enciende o apaga una función para una organización.
func (s *Service) SetOverride(ctx context.Context, actor identity.Principal, orgID uuid.UUID, feature string, enabled *bool, reason *string) (Override, error) {
	def, ok := LookupFeature(feature)
	if !ok {
		return Override{}, apperr.Invalid("función desconocida: " + feature)
	}
	if enabled == nil {
		return Override{}, apperr.Invalid("enabled es obligatorio")
	}
	if err := validate.OptionalMaxLen("reason", reason, maxReason); err != nil {
		return Override{}, err
	}
	o := Override{Feature: def.Key, Enabled: *enabled, Reason: reason}
	if err := s.repo.SetOverride(ctx, orgID, o, actor.ID); errors.Is(err, ErrNotFound) {
		return Override{}, apperr.NotFound("organización no encontrada")
	} else if err != nil {
		return Override{}, err
	}
	return o, nil
}

// ClearOverride quita la excepción: la función vuelve a depender solo del plan.
func (s *Service) ClearOverride(ctx context.Context, actor identity.Principal, orgID uuid.UUID, feature string) error {
	def, ok := LookupFeature(feature)
	if !ok {
		return apperr.Invalid("función desconocida: " + feature)
	}
	if _, err := s.state(ctx, orgID); err != nil {
		return err
	}
	cleared, err := s.repo.ClearOverride(ctx, orgID, def.Key, actor.ID)
	if err != nil {
		return err
	}
	if !cleared {
		return apperr.NotFound("la organización no tiene una excepción para esa función")
	}
	return nil
}
