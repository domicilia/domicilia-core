package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func publicFeedURL(suffix string) string { return "/v1/public/products" + suffix }
func publicProductDetailURL(o organization, productID string) string {
	return "/v1/public/organizations/" + o.ID.String() + "/products/" + productID
}

// seedCategoryProduct crea una categoría y un producto YA PUBLICADO ("ecommerce") dentro de
// ella — para probar el filtro por nombre de categoría del feed. Ver TestPublicarUnProducto para
// el caso de uno sin publicar.
func seedCategoryProduct(h *harness, admin person, o organization, categoryName, productName string, priceCents int) (productID string) {
	h.t.Helper()
	cat := jsonMap(h.t, h.do(http.MethodPost, categoriesURL(o, ""), map[string]any{"name": categoryName}, &admin))
	rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
		"category_id": cat["id"],
		"name":        productName,
		"variants":    []map[string]any{{"name": "Regular", "price_cents": priceCents}},
		"channels":    []string{"ecommerce"},
	}, &admin)
	want(h.t, rec, http.StatusCreated)
	return jsonMap(h.t, rec)["id"].(string)
}

func TestFeedPublicoDeProductos(t *testing.T) {
	h := newHarness(t)
	admin, pizzeria := orgAdmin(h, "Pizzería")
	adminSushi, sushi := orgAdmin(h, "Sushi House")
	seedCategoryProduct(h, admin, pizzeria, "Pizzas", "Margarita", 25000)
	seedCategoryProduct(h, adminSushi, sushi, "Rolls", "California Roll", 18000)

	t.Run("cruza organizaciones a propósito", func(t *testing.T) {
		rec := h.do(http.MethodGet, publicFeedURL(""), nil, nil)
		want(t, rec, http.StatusOK)
		body := jsonMap(t, rec)
		items := body["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("items = %v, quería 2 (una por organización)", items)
		}
		names := map[string]bool{}
		for _, it := range items {
			names[it.(map[string]any)["organization_name"].(string)] = true
			if it.(map[string]any)["name"] == "Margarita" && it.(map[string]any)["min_price_cents"] != float64(25000) {
				t.Fatalf("min_price_cents = %v", it)
			}
		}
		if !names["Pizzería"] || !names["Sushi House"] {
			t.Fatalf("organizaciones en el feed = %v", names)
		}
	})

	t.Run("organization_id acota al menú de un solo restaurante", func(t *testing.T) {
		rec := h.do(http.MethodGet, publicFeedURL("?organization_id="+pizzeria.ID.String()), nil, nil)
		want(t, rec, http.StatusOK)
		items := jsonMap(t, rec)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["organization_name"] != "Pizzería" {
			t.Fatalf("items = %v", items)
		}
	})

	t.Run("organization_slug hace lo mismo que organization_id — la vitrina solo conoce el slug", func(t *testing.T) {
		rec := h.do(http.MethodGet, publicFeedURL("?organization_slug="+pizzeria.Slug), nil, nil)
		want(t, rec, http.StatusOK)
		items := jsonMap(t, rec)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["organization_name"] != "Pizzería" {
			t.Fatalf("items = %v", items)
		}
	})

	t.Run("q busca por nombre de producto", func(t *testing.T) {
		rec := h.do(http.MethodGet, publicFeedURL("?q=california"), nil, nil)
		want(t, rec, http.StatusOK)
		items := jsonMap(t, rec)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["name"] != "California Roll" {
			t.Fatalf("items = %v", items)
		}
	})

	t.Run("category filtra por el nombre, sin distinguir mayúsculas", func(t *testing.T) {
		rec := h.do(http.MethodGet, publicFeedURL("?category=pizzas"), nil, nil)
		want(t, rec, http.StatusOK)
		items := jsonMap(t, rec)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["name"] != "Margarita" {
			t.Fatalf("items = %v", items)
		}
	})

	t.Run("una organización suspendida desaparece del feed", func(t *testing.T) {
		_, err := pool.Exec(context.Background(), `UPDATE organizations SET status = 'suspended' WHERE id = $1`, sushi.ID)
		must(t, err)
		rec := h.do(http.MethodGet, publicFeedURL(""), nil, nil)
		want(t, rec, http.StatusOK)
		items := jsonMap(t, rec)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["organization_name"] != "Pizzería" {
			t.Fatalf("items = %v, la organización suspendida no debía aparecer", items)
		}
	})

	t.Run("un producto desactivado desaparece del feed", func(t *testing.T) {
		pID := seedCategoryProduct(h, admin, pizzeria, "Pizzas", "Hawaiana", 27000)
		want(t, h.do(http.MethodPatch, productsURL(pizzeria, "/"+pID), map[string]any{"is_active": false}, &admin), http.StatusOK)

		rec := h.do(http.MethodGet, publicFeedURL("?organization_id="+pizzeria.ID.String()), nil, nil)
		items := jsonMap(t, rec)["items"].([]any)
		for _, it := range items {
			if it.(map[string]any)["name"] == "Hawaiana" {
				t.Fatalf("un producto desactivado apareció en el feed: %v", items)
			}
		}
	})
}

