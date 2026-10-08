package pricing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// Settings es la configuración general (pricing_settings).
type Settings struct {
	PlatformFeeBps      int32     `json:"platform_fee_bps"`
	PromoPlatformFeeBps int32     `json:"promo_platform_fee_bps"`
	CourierFeeBps       int32     `json:"courier_fee_bps"`
	DeliveryFeeCents    int32     `json:"delivery_fee_cents"`
	GatewayPlanCode     string    `json:"gateway_plan_code"`
	SplitEnabled        bool      `json:"split_enabled"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// PlanRecord es un plan con sus datos de edición.
type PlanRecord struct {
	GatewayPlan
	Notes     *string   `json:"notes"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Override es lo que el superadmin le sobrescribe a una organización. nil = hereda.
type Override struct {
	PlatformFeeBps      *int32     `json:"platform_fee_bps"`
	PromoPlatformFeeBps *int32     `json:"promo_platform_fee_bps"`
	CourierFeeBps       *int32     `json:"courier_fee_bps"`
	DeliveryFeeCents    *int32     `json:"delivery_fee_cents"`
	GatewayPlanCode     *string    `json:"gateway_plan_code"`
	EpaycoMerchantID    *string    `json:"epayco_merchant_id"`
	UpdatedAt           *time.Time `json:"updated_at"`
}

// OrganizationPricing es la vista de UNA organización: lo propio y lo efectivo.
type OrganizationPricing struct {
	OrganizationID uuid.UUID `json:"organization_id"`
	Override       Override  `json:"override"`
	Effective      Rates     `json:"effective"`
}

// OrganizationRow es una fila de la vista general.
type OrganizationRow struct {
	OrganizationID uuid.UUID `json:"organization_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Status         string    `json:"status"`
	Override       Override  `json:"override"`
	// HasOverride dice si la organización tiene ALGO propio (para resaltarla en la lista).
	HasOverride bool `json:"has_override"`
}

// Overview es lo que muestra la pantalla general del superadmin.
type Overview struct {
	Settings      Settings          `json:"settings"`
	Plans         []PlanRecord      `json:"plans"`
	Organizations []OrganizationRow `json:"organizations"`
	Methods       []MethodInfo      `json:"methods"`
}

// MethodInfo describe un medio de pago para la interfaz.
type MethodInfo struct {
	Code  Method `json:"code"`
	Label string `json:"label"`
}

// Service resuelve las tarifas efectivas y administra la configuración.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	gate *tenant.Gate
}

// NewService crea el servicio.
func NewService(pool *pgxpool.Pool, gate *tenant.Gate) *Service {
	return &Service{pool: pool, q: store.New(pool), gate: gate}
}

func planFromRow(p store.GatewayFeePlan) GatewayPlan {
	return GatewayPlan{
		Code: p.Code, Gateway: p.Gateway, Name: p.Name,
		CardPercentBps: p.CardPercentBps, CardFixedCents: p.CardFixedCents,
		InternationalExtraBps: p.InternationalExtraBps,
		WalletPercentBps:      p.WalletPercentBps, WalletFixedCents: p.WalletFixedCents,
		PSEPercentBps: p.PsePercentBps, PSEFixedCents: p.PseFixedCents,
		PSESmallThresholdCents: p.PseSmallThresholdCents, PSESmallFixedCents: p.PseSmallFixedCents,
		VATBps: p.VatBps,
	}
}

func settingsFromRow(s store.PricingSetting) Settings {
	return Settings{
		PlatformFeeBps: s.PlatformFeeBps, PromoPlatformFeeBps: s.PromoPlatformFeeBps,
		CourierFeeBps: s.CourierFeeBps, DeliveryFeeCents: s.DeliveryFeeCents,
		GatewayPlanCode: s.GatewayPlanCode, SplitEnabled: s.SplitEnabled, UpdatedAt: s.UpdatedAt,
	}
}

func overrideFromRow(o store.OrganizationPricing) Override {
	t := o.UpdatedAt
	return Override{
		PlatformFeeBps: o.PlatformFeeBps, PromoPlatformFeeBps: o.PromoPlatformFeeBps,
		CourierFeeBps: o.CourierFeeBps, DeliveryFeeCents: o.DeliveryFeeCents,
		GatewayPlanCode: o.GatewayPlanCode, EpaycoMerchantID: o.EpaycoMerchantID, UpdatedAt: &t,
	}
}

// RatesFor devuelve las tarifas efectivas de una organización: la general con lo propio encima.
// Sin control de acceso: la usan catálogo, pedidos y pagos, que ya controlaron el suyo.
func (s *Service) RatesFor(ctx context.Context, orgID uuid.UUID) (Rates, error) {
	return ratesFor(ctx, s.q, orgID)
}

func ratesFor(ctx context.Context, q *store.Queries, orgID uuid.UUID) (Rates, error) {
	st, err := q.GetPricingSettings(ctx)
	if err != nil {
		return Rates{}, fmt.Errorf("pricing: leer configuración: %w", err)
	}
	ov := Override{}
	if row, err := q.GetOrganizationPricing(ctx, orgID); err == nil {
		ov = overrideFromRow(row)
	} else if !db.IsNoRows(err) {
		return Rates{}, fmt.Errorf("pricing: leer configuración de la organización: %w", err)
	}
	return resolve(ctx, q, settingsFromRow(st), ov)
}

// resolve combina general + propio. Exportable a Simulate para simular "qué pasaría si".
func resolve(ctx context.Context, q *store.Queries, st Settings, ov Override) (Rates, error) {
	r := Rates{
		PlatformFeeBps: pick(ov.PlatformFeeBps, st.PlatformFeeBps), PromoPlatformFeeBps: pick(ov.PromoPlatformFeeBps, st.PromoPlatformFeeBps),
		CourierFeeBps: pick(ov.CourierFeeBps, st.CourierFeeBps), DeliveryFeeCents: pick(ov.DeliveryFeeCents, st.DeliveryFeeCents),
		SplitEnabled: st.SplitEnabled,
	}
	if ov.EpaycoMerchantID != nil {
		r.EpaycoMerchantID = *ov.EpaycoMerchantID
	}
	code := st.GatewayPlanCode
	if ov.GatewayPlanCode != nil {
		code = *ov.GatewayPlanCode
	}
	plan, err := q.GetGatewayFeePlan(ctx, code)
	if err != nil {
		return Rates{}, fmt.Errorf("pricing: leer plan %q: %w", code, err)
	}
	r.Plan = planFromRow(plan)
	return r, nil
}

func pick(v *int32, def int32) int32 {
	if v != nil {
		return *v
	}
	return def
}

func methodsInfo() []MethodInfo {
	out := make([]MethodInfo, len(Methods))
	for i, m := range Methods {
		out[i] = MethodInfo{Code: m, Label: m.Label()}
	}
	return out
}

// ---------------------------------------------------------------------------
// Superadmin (platform.organizations.manage, exigido por el handler)
// ---------------------------------------------------------------------------

// Overview es la vista general: configuración, planes y todas las organizaciones.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	st, err := s.q.GetPricingSettings(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("pricing: leer configuración: %w", err)
	}
	plans, err := s.listPlans(ctx)
	if err != nil {
		return Overview{}, err
	}
	rows, err := s.q.ListOrganizationsPricing(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("pricing: listar organizaciones: %w", err)
	}
	orgs := make([]OrganizationRow, len(rows))
	for i, r := range rows {
		ov := Override{
			PlatformFeeBps: r.PlatformFeeBps, PromoPlatformFeeBps: r.PromoPlatformFeeBps, CourierFeeBps: r.CourierFeeBps,
			DeliveryFeeCents: r.DeliveryFeeCents, GatewayPlanCode: r.GatewayPlanCode, EpaycoMerchantID: r.EpaycoMerchantID,
		}
		if r.UpdatedAt.Valid {
			t := r.UpdatedAt.Time
			ov.UpdatedAt = &t
		}
		orgs[i] = OrganizationRow{
			OrganizationID: r.ID, Name: r.Name, Slug: r.Slug, Status: r.Status, Override: ov,
			HasOverride: ov.PlatformFeeBps != nil || ov.PromoPlatformFeeBps != nil || ov.CourierFeeBps != nil ||
				ov.DeliveryFeeCents != nil || ov.GatewayPlanCode != nil || ov.EpaycoMerchantID != nil,
		}
	}
	return Overview{Settings: settingsFromRow(st), Plans: plans, Organizations: orgs, Methods: methodsInfo()}, nil
}

func (s *Service) listPlans(ctx context.Context) ([]PlanRecord, error) {
	rows, err := s.q.ListGatewayFeePlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("pricing: listar planes: %w", err)
	}
	out := make([]PlanRecord, len(rows))
	for i, p := range rows {
		out[i] = PlanRecord{GatewayPlan: planFromRow(p), Notes: p.Notes, UpdatedAt: p.UpdatedAt}
	}
	return out, nil
}

func validBps(name string, v int32, maxBps int32) error {
	if v < 0 || v > maxBps {
		return apperr.Invalid(fmt.Sprintf("%s debe estar entre 0 y %d (puntos básicos)", name, maxBps))
	}
	return nil
}

func validCents(name string, v int32) error {
	if v < 0 || v > 100_000_000 {
		return apperr.Invalid(name + " debe estar entre 0 y 100.000.000 centavos")
	}
	return nil
}

// UpdateSettings cambia la configuración general. Aplica a pedidos NUEVOS: los ya confirmados
// conservan su copia de tarifas (orders.pricing_snapshot).
func (s *Service) UpdateSettings(ctx context.Context, actor identity.Principal, in Settings) (Settings, error) {
	for _, c := range []struct {
		n string
		v int32
	}{{"platform_fee_bps", in.PlatformFeeBps}, {"promo_platform_fee_bps", in.PromoPlatformFeeBps}, {"courier_fee_bps", in.CourierFeeBps}} {
		if err := validBps(c.n, c.v, BpsScale); err != nil {
			return Settings{}, err
		}
	}
	if err := validCents("delivery_fee_cents", in.DeliveryFeeCents); err != nil {
		return Settings{}, err
	}
	var out Settings
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		before, err := q.GetPricingSettings(ctx)
		if err != nil {
			return err
		}
		if _, err := q.GetGatewayFeePlan(ctx, in.GatewayPlanCode); err != nil {
			if db.IsNoRows(err) {
				return apperr.Invalid("gateway_plan_code no existe")
			}
			return err
		}
		row, err := q.UpdatePricingSettings(ctx, store.UpdatePricingSettingsParams{
			PlatformFeeBps: in.PlatformFeeBps, PromoPlatformFeeBps: in.PromoPlatformFeeBps, CourierFeeBps: in.CourierFeeBps,
			DeliveryFeeCents: in.DeliveryFeeCents, GatewayPlanCode: in.GatewayPlanCode, SplitEnabled: in.SplitEnabled,
			UpdatedBy: uuid.NullUUID{UUID: actor.ID, Valid: true},
		})
		if err != nil {
			return err
		}
		out = settingsFromRow(row)
		return audit.Entry{ActorID: actor.ID, Action: audit.PricingSettingsChanged,
			Detail: map[string]any{"before": settingsFromRow(before), "after": out}}.Insert(ctx, q)
	})
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return Settings{}, err
		}
		return Settings{}, fmt.Errorf("pricing: cambiar configuración: %w", err)
	}
	return out, nil
}

// UpdatePlan cambia las tarifas de un plan de la pasarela (p. ej. cuando ePayco termina una
// tarifa promocional).
func (s *Service) UpdatePlan(ctx context.Context, actor identity.Principal, code string, in GatewayPlan, notes *string) (PlanRecord, error) {
	for _, c := range []struct {
		n string
		v int32
	}{
		{"card_percent_bps", in.CardPercentBps}, {"international_extra_bps", in.InternationalExtraBps},
		{"wallet_percent_bps", in.WalletPercentBps}, {"pse_percent_bps", in.PSEPercentBps}, {"vat_bps", in.VATBps},
	} {
		if err := validBps(c.n, c.v, 5000); err != nil {
			return PlanRecord{}, err
		}
	}
	for _, c := range []struct {
		n string
		v int32
	}{
		{"card_fixed_cents", in.CardFixedCents}, {"wallet_fixed_cents", in.WalletFixedCents}, {"pse_fixed_cents", in.PSEFixedCents},
		{"pse_small_threshold_cents", in.PSESmallThresholdCents}, {"pse_small_fixed_cents", in.PSESmallFixedCents},
	} {
		if err := validCents(c.n, c.v); err != nil {
			return PlanRecord{}, err
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		return PlanRecord{}, apperr.Invalid("name es obligatorio (máximo 80 caracteres)")
	}
	var out PlanRecord
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		before, err := q.GetGatewayFeePlan(ctx, code)
		if db.IsNoRows(err) {
			return apperr.NotFound("plan no encontrado")
		}
		if err != nil {
			return err
		}
		row, err := q.UpdateGatewayFeePlan(ctx, store.UpdateGatewayFeePlanParams{
			Code: code, Name: name, CardPercentBps: in.CardPercentBps, CardFixedCents: in.CardFixedCents,
			InternationalExtraBps: in.InternationalExtraBps, WalletPercentBps: in.WalletPercentBps, WalletFixedCents: in.WalletFixedCents,
			PsePercentBps: in.PSEPercentBps, PseFixedCents: in.PSEFixedCents,
			PseSmallThresholdCents: in.PSESmallThresholdCents, PseSmallFixedCents: in.PSESmallFixedCents,
			VatBps: in.VATBps, Notes: notes, UpdatedBy: uuid.NullUUID{UUID: actor.ID, Valid: true},
		})
		if err != nil {
			return err
		}
		out = PlanRecord{GatewayPlan: planFromRow(row), Notes: row.Notes, UpdatedAt: row.UpdatedAt}
		return audit.Entry{ActorID: actor.ID, Action: audit.GatewayFeePlanChanged,
			Detail: map[string]any{"code": code, "before": planFromRow(before), "after": out.GatewayPlan}}.Insert(ctx, q)
	})
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return PlanRecord{}, err
		}
		return PlanRecord{}, fmt.Errorf("pricing: cambiar plan: %w", err)
	}
	return out, nil
}

// GetOrganization devuelve lo propio y lo efectivo de una organización.
func (s *Service) GetOrganization(ctx context.Context, orgID uuid.UUID) (OrganizationPricing, error) {
	ov := Override{}
	row, err := s.q.GetOrganizationPricing(ctx, orgID)
	switch {
	case err == nil:
		ov = overrideFromRow(row)
	case !db.IsNoRows(err):
		return OrganizationPricing{}, fmt.Errorf("pricing: leer organización: %w", err)
	}
	r, err := s.RatesFor(ctx, orgID)
	if err != nil {
		return OrganizationPricing{}, err
	}
	return OrganizationPricing{OrganizationID: orgID, Override: ov, Effective: r}, nil
}

// SetOrganization reemplaza lo propio de una organización (cada campo nil = heredar).
func (s *Service) SetOrganization(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in Override) (OrganizationPricing, error) {
	for _, c := range []struct {
		n string
		v *int32
	}{{"platform_fee_bps", in.PlatformFeeBps}, {"promo_platform_fee_bps", in.PromoPlatformFeeBps}, {"courier_fee_bps", in.CourierFeeBps}} {
		if c.v != nil {
			if err := validBps(c.n, *c.v, BpsScale); err != nil {
				return OrganizationPricing{}, err
			}
		}
	}
	if in.DeliveryFeeCents != nil {
		if err := validCents("delivery_fee_cents", *in.DeliveryFeeCents); err != nil {
			return OrganizationPricing{}, err
		}
	}
	if in.EpaycoMerchantID != nil {
		v := strings.TrimSpace(*in.EpaycoMerchantID)
		if v == "" {
			in.EpaycoMerchantID = nil
		} else if len(v) > 40 {
			return OrganizationPricing{}, apperr.Invalid("epayco_merchant_id es demasiado largo")
		} else {
			in.EpaycoMerchantID = &v
		}
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var before *Override
		if row, err := q.GetOrganizationPricing(ctx, orgID); err == nil {
			b := overrideFromRow(row)
			before = &b
		} else if !db.IsNoRows(err) {
			return err
		}
		if in.GatewayPlanCode != nil {
			if _, err := q.GetGatewayFeePlan(ctx, *in.GatewayPlanCode); db.IsNoRows(err) {
				return apperr.Invalid("gateway_plan_code no existe")
			} else if err != nil {
				return err
			}
		}
		if _, err := q.UpsertOrganizationPricing(ctx, store.UpsertOrganizationPricingParams{
			OrganizationID: orgID, PlatformFeeBps: in.PlatformFeeBps, PromoPlatformFeeBps: in.PromoPlatformFeeBps,
			CourierFeeBps: in.CourierFeeBps, DeliveryFeeCents: in.DeliveryFeeCents, GatewayPlanCode: in.GatewayPlanCode,
			EpaycoMerchantID: in.EpaycoMerchantID, UpdatedBy: actor.ID,
		}); err != nil {
			if db.IsForeignKeyViolation(err) {
				return apperr.NotFound("organización no encontrada")
			}
			return err
		}
		return audit.Entry{ActorID: actor.ID, Action: audit.OrganizationPricingChanged, OrganizationID: orgID,
			Detail: map[string]any{"before": before, "after": in}}.Insert(ctx, q)
	})
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return OrganizationPricing{}, err
		}
		return OrganizationPricing{}, fmt.Errorf("pricing: cambiar organización: %w", err)
	}
	return s.GetOrganization(ctx, orgID)
}

// SimulateLine es una línea del simulador del superadmin.
type SimulateLine struct {
	LocalCents       int64 `json:"local_cents"`
	Quantity         int64 `json:"quantity"`
	PromoDiscountBps int32 `json:"promo_discount_bps"`
}

// SimulateInput es lo que manda la pantalla de simulación.
type SimulateInput struct {
	OrganizationID *uuid.UUID     `json:"organization_id"`
	Lines          []SimulateLine `json:"lines"`
	DiscountCents  int64          `json:"discount_cents"`
	// DeliveryFeeCents nil = el de la configuración efectiva.
	DeliveryFeeCents *int64 `json:"delivery_fee_cents"`
}

// SimulateResult tiene el desglose para cada medio de pago.
type SimulateResult struct {
	Rates  Rates           `json:"rates"`
	Lines  []SimulatedLine `json:"lines"`
	Quotes []Quote         `json:"quotes"`
}

// SimulatedLine es una línea con su precio publicado.
type SimulatedLine struct {
	SimulateLine
	PublishedCents int64 `json:"published_cents"`
	LocalNetCents  int64 `json:"local_net_cents"`
	FeeBps         int32 `json:"fee_bps"`
}

// Simulate calcula, con las tarifas vigentes (de una organización o las generales), cuánto
// pagaría el cliente con cada medio. Es el mismo algoritmo que el carrito y el pago.
func (s *Service) Simulate(ctx context.Context, in SimulateInput) (SimulateResult, error) {
	if len(in.Lines) == 0 || len(in.Lines) > 50 {
		return SimulateResult{}, apperr.Invalid("lines debe tener entre 1 y 50 líneas")
	}
	var r Rates
	var err error
	if in.OrganizationID != nil {
		r, err = s.RatesFor(ctx, *in.OrganizationID)
	} else {
		var st store.PricingSetting
		st, err = s.q.GetPricingSettings(ctx)
		if err == nil {
			r, err = resolve(ctx, s.q, settingsFromRow(st), Override{})
		}
	}
	if err != nil {
		return SimulateResult{}, err
	}
	var a OrderAmounts
	lines := make([]SimulatedLine, len(in.Lines))
	for i, l := range in.Lines {
		if l.LocalCents < 0 || l.LocalCents > 100_000_000 || l.Quantity < 1 || l.Quantity > 50 {
			return SimulateResult{}, apperr.Invalid("cada línea necesita local_cents entre 0 y 100.000.000 y quantity entre 1 y 50")
		}
		if l.PromoDiscountBps < 0 || l.PromoDiscountBps > 9000 {
			return SimulateResult{}, apperr.Invalid("promo_discount_bps debe estar entre 0 y 9000")
		}
		u := r.PriceUnit(l.LocalCents, nil, l.PromoDiscountBps)
		lines[i] = SimulatedLine{SimulateLine: l, PublishedCents: u.TotalCents, LocalNetCents: u.LocalCents, FeeBps: u.FeeBps}
		a.SubtotalCents += u.TotalCents * l.Quantity
		a.SubtotalLocalCents += u.LocalCents * l.Quantity
	}
	a.DiscountCents = in.DiscountCents
	a.DeliveryFeeCents = int64(r.DeliveryFeeCents)
	if in.DeliveryFeeCents != nil {
		a.DeliveryFeeCents = *in.DeliveryFeeCents
	}
	quotes := make([]Quote, 0, len(Methods))
	for _, m := range Methods {
		q, err := r.QuoteFor(a, m)
		if err != nil {
			return SimulateResult{}, apperr.Invalid("las tarifas configuradas no tienen solución (porcentaje demasiado alto)")
		}
		quotes = append(quotes, q)
	}
	return SimulateResult{Rates: r, Lines: lines, Quotes: quotes}, nil
}

// PublicRatesFor son las comisiones que el ADMIN de una organización necesita para ver el precio
// publicado mientras edita un producto. Exige poder leer el catálogo de esa organización.
type PublicRates struct {
	PlatformFeeBps      int32 `json:"platform_fee_bps"`
	PromoPlatformFeeBps int32 `json:"promo_platform_fee_bps"`
	DeliveryFeeCents    int32 `json:"delivery_fee_cents"`
}

// PublicRatesFor ver PublicRates.
func (s *Service) PublicRatesFor(ctx context.Context, actor identity.Principal, orgID uuid.UUID) (PublicRates, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogRead, false); err != nil {
		return PublicRates{}, err
	}
	r, err := s.RatesFor(ctx, orgID)
	if err != nil {
		return PublicRates{}, err
	}
	return PublicRates{PlatformFeeBps: r.PlatformFeeBps, PromoPlatformFeeBps: r.PromoPlatformFeeBps, DeliveryFeeCents: r.DeliveryFeeCents}, nil
}
