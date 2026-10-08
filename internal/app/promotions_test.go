package app_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

func promotionsURL(o organization, suffix string) string { return orgURL(o, "/promotions"+suffix) }

// newPromoReq arma el cuerpo mínimo para crear una promoción.
func newPromoReq(code, discountType string, value int32, extra map[string]any) map[string]any {
	req := map[string]any{"code": code, "discount_type": discountType, "value": value}
	for k, v := range extra {
		req[k] = v
	}
	return req
}

func TestCrearYListarPromociones(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	empleado := h.user()
	h.join(empleado, o, "employee")

	rec := h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("BIENVENIDA10", "percent", 10, nil), &admin)
	want(t, rec, http.StatusCreated)
	promo := jsonMap(t, rec)
	if promo["code"] != "BIENVENIDA10" || promo["discount_type"] != "percent" || promo["value"] != float64(10) ||
		promo["is_active"] != true || promo["min_order_cents"] != float64(0) {
		t.Fatalf("promoción creada = %v", promo)
	}

	rec2 := h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("enviogratis", "fixed", 3000, nil), &admin)
	want(t, rec2, http.StatusCreated)

	t.Run("org.promotions.read no es de employee: no administra mercadeo", func(t *testing.T) {
		want(t, h.do(http.MethodGet, promotionsURL(o, ""), nil, &empleado), http.StatusForbidden)
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("X", "fixed", 100, nil), &empleado), http.StatusForbidden)
	})

	t.Run("listar devuelve ambas", func(t *testing.T) {
		rec := h.do(http.MethodGet, promotionsURL(o, ""), nil, &admin)
		want(t, rec, http.StatusOK)
		rows := decode[[]map[string]any](t, rec)
		if len(rows) != 2 {
			t.Fatalf("promociones = %v", rows)
		}
	})

	t.Run("un código repetido (sin distinguir mayúsculas) es 409", func(t *testing.T) {
		rec := h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("bienvenida10", "fixed", 500, nil), &admin)
		want(t, rec, http.StatusConflict)
	})

	t.Run("un descuento percent fuera de 1-100 es 422", func(t *testing.T) {
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("MALO", "percent", 150, nil), &admin), http.StatusUnprocessableEntity)
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("MALO2", "percent", 0, nil), &admin), http.StatusUnprocessableEntity)
	})

	t.Run("starts_at debe ser anterior a ends_at", func(t *testing.T) {
		now := time.Now().UTC()
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("FECHAS", "fixed", 100, map[string]any{
			"starts_at": now.Add(2 * time.Hour), "ends_at": now,
		}), &admin), http.StatusUnprocessableEntity)
	})

	t.Run("una organización ajena no ve ni administra estas promociones", func(t *testing.T) {
		adminOtra, otra := orgAdmin(h, "Otra")
		want(t, h.do(http.MethodGet, promotionsURL(o, "/"+promo["id"].(string)), nil, &adminOtra), http.StatusForbidden)
		want(t, h.do(http.MethodGet, promotionsURL(otra, "/"+promo["id"].(string)), nil, &adminOtra), http.StatusNotFound)
	})

	t.Run("org.promotions.manage SÍ es delegable", func(t *testing.T) {
		crearRol(t, h, admin, o, "mercadeo", "org.promotions.manage", "org.promotions.read")
		mercadeo := h.user()
		h.join(mercadeo, o, "mercadeo")
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("DELEGADO", "fixed", 100, nil), &mercadeo), http.StatusCreated)
	})
}