func TestDetallePublicoDeUnProducto(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")

	group := jsonMap(t, h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
		"name": "Tamaño", "selection_type": "single", "min_select": 1, "max_select": 1,
		"options": []map[string]any{{"name": "Grande", "price_delta_cents": 2000}},
	}, &admin))
	p := jsonMap(t, h.do(http.MethodPost, productsURL(o, ""), map[string]any{
		"name":               "Café",
		"variants":           []map[string]any{{"name": "Regular", "price_cents": 5000}},
		"modifier_group_ids": []string{group["id"].(string)},
	}, &admin))
	productID := p["id"].(string)

	rec := h.do(http.MethodGet, publicProductDetailURL(o, productID), nil, nil)
	want(t, rec, http.StatusOK)
	detail := jsonMap(t, rec)
	if detail["name"] != "Café" {
		t.Fatalf("detalle = %v", detail)
	}
	groups := detail["modifier_groups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["name"] != "Tamaño" {
		t.Fatalf("modifier_groups = %v", groups)
	}
	variants := detail["variants"].([]any)
	if len(variants) != 1 {
		t.Fatalf("variants = %v", variants)
	}

	t.Run("un producto desactivado no existe para el público", func(t *testing.T) {
		want(t, h.do(http.MethodPatch, productsURL(o, "/"+productID), map[string]any{"is_active": false}, &admin), http.StatusOK)
		want(t, h.do(http.MethodGet, publicProductDetailURL(o, productID), nil, nil), http.StatusNotFound)
	})

	t.Run("una organización que no existe, 404", func(t *testing.T) {
		fake := organization{ID: uuid.New()}
		want(t, h.do(http.MethodGet, publicProductDetailURL(fake, productID), nil, nil), http.StatusNotFound)
	})
}

// TestPublicarUnProducto cubre el interruptor real de "publicar": un producto activo no aparece
// en el feed público solo por existir, la organización tiene que elegirlo a propósito.
func TestPublicarUnProducto(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")

	t.Run("sin channels, no se publica en ningún lado", func(t *testing.T) {
		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name":     "Sin publicar",
			"variants": []map[string]any{{"name": "Regular", "price_cents": 10000}},
		}, &admin)
		want(t, rec, http.StatusCreated)
		p := jsonMap(t, rec)
		if channels, _ := p["channels"].([]any); len(channels) != 0 {
			t.Fatalf("channels = %v, quería vacío por omisión", p["channels"])
		}
		productID := p["id"].(string)

		feed := jsonMap(t, h.do(http.MethodGet, publicFeedURL("?organization_id="+o.ID.String()), nil, nil))
		for _, it := range feed["items"].([]any) {
			if it.(map[string]any)["id"] == productID {
				t.Fatal("un producto activo pero sin publicar apareció en el feed")
			}
		}
		want(t, h.do(http.MethodGet, publicProductDetailURL(o, productID), nil, nil), http.StatusOK) // el detalle SÍ es público, publicar solo gobierna el feed

		t.Run("publicarlo con PATCH channels lo hace aparecer", func(t *testing.T) {
			want(t, h.do(http.MethodPatch, productsURL(o, "/"+productID), map[string]any{"channels": []string{"ecommerce"}}, &admin), http.StatusOK)
			feed := jsonMap(t, h.do(http.MethodGet, publicFeedURL("?organization_id="+o.ID.String()), nil, nil))
			found := false
			for _, it := range feed["items"].([]any) {
				if it.(map[string]any)["id"] == productID {
					found = true
				}
			}
			if !found {
				t.Fatal("tras publicarlo, el producto no apareció en el feed")
			}

			t.Run("y despublicarlo con channels: [] lo saca de nuevo, sin desactivarlo", func(t *testing.T) {
				rec := h.do(http.MethodPatch, productsURL(o, "/"+productID), map[string]any{"channels": []string{}}, &admin)
				want(t, rec, http.StatusOK)
				if jsonMap(t, rec)["is_active"] != true {
					t.Fatal("despublicar no debe desactivar el producto")
				}
				feed := jsonMap(t, h.do(http.MethodGet, publicFeedURL("?organization_id="+o.ID.String()), nil, nil))
				for _, it := range feed["items"].([]any) {
					if it.(map[string]any)["id"] == productID {
						t.Fatal("un producto despublicado siguió apareciendo en el feed")
					}
				}
			})
		})
	})

	t.Run("un canal desconocido es 422", func(t *testing.T) {
		want(t, h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "X", "variants": []map[string]any{{"name": "Regular", "price_cents": 100}},
			"channels": []string{"instagram"},
		}, &admin), http.StatusUnprocessableEntity)
	})

	t.Run("ingredients va y viene", func(t *testing.T) {
		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Con ingredientes", "variants": []map[string]any{{"name": "Regular", "price_cents": 100}},
			"ingredients": []string{"Tomate", "  Queso  ", ""},
		}, &admin)
		want(t, rec, http.StatusCreated)
		// Se recortan los espacios y se descartan los vacíos.
		got := jsonMap(t, rec)["ingredients"].([]any)
		if len(got) != 2 || got[0] != "Tomate" || got[1] != "Queso" {
			t.Fatalf("ingredients = %v", got)
		}
	})
}
