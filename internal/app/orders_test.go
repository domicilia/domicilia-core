package app_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func cartURL(o organization, suffix string) string { return orgURL(o, "/cart"+suffix) }
func ordersOrgURL(o organization, suffix string) string {
	return orgURL(o, "/orders"+suffix)
}

// seedSimpleProduct crea un producto de una sola variante (sin modificadores) y devuelve sus ids.
func seedSimpleProduct(h *harness, admin person, o organization, name string, priceCents int) (productID, variantID string) {
	h.t.Helper()
	rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
		"name":     name,
		"variants": []map[string]any{{"name": "Regular", "price_cents": priceCents}},
	}, &admin)
	want(h.t, rec, http.StatusCreated)
	p := jsonMap(h.t, rec)
	v := p["variants"].([]any)[0].(map[string]any)
	return p["id"].(string), v["id"].(string)
}

func TestElCarritoEsUnPedidoEnDraft(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	rec := h.do(http.MethodGet, cartURL(o, ""), nil, &cliente)
	want(t, rec, http.StatusOK)
	cart := jsonMap(t, rec)
	if cart["status"] != "draft" || cart["customer_id"] != cliente.ID.String() {
		t.Fatalf("carrito = %v", cart)
	}
	if v := cart["items"].([]any); len(v) != 0 {
		t.Fatalf("un carrito recién creado debe empezar vacío: %v", v)
	}

	// Verlo de nuevo devuelve el MISMO carrito, no uno nuevo.
	rec2 := h.do(http.MethodGet, cartURL(o, ""), nil, &cliente)
	if jsonMap(t, rec2)["id"] != cart["id"] {
		t.Fatal("ver el carrito dos veces creó dos carritos distintos")
	}

	// Un cliente distinto tiene su propio carrito, aislado del anterior.
	otro := h.user()
	rec3 := h.do(http.MethodGet, cartURL(o, ""), nil, &otro)
	if jsonMap(t, rec3)["id"] == cart["id"] {
		t.Fatal("dos clientes distintos comparten el mismo carrito")
	}
}

func TestAgregarAlCarrito(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	t.Run("agrega la línea con el precio del catálogo, no el que mande el cliente", func(t *testing.T) {
		rec := h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 2,
		}, &cliente)
		want(t, rec, http.StatusCreated)
		cart := jsonMap(t, rec)
		items := cart["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items = %v", items)
		}
		line := items[0].(map[string]any)
		if line["unit_price_cents"] != float64(3000) || line["quantity"] != float64(2) || line["line_total_cents"] != float64(6000) {
			t.Fatalf("línea = %v", line)
		}
		if cart["subtotal_cents"] != float64(6000) || cart["total_cents"] != float64(6000) {
			t.Fatalf("carrito = %v", cart)
		}
	})

	t.Run("un product_id o variant_id que no existe es 422, no 500", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		productID, _ := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": uuid.NewString(), "variant_id": uuid.NewString(), "quantity": 1,
		}, &cliente), http.StatusUnprocessableEntity)

		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": uuid.NewString(), "quantity": 1,
		}, &cliente), http.StatusUnprocessableEntity)
	})

	t.Run("una variante de OTRO producto no aplica, aunque exista", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		productID, _ := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)
		_, otraVariantID := seedSimpleProduct(h, admin, o, "Pizza", 20000)

		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": otraVariantID, "quantity": 1,
		}, &cliente), http.StatusUnprocessableEntity)
	})

	t.Run("un producto de OTRA organización no existe para esta", func(t *testing.T) {
		h := newHarness(t)
		_, o := orgAdmin(h, "Acme")
		adminOtra, otra := orgAdmin(h, "Otra")
		cliente := h.user()
		productID, variantID := seedSimpleProduct(h, adminOtra, otra, "Pizza", 20000)

		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1,
		}, &cliente), http.StatusUnprocessableEntity)
	})
}

