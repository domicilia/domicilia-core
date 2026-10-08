// Package pricing es el algoritmo de cobro de Domicilia (docs/pagos.md §8): precio publicado a
// partir del precio local del restaurante, comisión de la plataforma, promociones de producto y el
// "despeje" del costo de transacción de la pasarela según el medio de pago que elige el cliente.
//
// Es aritmética pura: no lee la base ni conoce HTTP. Quien la usa (catalog, orders, payments) le
// pasa las tarifas vigentes (Rates) que resuelve el Service de este mismo paquete. Así el cálculo
// se prueba con tablas, sin base de datos, y es imposible que el catálogo, el carrito y el pago
// calculen el mismo precio de dos formas distintas.
//
// Unidades: todos los montos van en CENTAVOS de peso (COP × 100), igual que el resto del core.
// Los porcentajes van en puntos básicos (bps): 10000 = 100 %, 1000 = 10 %, 264 = 2,64 %.
// Regla de redondeo (decisión del usuario, 2026-10-08): "cobrar el valor real" — sin redondeo
// comercial. Solo se redondea al PESO entero (100 centavos) porque ePayco cobra en pesos:
// los precios publicados, mitad hacia arriba; el total a cobrar, siempre hacia arriba (para que
// la plataforma nunca quede corta por una fracción).
package pricing

import (
	"errors"
	"math/big"
)

// Peso son los centavos de un peso: la unidad mínima que se cobra.
const Peso = 100

// BpsScale es el 100 % en puntos básicos.
const BpsScale = 10000

// Method es el medio de pago que el cliente declara ANTES de ir al checkout. De él depende la
// tarifa de la pasarela (docs/pagos.md §7.1).
type Method string

// Medios de pago.
const (
	MethodCard              Method = "card"               // tarjeta crédito/débito nacional
	MethodCardInternational Method = "card_international" // tarjeta emitida fuera de Colombia
	MethodPSE               Method = "pse"                // débito bancario PSE
	MethodWallet            Method = "wallet"             // Nequi, Daviplata
)

// Methods en el orden en que se ofrecen al cliente.
var Methods = []Method{MethodCard, MethodPSE, MethodWallet, MethodCardInternational}

// Valid dice si m es un medio conocido.
func (m Method) Valid() bool {
	for _, k := range Methods {
		if m == k {
			return true
		}
	}
	return false
}

// Label es el nombre para mostrar.
func (m Method) Label() string {
	switch m {
	case MethodCard:
		return "Tarjeta nacional"
	case MethodCardInternational:
		return "Tarjeta internacional"
	case MethodPSE:
		return "PSE"
	case MethodWallet:
		return "Nequi o Daviplata"
	}
	return string(m)
}

// GatewayPlan son las tarifas de un plan de la pasarela (p. ej. ePayco "Davivienda" u "otros
// bancos"). Todo configurable por el superadmin: la tarifa promocional de hoy cambia mañana.
type GatewayPlan struct {
	Code    string `json:"code"`
	Gateway string `json:"gateway"`
	Name    string `json:"name"`
	// Tarjeta nacional.
	CardPercentBps int32 `json:"card_percent_bps"`
	CardFixedCents int32 `json:"card_fixed_cents"`
	// Recargo de las tarjetas internacionales, sobre el porcentaje de tarjeta.
	InternationalExtraBps int32 `json:"international_extra_bps"`
	// Billeteras (Nequi, Daviplata).
	WalletPercentBps int32 `json:"wallet_percent_bps"`
	WalletFixedCents int32 `json:"wallet_fixed_cents"`
	// PSE: tarifa normal y la regla de montos pequeños ("transacciones menores a $60.000 con PSE
	// tienen un costo de $2.200", epayco.com/tarifas).
	PSEPercentBps          int32 `json:"pse_percent_bps"`
	PSEFixedCents          int32 `json:"pse_fixed_cents"`
	PSESmallThresholdCents int32 `json:"pse_small_threshold_cents"`
	PSESmallFixedCents     int32 `json:"pse_small_fixed_cents"`
	// IVA sobre la tarifa de la pasarela ("todos los valores expresados son más IVA").
	VATBps int32 `json:"vat_bps"`
}

// Rates son las tarifas EFECTIVAS de una organización: la configuración general con lo que el
// superadmin le haya sobrescrito a ella. Las resuelve Service.RatesFor.
type Rates struct {
	PlatformFeeBps      int32       `json:"platform_fee_bps"`
	PromoPlatformFeeBps int32       `json:"promo_platform_fee_bps"`
	CourierFeeBps       int32       `json:"courier_fee_bps"`
	DeliveryFeeCents    int32       `json:"delivery_fee_cents"`
	Plan                GatewayPlan `json:"plan"`
	SplitEnabled        bool        `json:"split_enabled"`
	// EpaycoMerchantID es el id del restaurante como receptor del split (no es un secreto).
	EpaycoMerchantID string `json:"epayco_merchant_id"`
}