func TestActualizarUnaPromocion(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	maxUses := 5
	promo := jsonMap(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("TEMPORAL", "percent", 20, map[string]any{
		"max_uses": maxUses, "ends_at": time.Now().Add(24 * time.Hour).UTC(),
	}), &admin))
	id := promo["id"].(string)

	t.Run("cambiar solo el valor conserva lo demás", func(t *testing.T) {
		rec := h.do(http.MethodPatch, promotionsURL(o, "/"+id), map[string]any{"value": 30}, &admin)
		want(t, rec, http.StatusOK)
		out := jsonMap(t, rec)
		if out["value"] != float64(30) || out["max_uses"] != float64(maxUses) || out["ends_at"] == nil {
			t.Fatalf("promoción tras el patch = %v", out)
		}
	})

	t.Run("poner ends_at y max_uses en null los deja sin límite", func(t *testing.T) {
		rec := h.do(http.MethodPatch, promotionsURL(o, "/"+id), map[string]any{"ends_at": nil, "max_uses": nil}, &admin)
		want(t, rec, http.StatusOK)
		out := jsonMap(t, rec)
		if out["ends_at"] != nil || out["max_uses"] != nil {
			t.Fatalf("promoción tras dejar sin límite = %v", out)
		}
	})

	t.Run("desactivarla la saca de las búsquedas por código", func(t *testing.T) {
		rec := h.do(http.MethodPatch, promotionsURL(o, "/"+id), map[string]any{"is_active": false}, &admin)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["is_active"] != false {
			t.Fatal("no quedó desactivada")
		}
		// Sigue existiendo (nunca se borra): Get la sigue mostrando.
		want(t, h.do(http.MethodGet, promotionsURL(o, "/"+id), nil, &admin), http.StatusOK)
		// Y libera el código: se puede recrear con el mismo texto.
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("TEMPORAL", "fixed", 100, nil), &admin), http.StatusCreated)
	})

	t.Run("una promoción que no existe es 404", func(t *testing.T) {
		want(t, h.do(http.MethodPatch, promotionsURL(o, "/"+uuid.NewString()), map[string]any{"value": 1}, &admin), http.StatusNotFound)
	})
}

// aplicarCodigo agrega un producto al carrito de cliente y aplica code; devuelve el carrito resultante.
func aplicarCodigo(h *harness, o organization, cliente person, productID, variantID string, quantity int, code string) map[string]any {
	h.t.Helper()
	want(h.t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
		"product_id": productID, "variant_id": variantID, "quantity": quantity,
	}, &cliente), http.StatusCreated)
	rec := h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": code}, &cliente)
	want(h.t, rec, http.StatusOK)
	return jsonMap(h.t, rec)
}

func TestAplicarYQuitarUnCodigoDelCarrito(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)
	want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("DIEZ", "percent", 10, nil), &admin), http.StatusCreated)
	want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("MIL", "fixed", 1000, nil), &admin), http.StatusCreated)

	cart := aplicarCodigo(h, o, cliente, productID, variantID, 1, "diez") // sin distinguir mayúsculas
	if cart["discount_cents"] != float64(330) || cart["total_cents"] != float64(2970) || cart["promotion_id"] == nil {
		t.Fatalf("carrito con DIEZ = %v", cart)
	}

	t.Run("aplicar OTRO código reemplaza el descuento, nunca lo acumula", func(t *testing.T) {
		rec := h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": "MIL"}, &cliente)
		want(t, rec, http.StatusOK)
		out := jsonMap(t, rec)
		if out["discount_cents"] != float64(1000) || out["total_cents"] != float64(2300) {
			t.Fatalf("carrito con MIL = %v", out)
		}
	})

	t.Run("editar el carrito borra el descuento aplicado", func(t *testing.T) {
		rec := h.do(http.MethodPatch, cartURL(o, "/items/"+cart["items"].([]any)[0].(map[string]any)["id"].(string)), map[string]any{"quantity": 2}, &cliente)
		want(t, rec, http.StatusOK)
		out := jsonMap(t, rec)
		if out["discount_cents"] != float64(0) || out["promotion_id"] != nil || out["total_cents"] != float64(6600) {
			t.Fatalf("carrito tras editar líneas = %v", out)
		}
	})

	t.Run("quitar el código deja el total sin descuento", func(t *testing.T) {
		rec := h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": "DIEZ"}, &cliente)
		want(t, rec, http.StatusOK)
		applied := jsonMap(t, rec)
		if applied["discount_cents"] == float64(0) {
			t.Fatalf("el código no se aplicó: %v", applied)
		}

		rec2 := h.do(http.MethodDelete, cartURL(o, "/promotion"), nil, &cliente)
		want(t, rec2, http.StatusOK)
		out := jsonMap(t, rec2)
		if out["discount_cents"] != float64(0) || out["promotion_id"] != nil {
			t.Fatalf("carrito tras quitar el código = %v", out)
		}

		// Idempotente: quitarlo de nuevo no es un error.
		want(t, h.do(http.MethodDelete, cartURL(o, "/promotion"), nil, &cliente), http.StatusOK)
	})
}