func TestModificadoresObligatoriosEnElCarrito(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()

	group := jsonMap(t, h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
		"name": "Tamaño", "selection_type": "single", "min_select": 1, "max_select": 1,
		"options": []map[string]any{{"name": "Grande", "price_delta_cents": 2000}},
	}, &admin))
	groupID := group["id"].(string)
	optionID := group["options"].([]any)[0].(map[string]any)["id"].(string)

	p := jsonMap(t, h.do(http.MethodPost, productsURL(o, ""), map[string]any{
		"name":               "Café",
		"variants":           []map[string]any{{"name": "Regular", "price_cents": 5000}},
		"modifier_group_ids": []string{groupID},
	}, &admin))
	productID := p["id"].(string)
	variantID := p["variants"].([]any)[0].(map[string]any)["id"].(string)

	t.Run("sin elegir el grupo obligatorio, 422", func(t *testing.T) {
		rec := h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1,
		}, &cliente)
		want(t, rec, http.StatusUnprocessableEntity)
	})

	t.Run("con la opción elegida, el precio incluye el recargo", func(t *testing.T) {
		rec := h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1, "option_ids": []string{optionID},
		}, &cliente)
		want(t, rec, http.StatusCreated)
		line := jsonMap(t, rec)["items"].([]any)[0].(map[string]any)
		if line["unit_total_cents"] != float64(7000) || line["line_total_cents"] != float64(7000) {
			t.Fatalf("línea = %v", line)
		}
		mods := line["modifiers"].([]any)
		if len(mods) != 1 || mods[0].(map[string]any)["option_id"] != optionID {
			t.Fatalf("modifiers = %v", mods)
		}
	})

	t.Run("una opción que no pertenece a ningún grupo del producto no aplica", func(t *testing.T) {
		otroGrupo := jsonMap(t, h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
			"name": "Ajeno", "selection_type": "single", "max_select": 1,
			"options": []map[string]any{{"name": "X", "price_delta_cents": 0}},
		}, &admin))
		ajenaID := otroGrupo["options"].([]any)[0].(map[string]any)["id"].(string)

		rec := h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1, "option_ids": []string{ajenaID},
		}, &cliente)
		want(t, rec, http.StatusUnprocessableEntity)
	})
}

func TestCambiarCantidadYQuitarLinea(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	rec := h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
		"product_id": productID, "variant_id": variantID, "quantity": 1,
	}, &cliente)
	cart := jsonMap(t, rec)
	itemID := cart["items"].([]any)[0].(map[string]any)["id"].(string)

	t.Run("cambiar cantidad recalcula la línea y el subtotal", func(t *testing.T) {
		rec := h.do(http.MethodPatch, cartURL(o, "/items/"+itemID), map[string]any{"quantity": 3}, &cliente)
		want(t, rec, http.StatusOK)
		out := jsonMap(t, rec)
		line := out["items"].([]any)[0].(map[string]any)
		if line["quantity"] != float64(3) || line["line_total_cents"] != float64(9000) || out["subtotal_cents"] != float64(9000) {
			t.Fatalf("carrito = %v", out)
		}
	})

	t.Run("un extraño no toca la línea de otro cliente", func(t *testing.T) {
		otro := h.user()
		want(t, h.do(http.MethodPatch, cartURL(o, "/items/"+itemID), map[string]any{"quantity": 1}, &otro), http.StatusNotFound)
		want(t, h.do(http.MethodDelete, cartURL(o, "/items/"+itemID), nil, &otro), http.StatusNotFound)
	})

	t.Run("quitar la línea deja el carrito en 0", func(t *testing.T) {
		rec := h.do(http.MethodDelete, cartURL(o, "/items/"+itemID), nil, &cliente)
		want(t, rec, http.StatusOK)
		out := jsonMap(t, rec)
		if items := out["items"].([]any); len(items) != 0 {
			t.Fatalf("items = %v", items)
		}
		if out["subtotal_cents"] != float64(0) {
			t.Fatalf("subtotal_cents = %v", out["subtotal_cents"])
		}
	})
}

