package pricing

import "testing"

// pesos convierte pesos a centavos para que las tablas se lean como en docs/pagos.md.
func pesos(p int64) int64 { return p * Peso }

func davivienda() GatewayPlan {
	return GatewayPlan{
		Code: "epayco_davivienda", Name: "ePayco · Davivienda",
		CardPercentBps: 264, CardFixedCents: 690 * Peso, InternationalExtraBps: 80,
		WalletPercentBps: 264, WalletFixedCents: 690 * Peso,
		PSEPercentBps: 264, PSEFixedCents: 690 * Peso,
		PSESmallThresholdCents: 60000 * Peso, PSESmallFixedCents: 2200 * Peso,
		VATBps: 1900,
	}
}

func otrosBancos() GatewayPlan {
	p := davivienda()
	p.Code, p.Name = "epayco_otros_bancos", "ePayco · otros bancos"
	p.CardPercentBps, p.CardFixedCents = 329, 700*Peso
	p.WalletPercentBps, p.WalletFixedCents = 329, 700*Peso
	p.PSEPercentBps, p.PSEFixedCents = 329, 700*Peso
	return p
}

func rates(plan GatewayPlan) Rates {
	return Rates{PlatformFeeBps: 1000, PromoPlatformFeeBps: 500, Plan: plan}
}

func TestPublish(t *testing.T) {
	cases := []struct {
		name  string
		local int64
		fee   int32
		want  int64
	}{
		{"10 % exacto", pesos(20000), 1000, pesos(22000)},
		{"10 % sin redondeo comercial", pesos(18900), 1000, pesos(20790)},
		{"fracción de peso hacia arriba", pesos(12345), 1000, pesos(13580)}, // 13.579,5
		{"5 % de promoción", pesos(16000), 500, pesos(16800)},
		{"cero", 0, 1000, 0},
		{"sin comisión", pesos(5000), 0, pesos(5000)},
	}
	for _, c := range cases {
		if got := Publish(c.local, c.fee); got != c.want {
			t.Errorf("%s: Publish(%d, %d) = %d, quería %d", c.name, c.local, c.fee, got, c.want)
		}
	}
}

func TestPriceUnitConPromocion(t *testing.T) {
	r := rates(davivienda())
	// Local $20.000, el restaurante da 20 % → $16.000 local; comisión de promoción 5 % → $16.800.
	u := r.PriceUnit(pesos(20000), []int64{pesos(2000)}, 2000)
	if u.VariantCents != pesos(16800) || u.LocalCents != pesos(16000+1600) || u.ModifierCents[0] != pesos(1680) {
		t.Fatalf("promo: %+v", u)
	}
	if u.TotalCents != pesos(16800+1680) || u.FeeBps != 500 {
		t.Fatalf("promo total: %+v", u)
	}
	// Sin promoción: 10 %.
	u = r.PriceUnit(pesos(20000), nil, 0)
	if u.TotalCents != pesos(22000) || u.LocalCents != pesos(20000) || u.FeeBps != 1000 {
		t.Fatalf("normal: %+v", u)
	}
}

// El ejemplo de docs/pagos.md §8: 2 hamburguesas ($20.000 local) + 1 gaseosa ($4.000 local),
// domicilio $5.000.
func ejemplo() OrderAmounts {
	return OrderAmounts{
		SubtotalCents: pesos(48400), SubtotalLocalCents: pesos(44000), DeliveryFeeCents: pesos(5000),
	}
}

func TestQuoteEjemploDelDocumento(t *testing.T) {
	cases := []struct {
		plan  GatewayPlan
		m     Method
		total int64
	}{
		{davivienda(), MethodCard, pesos(55980)},
		{otrosBancos(), MethodCard, pesos(56443)},
		{davivienda(), MethodPSE, pesos(56018)},               // < $60.000: tarifa fija $2.200 + IVA
		{davivienda(), MethodCardInternational, pesos(56536)}, // 2,64 % + 0,8 % = 3,44 %
	}
	for _, c := range cases {
		q, err := rates(c.plan).QuoteFor(ejemplo(), c.m)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.plan.Code, c.m, err)
		}
		if q.BaseCents != pesos(53400) {
			t.Fatalf("base = %d", q.BaseCents)
		}
		if q.TotalCents != c.total {
			t.Errorf("%s/%s: total = %d, quería %d", c.plan.Code, c.m, q.TotalCents/Peso, c.total/Peso)
		}
		if net := q.TotalCents - q.GatewayFeeCents - q.GatewayFeeVATCents; net < q.BaseCents || net-q.BaseCents >= Peso {
			t.Errorf("%s/%s: neto %d no cubre exacto la base %d", c.plan.Code, c.m, net, q.BaseCents)
		}
		if q.TransactionFeeCents != q.TotalCents-q.BaseCents {
			t.Errorf("costo de transacción inconsistente: %+v", q)
		}
	}
}