func TestReglasDeElegibilidadDeUnCodigo(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("MINIMO", "fixed", 500, map[string]any{
		"min_order_cents": 10000,
	}), &admin), http.StatusCreated)
	want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("VENCIDO", "fixed", 500, map[string]any{
		"ends_at": time.Now().Add(-time.Hour).UTC(),
	}), &admin), http.StatusCreated)
	want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("FUTURO", "fixed", 500, map[string]any{
		"starts_at": time.Now().Add(time.Hour).UTC(),
	}), &admin), http.StatusCreated)
	want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("INACTIVO", "fixed", 500, nil), &admin), http.StatusCreated)

	tests := []struct {
		name string
		code string
	}{
		{"código inexistente", "NOEXISTE"},
		{"por debajo del mínimo de compra", "MINIMO"},
		{"ya venció", "VENCIDO"},
		{"todavía no empieza", "FUTURO"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cliente := h.user()
			want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
				"product_id": productID, "variant_id": variantID, "quantity": 1,
			}, &cliente), http.StatusCreated)
			want(t, h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": tc.code}, &cliente), http.StatusUnprocessableEntity)
		})
	}

	t.Run("un carrito vacío no admite código", func(t *testing.T) {
		cliente := h.user()
		want(t, h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": "MINIMO"}, &cliente), http.StatusUnprocessableEntity)
	})

	t.Run("desactivada explícitamente tampoco aplica", func(t *testing.T) {
		list := decode[[]map[string]any](t, h.do(http.MethodGet, promotionsURL(o, ""), nil, &admin))
		var inactivoID string
		for _, p := range list {
			if p["code"] == "INACTIVO" {
				inactivoID = p["id"].(string)
			}
		}
		want(t, h.do(http.MethodPatch, promotionsURL(o, "/"+inactivoID), map[string]any{"is_active": false}, &admin), http.StatusOK)

		cliente := h.user()
		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1,
		}, &cliente), http.StatusCreated)
		want(t, h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": "INACTIVO"}, &cliente), http.StatusUnprocessableEntity)
	})

	t.Run("un código de OTRA organización no existe para esta", func(t *testing.T) {
		adminOtra, otra := orgAdmin(h, "Otra")
		want(t, h.do(http.MethodPost, promotionsURL(otra, ""), newPromoReq("AJENO", "fixed", 100, nil), &adminOtra), http.StatusCreated)

		cliente := h.user()
		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1,
		}, &cliente), http.StatusCreated)
		want(t, h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": "AJENO"}, &cliente), http.StatusUnprocessableEntity)
	})
}

func TestLimitesDeUsoDeUnaPromocion(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	t.Run("max_uses se cuenta contra pedidos confirmados de verdad, no contra carritos", func(t *testing.T) {
		one := 1
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("LIMITADO", "fixed", 500, map[string]any{
			"max_uses": one,
		}), &admin), http.StatusCreated)

		primero := h.user()
		cart := aplicarCodigo(h, o, primero, productID, variantID, 1, "LIMITADO")
		if cart["promotion_id"] == nil {
			t.Fatalf("el primer cliente no pudo aplicar el código: %v", cart)
		}
		// Confirmarlo (place) es lo que lo vuelve un canje REAL: ya no es un borrador.
		want(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &primero), http.StatusOK)

		// Un segundo cliente ya no puede: el cupo (1) está agotado por un pedido real.
		segundo := h.user()
		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1,
		}, &segundo), http.StatusCreated)
		want(t, h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": "LIMITADO"}, &segundo), http.StatusUnprocessableEntity)
	})

	t.Run("per_customer_limit es por cliente, no global", func(t *testing.T) {
		one := 1
		want(t, h.do(http.MethodPost, promotionsURL(o, ""), newPromoReq("UNAVEZ", "fixed", 500, map[string]any{
			"per_customer_limit": one,
		}), &admin), http.StatusCreated)

		cliente := h.user()
		aplicarCodigo(h, o, cliente, productID, variantID, 1, "UNAVEZ")
		want(t, h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente), http.StatusOK)

		// El MISMO cliente, en un carrito nuevo, ya no puede volver a usarlo.
		want(t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
			"product_id": productID, "variant_id": variantID, "quantity": 1,
		}, &cliente), http.StatusCreated)
		want(t, h.do(http.MethodPost, cartURL(o, "/promotion"), map[string]any{"code": "UNAVEZ"}, &cliente), http.StatusUnprocessableEntity)

		// Pero OTRO cliente sí puede: el límite es por cliente.
		otro := h.user()
		cart := aplicarCodigo(h, o, otro, productID, variantID, 1, "UNAVEZ")
		if cart["promotion_id"] == nil {
			t.Fatal("otro cliente debía poder usar el código")
		}
	})
}