func TestConfirmarElCarrito(t *testing.T) {
	t.Run("un carrito vacío no se puede confirmar", func(t *testing.T) {
		h := newHarness(t)
		_, o := orgAdmin(h, "Acme")
		cliente := h.user()
		want(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente), http.StatusUnprocessableEntity)
	})

	t.Run("confirmar congela el precio: un cambio de menú después no lo altera", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 2,
		}, &cliente), http.StatusCreated)

		rec := h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente)
		want(t, rec, http.StatusOK)
		placed := jsonMap(t, rec)
		if placed["status"] != "placed" || placed["placed_at"] == nil {
			t.Fatalf("pedido confirmado = %v", placed)
		}
		orderID := placed["id"].(string)

		// El negocio sube el precio del producto DESPUÉS de confirmado.
		want(t, h.do(http.MethodPatch, productsURL(o, "/"+productID), map[string]any{
			"variants": []map[string]any{{"id": variantID, "name": "Regular", "price_cents": 99000, "is_default": true}},
		}, &admin), http.StatusOK)

		rec2 := h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin)
		want(t, rec2, http.StatusOK)
		out := jsonMap(t, rec2)
		if out["subtotal_cents"] != float64(6000) {
			t.Fatalf("el pedido cambió de precio tras editar el menú: subtotal_cents = %v", out["subtotal_cents"])
		}

		// Y un nuevo GetCart abre un carrito NUEVO, no reutiliza el ya confirmado.
		rec3 := h.do(http.MethodGet, cartURL(o, ""), nil, &cliente)
		if jsonMap(t, rec3)["id"] == orderID {
			t.Fatal("el carrito nuevo reutilizó el id de un pedido ya confirmado")
		}
	})
}

func TestMaquinaDeEstadosDelPedido(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	empleado := h.user()
	h.join(empleado, o, "employee")
	cliente := h.user()
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
		"product_id": productID, "variant_id": variantID, "quantity": 1,
	}, &cliente), http.StatusCreated)
	placed := jsonMap(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente))
	orderID := placed["id"].(string)

	// Aceptar antes de pagar no aplica: sigue en placed, no confirmed.
	want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/accept"), nil, &empleado), http.StatusConflict)

	// El pago de verdad pasa por internal/payments: iniciarlo y que la pasarela lo confirme por
	// su webhook — no por una ruta que "simula" el pago.
	pay := jsonMap(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente))
	paymentID := pay["id"].(string)
	rec := h.postPaymentWebhook(paymentID, "ref-001", "tx-001", "30.00", "Aceptada", nil)
	want(t, rec, http.StatusOK)
	if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "confirmed" {
		t.Fatalf("estado tras el webhook = %v, quería confirmed", got)
	}
	// El mismo evento reentregado (la pasarela promete "al menos una vez") no rompe nada.
	want(t, h.postPaymentWebhook(paymentID, "ref-001", "tx-001", "30.00", "Aceptada", nil), http.StatusOK)

	want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/accept"), nil, &empleado), http.StatusOK)
	want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/start-preparing"), nil, &empleado), http.StatusOK)
	// Ya en preparación: cancelar (cliente o negocio) ya no aplica — hay comida hecha.
	want(t, h.do(http.MethodPost, "/v1/orders/"+orderID+"/cancel", nil, &cliente), http.StatusConflict)
	want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/cancel"), nil, &empleado), http.StatusConflict)

	want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/dispatch"), nil, &empleado), http.StatusOK)
	deliveredRec := h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/mark-delivered"), nil, &empleado)
	want(t, deliveredRec, http.StatusOK)
	if jsonMap(t, deliveredRec)["status"] != "delivered" {
		t.Fatalf("estado final = %v", jsonMap(t, deliveredRec)["status"])
	}
}

func TestAislamientoYPermisosDePedidos(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	adminOtra, otra := orgAdmin(h, "Otra")
	cliente, extrano := h.user(), h.user()
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
		"product_id": productID, "variant_id": variantID, "quantity": 1,
	}, &cliente), http.StatusCreated)
	placed := jsonMap(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente))
	orderID := placed["id"].(string)

	// El staff de OTRA organización no ve el pedido de esta.
	want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &adminOtra), http.StatusForbidden)
	want(t, h.do(http.MethodGet, ordersOrgURL(otra, "/"+orderID), nil, &adminOtra), http.StatusNotFound)

	// Un cliente que no es el dueño no ve el pedido por /v1/orders/{id} — 404, no 403.
	want(t, h.do(http.MethodGet, "/v1/orders/"+orderID, nil, &extrano), http.StatusNotFound)
	want(t, h.do(http.MethodPost, "/v1/orders/"+orderID+"/cancel", nil, &extrano), http.StatusNotFound)

	// El dueño sí lo ve por su ruta propia.
	want(t, h.do(http.MethodGet, "/v1/orders/"+orderID, nil, &cliente), http.StatusOK)

	// El carrito (draft) de alguien no aparece en el listado de "mis pedidos" ni es accesible
	// por /v1/orders/{id} de nadie más (ver TestElCarritoEsUnPedidoEnDraft para su propio acceso).
	cart := jsonMap(t, h.do(http.MethodGet, cartURL(o, ""), nil, &cliente))
	want(t, h.do(http.MethodGet, "/v1/orders/"+cart["id"].(string), nil, &cliente), http.StatusNotFound)
}