// ErrInvalid es un dato de entrada imposible (montos negativos, porcentajes fuera de rango).
var ErrInvalid = errors.New("pricing: dato inválido")

// roundHalfUpToPeso redondea centavos al peso más cercano (mitad hacia arriba).
func roundHalfUpToPeso(cents int64) int64 {
	if cents <= 0 {
		return 0
	}
	return (cents + Peso/2) / Peso * Peso
}

// ceilToPeso redondea centavos hacia arriba al peso entero.
func ceilToPeso(cents int64) int64 {
	if cents <= 0 {
		return 0
	}
	return (cents + Peso - 1) / Peso * Peso
}

// mulBps devuelve cents × bps / 10000 redondeado al peso (mitad hacia arriba).
func mulBps(cents int64, bps int32) int64 {
	return roundHalfUpToPeso(cents * int64(bps) / BpsScale)
}

// ApplyDiscount devuelve el precio local con el descuento de promoción del RESTAURANTE aplicado
// (discountBps sobre el precio local), redondeado al peso. 0 = sin promoción.
func ApplyDiscount(localCents int64, discountBps int32) int64 {
	if discountBps <= 0 {
		return localCents
	}
	return roundHalfUpToPeso(localCents * int64(BpsScale-discountBps) / BpsScale)
}

// Publish es el precio que ve y paga el cliente: precio local × (1 + comisión), redondeado al peso
// (mitad hacia arriba). Ejemplo: 20.000 con 10 % → 22.000.
func Publish(localCents int64, feeBps int32) int64 {
	if localCents <= 0 {
		return 0
	}
	return roundHalfUpToPeso(localCents * int64(BpsScale+feeBps) / BpsScale)
}

// FeeFor es la comisión de la plataforma que aplica a un producto: la de promoción si el
// restaurante lo publicó con descuento, la normal si no (docs/pagos.md §8, decisión f).
func (r Rates) FeeFor(promoDiscountBps int32) int32 {
	if promoDiscountBps > 0 {
		return r.PromoPlatformFeeBps
	}
	return r.PlatformFeeBps
}

// UnitPrice es el precio unitario de una línea: la variante más sus modificadores, cada parte
// publicada por separado (así el detalle que ve el cliente cuadra con el total). Devuelve el
// precio publicado de la variante, el de cada modificador, el total publicado y el total LOCAL
// (lo que le corresponde al restaurante, ya con su descuento de promoción).
type UnitPrice struct {
	VariantCents  int64
	ModifierCents []int64
	TotalCents    int64
	LocalCents    int64
	FeeBps        int32
	PromoDiscount int32
}

// PriceUnit calcula UnitPrice. promoDiscountBps aplica a la variante y a los modificadores.
func (r Rates) PriceUnit(variantLocalCents int64, modifierLocalCents []int64, promoDiscountBps int32) UnitPrice {
	fee := r.FeeFor(promoDiscountBps)
	out := UnitPrice{FeeBps: fee, PromoDiscount: promoDiscountBps, ModifierCents: make([]int64, len(modifierLocalCents))}
	local := ApplyDiscount(variantLocalCents, promoDiscountBps)
	out.VariantCents = Publish(local, fee)
	out.LocalCents = local
	out.TotalCents = out.VariantCents
	for i, m := range modifierLocalCents {
		ml := ApplyDiscount(m, promoDiscountBps)
		out.ModifierCents[i] = Publish(ml, fee)
		out.LocalCents += ml
		out.TotalCents += out.ModifierCents[i]
	}
	return out
}

// OrderAmounts son los montos de un pedido ya confirmado, sin el costo de transacción (ese
// depende del medio de pago y se calcula en Quote).
type OrderAmounts struct {
	SubtotalCents      int64 `json:"subtotal_cents"`       // publicado: lo que paga el cliente por la comida
	SubtotalLocalCents int64 `json:"subtotal_local_cents"` // local: lo del restaurante (antes de descuentos de código)
	DiscountCents      int64 `json:"discount_cents"`       // cupón de código, lo asume el restaurante
	DeliveryFeeCents   int64 `json:"delivery_fee_cents"`
}

