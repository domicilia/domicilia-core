// Package plans son los planes comerciales (Starter, Pro, Outreach, Enterprise), lo
// que incluye cada uno y lo que una organización puede usar de verdad: su plan más
// las excepciones que el operador le haya puesto.
//
// El catálogo vive en código, igual que los permisos: cambiar qué incluye un plan es
// un cambio revisado y probado, no una edición de tabla. La base guarda solo el plan
// de cada organización, su historial y sus excepciones.
//
// Las claves de las funciones son las de WA_FEATURES del frontend
// (lib/billing/features.ts). El servidor es quien decide: el frontend solo oculta la
// interfaz, y ningún endpoint premium debe confiar en eso.
package plans

import "slices"

// Tier es un plan.
type Tier string

// Planes, de menor a mayor.
const (
	Starter    Tier = "starter"
	Pro        Tier = "pro"
	Outreach   Tier = "outreach"
	Enterprise Tier = "enterprise"
)

// Default es el plan con el que nace una organización.
const Default = Starter

// Unlimited marca un límite sin tope.
const Unlimited = -1

// order da la posición de cada plan: un plan incluye todo lo de los anteriores.
func (t Tier) order() int {
	switch t {
	case Starter:
		return 0
	case Pro:
		return 1
	case Outreach:
		return 2
	case Enterprise:
		return 3
	default:
		return -1
	}
}

// AtLeast dice si el plan es igual o superior a min.
func (t Tier) AtLeast(min Tier) bool { return t.order() >= min.order() && t.order() >= 0 }

// Feature es una función que un plan puede incluir.
type Feature string

// Funciones (claves iguales a las de WA_FEATURES del frontend).
const (
	Inbox24h          Feature = "inbox_24h"
	TemplateInWindow  Feature = "template_in_window"
	TemplateSync      Feature = "template_sync"
	TemplatePreview   Feature = "template_preview_pro"
	LabelsPriority    Feature = "labels_priority"
	Teams             Feature = "teams"
	Macros            Feature = "macros"
	CSAT              Feature = "csat"
	BulkInbox         Feature = "bulk_inbox"
	ContactNotes      Feature = "contact_notes"
	ContactHistory    Feature = "contact_history"
	ProactiveOutreach Feature = "proactive_outreach"
	CampaignsBulk     Feature = "campaigns_bulk"
	CampaignSegments  Feature = "campaign_segments"
	MultiInbox        Feature = "multi_inbox"
	APIWebhooks       Feature = "api_webhooks"
	EmbeddedSignup    Feature = "embedded_signup"
	SLA               Feature = "sla"
	AICopilot         Feature = "ai_copilot"
)

// FeatureDef describe una función y el plan mínimo que la incluye.
type FeatureDef struct {
	Key     Feature `json:"key"`
	Label   string  `json:"label"`
	MinTier Tier    `json:"min_tier"`
}

// Features es el catálogo de funciones.
var Features = []FeatureDef{
	{Inbox24h, "Inbox WhatsApp 24h", Starter},
	{TemplateInWindow, "Plantillas en conversación", Starter},
	{TemplateSync, "Sync plantillas Meta", Pro},
	{TemplatePreview, "Preview avanzado plantillas", Pro},
	{LabelsPriority, "Etiquetas y prioridad", Pro},
	{Teams, "Equipos y colas", Pro},
	{Macros, "Macros / respuestas rápidas", Pro},
	{CSAT, "Encuestas CSAT", Pro},
	{BulkInbox, "Acciones masivas inbox", Pro},
	{ContactNotes, "Notas de contacto", Pro},
	{ContactHistory, "Historial de conversaciones", Pro},
	{ProactiveOutreach, "Chat proactivo (fuera 24h)", Outreach},
	{CampaignsBulk, "Campañas masivas", Outreach},
	{CampaignSegments, "Segmentación campañas", Outreach},
	{MultiInbox, "Multi-número WhatsApp", Enterprise},
	{APIWebhooks, "API y webhooks", Enterprise},
	{EmbeddedSignup, "Embedded Signup Meta", Enterprise},
	{SLA, "SLA y alertas", Enterprise},
	{AICopilot, "Copilot IA", Enterprise},
}

// LookupFeature busca una función por clave.
func LookupFeature(key string) (FeatureDef, bool) {
	for _, f := range Features {
		if string(f.Key) == key {
			return f, true
		}
	}
	return FeatureDef{}, false
}

// Limits son los topes de un plan. Unlimited (-1) es sin tope.
type Limits struct {
	// MaxMembers cuenta miembros e invitaciones pendientes.
	MaxMembers int `json:"max_members"`
}

// Plan es un plan comercial.
type Plan struct {
	Tier            Tier   `json:"tier"`
	Name            string `json:"name"`
	Tagline         string `json:"tagline"`
	PriceMonthlyCOP int    `json:"price_monthly_cop"`
	Limits          Limits `json:"limits"`
}

// Catalog son los planes, de menor a mayor. Precios y textos son los de
// lib/billing/tiers.ts del frontend.
//
// LOS LÍMITES SON VALORES INICIALES A CONFIRMAR CON NEGOCIO: el frontend no los
// define. Cambiarlos no requiere migración.
var Catalog = []Plan{
	{Starter, "Starter", "Inbox 1:1 dentro de ventana 24h", 0, Limits{MaxMembers: 3}},
	{Pro, "Pro", "Operación en equipo, etiquetas, macros y CSAT", 149_000, Limits{MaxMembers: 10}},
	{Outreach, "Outreach WA", "Proactivo fuera de 24h + campañas masivas", 299_000, Limits{MaxMembers: 25}},
	{Enterprise, "Enterprise", "Multi-número, API, Embedded Signup, SLA", 599_000, Limits{MaxMembers: Unlimited}},
}

// Lookup busca un plan por su clave.
func Lookup(tier string) (Plan, bool) {
	for _, p := range Catalog {
		if string(p.Tier) == tier {
			return p, true
		}
	}
	return Plan{}, false
}

// ValidTier dice si es un plan conocido.
func ValidTier(tier string) bool {
	_, ok := Lookup(tier)
	return ok
}

// Override es una excepción: enciende o apaga una función para una organización.
type Override struct {
	Feature Feature `json:"feature"`
	Enabled bool    `json:"enabled"`
	Reason  *string `json:"reason"`
}

// Included dice si el plan incluye la función, sin mirar excepciones.
func Included(tier Tier, f Feature) bool {
	def, ok := LookupFeature(string(f))
	return ok && tier.AtLeast(def.MinTier)
}

// Effective devuelve las funciones que la organización puede usar: las de su plan,
// más las que una excepción enciende, menos las que una excepción apaga. Sale
// ordenado como el catálogo.
func Effective(tier Tier, overrides []Override) []Feature {
	forced := make(map[Feature]bool, len(overrides))
	for _, o := range overrides {
		forced[o.Feature] = o.Enabled
	}
	out := make([]Feature, 0, len(Features))
	for _, def := range Features {
		on := Included(tier, def.Key)
		if v, ok := forced[def.Key]; ok {
			on = v
		}
		if on {
			out = append(out, def.Key)
		}
	}
	return out
}

// Has dice si la lista de funciones efectivas incluye una.
func Has(effective []Feature, f Feature) bool { return slices.Contains(effective, f) }