func TestMisPedidos(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	adminOtra, otra := orgAdmin(h, "Otra")
	cliente := h.user()

	pID, vID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)
	want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{"product_id": pID, "variant_id": vID, "quantity": 1}, &cliente), http.StatusCreated)
	h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente)

	pID2, vID2 := seedSimpleProduct(h, adminOtra, otra, "Pizza", 20000)
	want(t, h.do(http.MethodPost, cartURL(otra, "/items"), map[string]any{"product_id": pID2, "variant_id": vID2, "quantity": 1}, &cliente), http.StatusCreated)
	h.do(http.MethodPost, cartURL(otra, "/place"), nil, &cliente)

	// Deja un carrito sin confirmar en una tercera organización: no debe aparecer en "mis pedidos".
	_, o3 := orgAdmin(h, "Tercera")
	h.do(http.MethodGet, cartURL(o3, ""), nil, &cliente)

	rec := h.do(http.MethodGet, "/v1/orders/mine", nil, &cliente)
	want(t, rec, http.StatusOK)
	body := jsonMap(t, rec)
	if body["total"] != float64(2) {
		t.Fatalf("mis pedidos = %v, quería 2 (cruzando organizaciones, sin el carrito sin confirmar)", body)
	}
}

func TestMisCarritos(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	adminOtra, otra := orgAdmin(h, "Otra")
	cliente := h.user()

	// Un carrito vacío (solo abrir el menú) no debe aparecer.
	_, o3 := orgAdmin(h, "Tercera")
	h.do(http.MethodGet, cartURL(o3, ""), nil, &cliente)

	pID, vID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)
	want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{"product_id": pID, "variant_id": vID, "quantity": 1}, &cliente), http.StatusCreated)

	pID2, vID2 := seedSimpleProduct(h, adminOtra, otra, "Pizza", 20000)
	want(t, h.do(http.MethodPost, cartURL(otra, "/items"), map[string]any{"product_id": pID2, "variant_id": vID2, "quantity": 2}, &cliente), http.StatusCreated)

	rec := h.do(http.MethodGet, "/v1/carts/mine", nil, &cliente)
	want(t, rec, http.StatusOK)
	carts := decode[[]map[string]any](t, rec)
	if len(carts) != 2 {
		t.Fatalf("mis carritos = %v, quería 2 (el vacío de Tercera no cuenta)", carts)
	}
	byOrg := map[string]map[string]any{}
	for _, c := range carts {
		byOrg[c["organization_name"].(string)] = c
	}
	if byOrg["Acme"]["item_count"] != float64(1) || byOrg["Acme"]["total_cents"] != float64(3000) {
		t.Fatalf("carrito de Acme = %v", byOrg["Acme"])
	}
	if byOrg["Otra"]["item_count"] != float64(1) || byOrg["Otra"]["total_cents"] != float64(40000) {
		t.Fatalf("carrito de Otra = %v", byOrg["Otra"])
	}

	// Confirmar uno de los carritos lo saca de "mis carritos" (ya no es un borrador).
	want(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente), http.StatusOK)
	carts = decode[[]map[string]any](t, h.do(http.MethodGet, "/v1/carts/mine", nil, &cliente))
	if len(carts) != 1 || carts[0]["organization_name"] != "Otra" {
		t.Fatalf("mis carritos tras confirmar uno = %v", carts)
	}

	// Aislado por cliente: otro cliente no ve estos carritos.
	otroCliente := h.user()
	carts = decode[[]map[string]any](t, h.do(http.MethodGet, "/v1/carts/mine", nil, &otroCliente))
	if len(carts) != 0 {
		t.Fatalf("otro cliente vio carritos ajenos: %v", carts)
	}
}
