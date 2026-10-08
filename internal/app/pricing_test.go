package app_test

import (
	"net/http"
	"testing"
)

// settingsBody es la configuración general por defecto, para cambiar solo lo que la prueba
// necesita.
func settingsBody(overrides map[string]any) map[string]any {
	b := map[string]any{
		"platform_fee_bps": 1000, "promo_platform_fee_bps": 500, "courier_fee_bps": 0,
		"delivery_fee_cents": 0, "gateway_plan_code": "epayco_davivienda", "split_enabled": false,
	}
	for k, v := range overrides {
		b[k] = v
	}
	return b
}

func TestComisionesSoloLasAdministraLaPlataforma(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	root := h.user(superadmin())

	want(t, h.do(http.MethodGet, "/v1/platform/pricing", nil, &admin), http.StatusForbidden)
	want(t, h.do(http.MethodPut, "/v1/platform/pricing/settings", settingsBody(nil), &admin), http.StatusForbidden)
	want(t, h.do(http.MethodPut, platformOrgURL(o, "/pricing"), map[string]any{"platform_fee_bps": 0}, &admin), http.StatusForbidden)

	rec := h.do(http.MethodGet, "/v1/platform/pricing", nil, &root)
	want(t, rec, http.StatusOK)
	ov := jsonMap(t, rec)
	if s := ov["settings"].(map[string]any); s["platform_fee_bps"] != float64(1000) || s["promo_platform_fee_bps"] != float64(500) {
		t.Fatalf("configuración por defecto = %v", s)
	}
	if plans := ov["plans"].([]any); len(plans) != 2 {
		t.Fatalf("planes = %v", plans)
	}

	// El admin de la organización sí ve SUS comisiones (para ver el precio publicado al editar).
	rec = h.do(http.MethodGet, orgURL(o, "/pricing"), nil, &admin)
	want(t, rec, http.StatusOK)
	if r := jsonMap(t, rec); r["platform_fee_bps"] != float64(1000) {
		t.Fatalf("comisiones de la organización = %v", r)
	}
	extrano := h.user()
	want(t, h.do(http.MethodGet, orgURL(o, "/pricing"), nil, &extrano), http.StatusForbidden)
}

func TestLaComisionCambiaElPrecioPublicadoYSeAudita(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	root := h.user(superadmin())
	productID, variantID := seedSimpleProduct(h, admin, o, "Pizza", 20000)
	want(t, h.do(http.MethodPatch, productsURL(o, "/"+productID), map[string]any{"channels": []string{"ecommerce"}}, &admin), http.StatusOK)

	price := func() float64 {
		rec := h.do(http.MethodGet, "/v1/public/organizations/"+o.ID.String()+"/products/"+productID, nil, nil)
		want(t, rec, http.StatusOK)
		return jsonMap(t, rec)["variants"].([]any)[0].(map[string]any)["price_cents"].(float64)
	}
	if got := price(); got != 22000 {
		t.Fatalf("precio publicado = %v, quería 22000 (20000 + 10 %%)", got)
	}

	// Comisión propia de la organización: 15 %.
	rec := h.do(http.MethodPut, platformOrgURL(o, "/pricing"), map[string]any{"platform_fee_bps": 1500}, &root)
	want(t, rec, http.StatusOK)
	if eff := jsonMap(t, rec)["effective"].(map[string]any); eff["platform_fee_bps"] != float64(1500) || eff["promo_platform_fee_bps"] != float64(500) {
		t.Fatalf("efectivo = %v", eff)
	}
	if got := price(); got != 23000 {
		t.Fatalf("precio publicado con 15 %% = %v", got)
	}
	if !contains(auditActions(t, o), "organization.pricing_changed") {
		t.Fatal("el cambio de comisión no quedó auditado")
	}

	// El admin de la organización sigue viendo su precio LOCAL al editar el producto.
	got := jsonMap(t, h.do(http.MethodGet, productsURL(o, "/"+productID), nil, &admin))
	if v := got["variants"].([]any)[0].(map[string]any)["price_cents"]; v != float64(20000) {
		t.Fatalf("precio local = %v", v)
	}

	// Volver a heredar: todo null.
	want(t, h.do(http.MethodPut, platformOrgURL(o, "/pricing"), map[string]any{}, &root), http.StatusOK)
	if got := price(); got != 22000 {
		t.Fatalf("tras heredar = %v", got)
	}

	// Un pedido ya confirmado no cambia aunque cambien las tarifas.
	cliente := h.user()
	want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{"product_id": productID, "variant_id": variantID, "quantity": 1}, &cliente), http.StatusCreated)
	placed := jsonMap(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente))
	if placed["subtotal_cents"] != float64(22000) || placed["subtotal_local_cents"] != float64(20000) || placed["platform_fee_cents"] != float64(2000) {
		t.Fatalf("pedido confirmado = %v", placed)
	}
	want(t, h.do(http.MethodPut, "/v1/platform/pricing/settings", settingsBody(map[string]any{"platform_fee_bps": 3000}), &root), http.StatusOK)
	again := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+placed["id"].(string)), nil, &admin))
	if again["subtotal_cents"] != float64(22000) || again["platform_fee_cents"] != float64(2000) {
		t.Fatalf("el pedido cambió tras cambiar la comisión: %v", again)
	}
}

