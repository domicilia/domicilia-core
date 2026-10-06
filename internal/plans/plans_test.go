package plans

import (
	"slices"
	"testing"
)

func TestElCatalogoEsCoherente(t *testing.T) {
	seen := map[Feature]bool{}
	for _, f := range Features {
		if seen[f.Key] {
			t.Errorf("función repetida: %s", f.Key)
		}
		seen[f.Key] = true
		if !ValidTier(string(f.MinTier)) {
			t.Errorf("la función %s pide un plan que no existe: %q", f.Key, f.MinTier)
		}
		if f.Label == "" {
			t.Errorf("la función %s no tiene etiqueta", f.Key)
		}
	}

	prevPrice, prevMembers := -1, 0
	for i, p := range Catalog {
		if i > 0 && !p.Tier.AtLeast(Catalog[i-1].Tier) {
			t.Errorf("el catálogo debe ir de menor a mayor: %s tras %s", p.Tier, Catalog[i-1].Tier)
		}
		if p.PriceMonthlyCOP < prevPrice {
			t.Errorf("el plan %s cuesta menos que el anterior", p.Tier)
		}
		prevPrice = p.PriceMonthlyCOP
		// Un plan más alto nunca admite menos miembros que uno más bajo.
		if p.Limits.MaxMembers != Unlimited && p.Limits.MaxMembers < prevMembers {
			t.Errorf("el plan %s admite menos miembros que el anterior", p.Tier)
		}
		if p.Limits.MaxMembers != Unlimited {
			prevMembers = p.Limits.MaxMembers
		}
	}
	if Default != Starter {
		t.Errorf("el plan por omisión debía ser starter, es %s", Default)
	}
}

func TestUnPlanIncluyeLoDeLosAnteriores(t *testing.T) {
	// Lo que incluye un plan lo incluyen todos los superiores.
	for _, f := range Features {
		included := false
		for _, p := range Catalog {
			if Included(p.Tier, f.Key) {
				included = true
			} else if included {
				t.Errorf("%s está en un plan inferior a %s pero no en %s", f.Key, p.Tier, p.Tier)
			}
		}
		if !included {
			t.Errorf("la función %s no está en ningún plan", f.Key)
		}
	}
}

func TestCadaPlanIncluyeLoQueDiceElFrontend(t *testing.T) {
	// Fijado contra docs/whatsapp-modulos/tiers-y-precios.md: si cambia la matriz comercial,
	// este es el lugar donde se decide, a propósito.
	want := map[Tier][]Feature{
		Starter:    {Inbox24h, TemplateInWindow},
		Pro:        {TemplateSync, TemplatePreview, LabelsPriority, Teams, Macros, CSAT, BulkInbox, ContactNotes, ContactHistory},
		Outreach:   {ProactiveOutreach, CampaignsBulk, CampaignSegments},
		Enterprise: {MultiInbox, APIWebhooks, EmbeddedSignup, SLA, AICopilot},
	}
	for tier, feats := range want {
		for _, f := range feats {
			def, ok := LookupFeature(string(f))
			if !ok || def.MinTier != tier {
				t.Errorf("%s debía requerir el plan %s", f, tier)
			}
		}
	}
	if n := len(Features); n != 19 {
		t.Errorf("funciones = %d, quería 19 (las de WA_FEATURES)", n)
	}
}

func TestEffective(t *testing.T) {
	t.Run("sin excepciones son las del plan, en el orden del catálogo", func(t *testing.T) {
		got := Effective(Pro, nil)
		if !Has(got, TemplateSync) || Has(got, CampaignsBulk) || !Has(got, Inbox24h) {
			t.Fatalf("Effective(pro) = %v", got)
		}
		if !slices.IsSorted(indexes(got)) {
			t.Errorf("fuera del orden del catálogo: %v", got)
		}
	})

	t.Run("una excepción enciende o apaga, y manda sobre el plan", func(t *testing.T) {
		got := Effective(Starter, []Override{{Feature: CampaignsBulk, Enabled: true}, {Feature: Inbox24h, Enabled: false}})
		if !Has(got, CampaignsBulk) {
			t.Error("la excepción debía encender campañas en starter")
		}
		if Has(got, Inbox24h) {
			t.Error("la excepción debía apagar el inbox")
		}
	})

	t.Run("una excepción de una función desconocida no agrega nada", func(t *testing.T) {
		got := Effective(Starter, []Override{{Feature: "inventada", Enabled: true}})
		if Has(got, "inventada") || len(got) != len(Effective(Starter, nil)) {
			t.Fatalf("Effective = %v", got)
		}
	})

	t.Run("un plan desconocido no incluye nada", func(t *testing.T) {
		if got := Effective("gold", nil); len(got) != 0 {
			t.Fatalf("Effective(gold) = %v, quería vacío: falla cerrado", got)
		}
		if Tier("gold").AtLeast(Starter) {
			t.Error("un plan desconocido no debe superar a ninguno")
		}
	})
}

func indexes(fs []Feature) []int {
	out := make([]int, 0, len(fs))
	for _, f := range fs {
		for i, d := range Features {
			if d.Key == f {
				out = append(out, i)
			}
		}
	}
	return out
}