// Split es el reparto del dinero de un pedido entre las partes (docs/pagos.md §8).
type Split struct {
	// Restaurante: subtotal local menos el descuento de código (nunca negativo).
	OrganizationCents int64 `json:"organization_cents"`
	// Domicilia: la comisión (subtotal publicado − subtotal local) más su parte del domicilio.
	PlatformCents int64 `json:"platform_cents"`
	// Comisión de la plataforma sobre los productos (sin la parte del domicilio).
	PlatformFeeCents int64 `json:"platform_fee_cents"`
	// Domiciliario: el domicilio menos la comisión que Domicilia le cobra (CourierFeeBps).
	CourierCents int64 `json:"courier_cents"`
	// Parte del domicilio que se queda Domicilia.
	CourierFeeCents int64 `json:"courier_fee_cents"`
}

// SplitFor reparte un pedido. El descuento de código lo asume el restaurante (supuesto
// documentado en docs/pagos.md §3, pregunta abierta): si fuera mayor que su parte, su parte
// queda en 0 y la diferencia sale de la comisión.
func (r Rates) SplitFor(a OrderAmounts) Split {
	fee := a.SubtotalCents - a.SubtotalLocalCents
	if fee < 0 {
		fee = 0
	}
	org := a.SubtotalLocalCents - a.DiscountCents
	if org < 0 {
		fee += org // el exceso del descuento lo absorbe la comisión
		if fee < 0 {
			fee = 0
		}
		org = 0
	}
	courierFee := mulBps(a.DeliveryFeeCents, r.CourierFeeBps)
	return Split{
		OrganizationCents: org,
		PlatformFeeCents:  fee,
		CourierFeeCents:   courierFee,
		CourierCents:      a.DeliveryFeeCents - courierFee,
		PlatformCents:     fee + courierFee,
	}
}

// Quote es lo que se le muestra al cliente antes de pagar: el desglose completo y el costo de
// transacción del medio elegido. TotalCents es EXACTAMENTE lo que se cobra.
type Quote struct {
	Method              Method `json:"method"`
	MethodLabel         string `json:"method_label"`
	PlanCode            string `json:"plan_code"`
	PlanName            string `json:"plan_name"`
	SubtotalCents       int64  `json:"subtotal_cents"`
	DiscountCents       int64  `json:"discount_cents"`
	DeliveryFeeCents    int64  `json:"delivery_fee_cents"`
	BaseCents           int64  `json:"base_cents"`            // lo que debe quedar después de la pasarela
	TransactionFeeCents int64  `json:"transaction_fee_cents"` // lo que se suma por pagar con este medio
	TotalCents          int64  `json:"total_cents"`
	// Desglose de la tarifa que se espera que cobre la pasarela sobre TotalCents.
	GatewayFeeCents    int64 `json:"gateway_fee_cents"`
	GatewayFeeVATCents int64 `json:"gateway_fee_vat_cents"`
	Split              Split `json:"split"`
}

// QuoteFor calcula el total a cobrar con el medio m: la base (comida − descuento + domicilio)
// "despejada" para que, después de que la pasarela descuente su tarifa + IVA sobre el TOTAL, queden
// exactos los centavos de la base (docs/pagos.md §8).
func (r Rates) QuoteFor(a OrderAmounts, m Method) (Quote, error) {
	if !m.Valid() {
		return Quote{}, ErrInvalid
	}
	if a.SubtotalCents < 0 || a.DiscountCents < 0 || a.DeliveryFeeCents < 0 {
		return Quote{}, ErrInvalid
	}
	base := a.SubtotalCents - a.DiscountCents + a.DeliveryFeeCents
	if base < 0 {
		base = 0
	}
	total, err := r.Plan.grossUp(base, m)
	if err != nil {
		return Quote{}, err
	}
	fee, vat := r.Plan.feeOn(total, m)
	return Quote{
		Method: m, MethodLabel: m.Label(), PlanCode: r.Plan.Code, PlanName: r.Plan.Name,
		SubtotalCents: a.SubtotalCents, DiscountCents: a.DiscountCents, DeliveryFeeCents: a.DeliveryFeeCents,
		BaseCents: base, TransactionFeeCents: total - base, TotalCents: total,
		GatewayFeeCents: fee, GatewayFeeVATCents: vat,
		Split: r.SplitFor(a),
	}, nil
}

// rule es la tarifa de un medio para un monto dado: porcentaje + fijo.
type rule struct {
	percentBps int32
	fixedCents int32
}