func TestPromocionDeProducto(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
		"name": "Hamburguesa", "channels": []string{"ecommerce"}, "promo_discount_bps": 2000,
		"variants": []map[string]any{{"name": "Sencilla", "price_cents": 2000000}},
	}, &admin)
	want(t, rec, http.StatusCreated)
	p := jsonMap(t, rec)
	if p["promo_discount_bps"] != float64(2000) {
		t.Fatalf("producto = %v", p)
	}
	// $20.000 local − 20 % del restaurante = $16.000; comisión de promoción 5 % → $16.800. En el
	// feed, el precio regular (sin descuento, con el 10 %) es $22.000.
	feed := jsonMap(t, h.do(http.MethodGet, "/v1/public/products?organization_id="+o.ID.String(), nil, nil))
	item := feed["items"].([]any)[0].(map[string]any)
	if item["min_price_cents"] != float64(1680000) || item["regular_min_price_cents"] != float64(2200000) || item["promo_discount_bps"] != float64(2000) {
		t.Fatalf("feed = %v", item)
	}
	want(t, h.do(http.MethodPatch, productsURL(o, "/"+p["id"].(string)), map[string]any{"promo_discount_bps": 9500}, &admin), http.StatusUnprocessableEntity)

	// En el carrito: precio publicado de promoción y la parte local del restaurante.
	cliente := h.user()
	variantID := p["variants"].([]any)[0].(map[string]any)["id"]
	want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{"product_id": p["id"], "variant_id": variantID, "quantity": 2}, &cliente), http.StatusCreated)
	placed := jsonMap(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente))
	if placed["subtotal_cents"] != float64(3360000) || placed["subtotal_local_cents"] != float64(3200000) || placed["platform_fee_cents"] != float64(160000) {
		t.Fatalf("pedido con promoción = %v", placed)
	}
}