func TestSplitDelEjemplo(t *testing.T) {
	r := rates(davivienda())
	r.CourierFeeBps = 500 // 5 % al domiciliario
	s := r.SplitFor(ejemplo())
	if s.OrganizationCents != pesos(44000) || s.PlatformFeeCents != pesos(4400) {
		t.Fatalf("split: %+v", s)
	}
	if s.CourierFeeCents != pesos(250) || s.CourierCents != pesos(4750) || s.PlatformCents != pesos(4650) {
		t.Fatalf("split domicilio: %+v", s)
	}
}

func TestSplitDescuentoDeCodigoLoAsumeElRestaurante(t *testing.T) {
	a := ejemplo()
	a.DiscountCents = pesos(5000)
	s := rates(davivienda()).SplitFor(a)
	if s.OrganizationCents != pesos(39000) || s.PlatformFeeCents != pesos(4400) {
		t.Fatalf("split con cupón: %+v", s)
	}
	// Un descuento mayor que la parte del restaurante: el resto sale de la comisión, nunca negativo.
	a.DiscountCents = pesos(46000)
	s = rates(davivienda()).SplitFor(a)
	if s.OrganizationCents != 0 || s.PlatformFeeCents != pesos(2400) {
		t.Fatalf("split con cupón enorme: %+v", s)
	}
}

// Alrededor del umbral de PSE ($60.000) la tarifa salta de fija a porcentual: el total debe cubrir
// siempre la base con la regla que REALMENTE aplica a ese total.
func TestPSEAlrededorDelUmbral(t *testing.T) {
	p := davivienda()
	for base := pesos(55000); base <= pesos(62000); base += pesos(37) {
		total, err := p.grossUp(base, MethodPSE)
		if err != nil {
			t.Fatalf("base %d: %v", base, err)
		}
		if total%Peso != 0 {
			t.Fatalf("base %d: total %d no es un peso entero", base, total)
		}
		if p.netAfter(total, MethodPSE) < base {
			t.Fatalf("base %d: total %d deja %d (corto)", base, total, p.netAfter(total, MethodPSE))
		}
	}
}

// Para cualquier base y medio: el total es el MENOR peso entero que cubre la base.
func TestGrossUpEsMinimo(t *testing.T) {
	for _, plan := range []GatewayPlan{davivienda(), otrosBancos()} {
		for _, m := range []Method{MethodCard, MethodCardInternational, MethodWallet} {
			for base := pesos(1000); base <= pesos(500000); base = base*3/2 + pesos(7) {
				total, err := plan.grossUp(base, m)
				if err != nil {
					t.Fatal(err)
				}
				if plan.netAfter(total, m) < base {
					t.Fatalf("%s/%s base %d: corto", plan.Code, m, base)
				}
				if plan.netAfter(total-Peso, m) >= base {
					t.Fatalf("%s/%s base %d: %d no es el mínimo", plan.Code, m, base, total)
				}
			}
		}
	}
}

func TestQuoteRechazaEntradasInvalidas(t *testing.T) {
	r := rates(davivienda())
	if _, err := r.QuoteFor(ejemplo(), Method("bitcoin")); err == nil {
		t.Fatal("medio desconocido aceptado")
	}
	bad := davivienda()
	bad.CardPercentBps = 9000 // 90 % × 1,19 > 100 %: sin solución
	if _, err := rates(bad).QuoteFor(ejemplo(), MethodCard); err == nil {
		t.Fatal("tarifa imposible aceptada")
	}
	q, err := r.QuoteFor(OrderAmounts{}, MethodCard)
	if err != nil || q.TotalCents != 0 {
		t.Fatalf("pedido vacío: %+v %v", q, err)
	}
}