// ruleFor devuelve la tarifa que la pasarela aplica a un cobro de totalCents con el medio m.
func (p GatewayPlan) ruleFor(totalCents int64, m Method) rule {
	switch m {
	case MethodCardInternational:
		return rule{p.CardPercentBps + p.InternationalExtraBps, p.CardFixedCents}
	case MethodWallet:
		return rule{p.WalletPercentBps, p.WalletFixedCents}
	case MethodPSE:
		if p.PSESmallThresholdCents > 0 && totalCents < int64(p.PSESmallThresholdCents) {
			return rule{0, p.PSESmallFixedCents}
		}
		return rule{p.PSEPercentBps, p.PSEFixedCents}
	default:
		return rule{p.CardPercentBps, p.CardFixedCents}
	}
}

// feeOn es la tarifa (sin IVA) y su IVA que la pasarela cobra sobre totalCents, en centavos
// exactos redondeados al centavo (la pasarela liquida en pesos; esto es lo esperado, no lo real:
// lo real llega en la conciliación).
func (p GatewayPlan) feeOn(totalCents int64, m Method) (fee, vat int64) {
	r := p.ruleFor(totalCents, m)
	// fee = total × pct / 10000 + fijo, redondeado al centavo.
	fee = (totalCents*int64(r.percentBps)+BpsScale/2)/BpsScale + int64(r.fixedCents)
	vat = (fee*int64(p.VATBps) + BpsScale/2) / BpsScale
	return fee, vat
}

// netAfter es lo que queda de totalCents después de la tarifa + IVA de la pasarela.
func (p GatewayPlan) netAfter(totalCents int64, m Method) int64 {
	fee, vat := p.feeOn(totalCents, m)
	return totalCents - fee - vat
}

// grossUpRule resuelve, con aritmética entera exacta, el menor total T (en pesos enteros) tal que
// T − (T·pct + fijo)·(1 + IVA) ≥ base:
//
//	T ≥ (base + fijo·(1+IVA)) / (1 − pct·(1+IVA))
//
// Con pct y IVA en bps: T ≥ (base·10⁸ + fijo·V·10⁴) / (10⁸ − pct·V), con V = 10⁴ + IVA.
func grossUpRule(base int64, r rule, vatBps int32) (int64, error) {
	v := big.NewInt(int64(BpsScale + vatBps))
	scale2 := big.NewInt(BpsScale * BpsScale)
	den := new(big.Int).Sub(scale2, new(big.Int).Mul(big.NewInt(int64(r.percentBps)), v))
	if den.Sign() <= 0 {
		return 0, ErrInvalid // una tarifa ≥ 100 % no tiene solución
	}
	num := new(big.Int).Mul(big.NewInt(base), scale2)
	num.Add(num, new(big.Int).Mul(new(big.Int).Mul(big.NewInt(int64(r.fixedCents)), v), big.NewInt(BpsScale)))
	// ceil(num / den)
	q, m := new(big.Int).QuoRem(num, den, new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, ErrInvalid
	}
	return ceilToPeso(q.Int64()), nil
}

// grossUp devuelve el total a cobrar con el medio m. Para PSE resuelve el salto de la regla de
// montos pequeños: por debajo del umbral la tarifa es fija; por encima, porcentual. Siempre
// devuelve un total cuyo neto (con la regla que REALMENTE aplica a ese total) cubre la base.
func (p GatewayPlan) grossUp(base int64, m Method) (int64, error) {
	if base <= 0 {
		return 0, nil
	}
	var total int64
	if m == MethodPSE && p.PSESmallThresholdCents > 0 {
		small, err := grossUpRule(base, rule{0, p.PSESmallFixedCents}, p.VATBps)
		if err != nil {
			return 0, err
		}
		if small < int64(p.PSESmallThresholdCents) {
			total = small
		} else {
			pct, err := grossUpRule(base, rule{p.PSEPercentBps, p.PSEFixedCents}, p.VATBps)
			if err != nil {
				return 0, err
			}
			// Si el total porcentual cayera bajo el umbral, la pasarela aplicaría la tarifa fija y
			// el total fijo ya no alcanza: el mínimo seguro es el propio umbral (por encima de él
			// la tarifa es porcentual y su neto crece con el total).
			total = max(pct, ceilToPeso(int64(p.PSESmallThresholdCents)))
		}
	} else {
		t, err := grossUpRule(base, p.ruleFor(base, m), p.VATBps)
		if err != nil {
			return 0, err
		}
		total = t
	}
	// Red de seguridad ante cualquier redondeo: subir de a un peso hasta que el neto cubra la base.
	for i := 0; i < 100 && p.netAfter(total, m) < base; i++ {
		total += Peso
	}
	if p.netAfter(total, m) < base {
		return 0, ErrInvalid
	}
	return total, nil
}