func TestDomicilioYCostoDeTransaccion(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	root := h.user(superadmin())
	// El ejemplo de docs/pagos.md §8: 2 hamburguesas de $20.000 + 1 gaseosa de $4.000, domicilio
	// de $5.000 → base $53.400 → con tarjeta y plan Davivienda, $55.980.
	want(t, h.do(http.MethodPut, "/v1/platform/pricing/settings", settingsBody(map[string]any{"delivery_fee_cents": 500000, "courier_fee_bps": 500}), &root), http.StatusOK)
	hamburguesa, vh := seedSimpleProduct(h, admin, o, "Hamburguesa", 2000000)
	gaseosa, vg := seedSimpleProduct(h, admin, o, "Gaseosa", 400000)
	cliente := h.user()
	want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{"product_id": hamburguesa, "variant_id": vh, "quantity": 2}, &cliente), http.StatusCreated)
	rec := h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{"product_id": gaseosa, "variant_id": vg, "quantity": 1}, &cliente)
	want(t, rec, http.StatusCreated)
	if cart := jsonMap(t, rec); cart["delivery_fee_cents"] != float64(500000) || cart["total_cents"] != float64(5340000) {
		t.Fatalf("el carrito debía mostrar el domicilio: %v", cart)
	}
	placed := jsonMap(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente))
	if placed["platform_fee_cents"] != float64(440000) || placed["courier_fee_cents"] != float64(25000) {
		t.Fatalf("pedido = %v", placed)
	}
	orderID := placed["id"].(string)

	for method, total := range map[string]float64{"card": 5598000, "pse": 5601800, "card_international": 5653600} {
		q := quote(h, o, orderID, cliente, method)
		if q["base_cents"] != float64(5340000) || q["total_cents"] != total {
			t.Errorf("%s: cotización = %v, quería total %v", method, q, total)
		}
	}
	// Con el plan de otros bancos para esta organización: $56.443.
	want(t, h.do(http.MethodPut, platformOrgURL(o, "/pricing"), map[string]any{"gateway_plan_code": "epayco_otros_bancos"}, &root), http.StatusOK)
	if q := quote(h, o, orderID, cliente, "card"); q["total_cents"] != float64(5644300) {
		t.Fatalf("otros bancos = %v", q)
	}
	// Reparto: restaurante $44.000, comisión $4.400, domiciliario $4.750 (5 % de $5.000 a la
	// plataforma).
	split := quote(h, o, orderID, cliente, "card")["split"].(map[string]any)
	if split["organization_cents"] != float64(4400000) || split["platform_fee_cents"] != float64(440000) ||
		split["courier_cents"] != float64(475000) || split["platform_cents"] != float64(465000) {
		t.Fatalf("split = %v", split)
	}
}

func TestSplitConEpayco(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	root := h.user(superadmin())
	want(t, h.do(http.MethodPut, "/v1/platform/pricing/settings", settingsBody(map[string]any{"split_enabled": true}), &root), http.StatusOK)
	want(t, h.do(http.MethodPut, platformOrgURL(o, "/pricing"), map[string]any{"epayco_merchant_id": "123456"}, &root), http.StatusOK)
	cliente := h.user()
	orderID := placeSimpleOrder(h, admin, o, cliente)

	startPayment(h, o, orderID, cliente, "card")
	split, ok := h.epayco.lastSession()["splitPayment"].(map[string]any)
	if !ok {
		t.Fatal("con split activado y receptor configurado, la sesión debía llevar splitPayment")
	}
	receivers := split["receivers"].([]any)
	r := receivers[0].(map[string]any)
	// La parte del restaurante es su precio local: $30 de la gaseosa.
	if len(receivers) != 1 || r["merchantId"] != "123456" || r["amount"] != float64(30) {
		t.Fatalf("splitPayment = %v", split)
	}
}

func TestSimuladorDeComisiones(t *testing.T) {
	h := newHarness(t)
	root := h.user(superadmin())
	rec := h.do(http.MethodPost, "/v1/platform/pricing/simulate", map[string]any{
		"lines": []map[string]any{
			{"local_cents": 2000000, "quantity": 2},
			{"local_cents": 400000, "quantity": 1},
		},
		"delivery_fee_cents": 500000,
	}, &root)
	want(t, rec, http.StatusOK)
	out := jsonMap(t, rec)
	quotes := out["quotes"].([]any)
	if len(quotes) != 4 {
		t.Fatalf("cotizaciones = %v", quotes)
	}
	if card := quotes[0].(map[string]any); card["method"] != "card" || card["total_cents"] != float64(5598000) {
		t.Fatalf("tarjeta = %v", card)
	}
	want(t, h.do(http.MethodPost, "/v1/platform/pricing/simulate", map[string]any{"lines": []any{}}, &root), http.StatusUnprocessableEntity)
}
