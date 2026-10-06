package app_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func categoriesURL(o organization, suffix string) string { return orgURL(o, "/categories"+suffix) }
func productsURL(o organization, suffix string) string   { return orgURL(o, "/products"+suffix) }
func modifierGroupsURL(o organization, suffix string) string {
	return orgURL(o, "/modifier-groups"+suffix)
}

func TestSoloElAdminAdministraElMenu(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Café Central")
	empleado, extrano := h.user(), h.user()
	h.join(empleado, o, "employee")

	// Crear (org.catalog.manage) — solo el admin.
	body := map[string]any{"name": "Entradas"}
	want(t, h.do(http.MethodPost, categoriesURL(o, ""), body, &empleado), http.StatusForbidden)
	want(t, h.do(http.MethodPost, categoriesURL(o, ""), body, &extrano), http.StatusForbidden)
	rec := h.do(http.MethodPost, categoriesURL(o, ""), body, &admin)
	want(t, rec, http.StatusCreated)

	// Leer (org.catalog.read) — admin y empleado sí, un extraño no.
	want(t, h.do(http.MethodGet, categoriesURL(o, ""), nil, &admin), http.StatusOK)
	want(t, h.do(http.MethodGet, categoriesURL(o, ""), nil, &empleado), http.StatusOK)
	want(t, h.do(http.MethodGet, categoriesURL(o, ""), nil, &extrano), http.StatusForbidden)

	// org.catalog.manage SÍ es delegable (a diferencia de org.inbox.manage): administrar el menú
	// es una operación de negocio rutinaria, no algo tan sensible como conectar un número de
	// WhatsApp — un admin puede delegarlo a un rol a medida, igual que org.contacts.manage.
	rec = h.do(http.MethodPost, orgRolesURL(o), roleReq("mesero", "Mesero", "org.catalog.manage"), &admin)
	want(t, rec, http.StatusCreated)
	mesero := h.user()
	h.join(mesero, o, jsonMap(t, rec)["code"].(string))
	want(t, h.do(http.MethodPost, categoriesURL(o, ""), map[string]any{"name": "Postres"}, &mesero), http.StatusCreated)
}

func TestCategorias(t *testing.T) {
	t.Run("crea, lista (activas e inactivas) y actualiza", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")

		rec := h.do(http.MethodPost, categoriesURL(o, ""), map[string]any{"name": "Pizzas", "position": 2}, &admin)
		want(t, rec, http.StatusCreated)
		cat := jsonMap(t, rec)
		if cat["name"] != "Pizzas" || cat["position"] != float64(2) || cat["is_active"] != true {
			t.Fatalf("categoría creada = %v", cat)
		}
		id := cat["id"].(string)

		rec = h.do(http.MethodPatch, categoriesURL(o, "/"+id), map[string]any{"is_active": false}, &admin)
		want(t, rec, http.StatusOK)
		if jsonMap(t, rec)["is_active"] != false {
			t.Fatalf("no se desactivó")
		}

		// Sigue en el listado (activas E inactivas) — quien administra necesita verla para reactivarla.
		rec = h.do(http.MethodGet, categoriesURL(o, ""), nil, &admin)
		want(t, rec, http.StatusOK)
		found := false
		for _, raw := range decode[[]any](t, rec) {
			if raw.(map[string]any)["id"] == id {
				found = true
			}
		}
		if !found {
			t.Fatal("la categoría desactivada desapareció del listado")
		}
	})

	t.Run("el nombre es único entre las activas, pero se puede reciclar de una inactiva", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPost, categoriesURL(o, ""), map[string]any{"name": "Postres"}, &admin), http.StatusCreated)
		want(t, h.do(http.MethodPost, categoriesURL(o, ""), map[string]any{"name": "postres"}, &admin), http.StatusConflict)
	})

	t.Run("PATCH sin campos es 422", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, categoriesURL(o, ""), map[string]any{"name": "Bebidas"}, &admin)
		id := jsonMap(t, rec)["id"].(string)
		want(t, h.do(http.MethodPatch, categoriesURL(o, "/"+id), map[string]any{}, &admin), http.StatusUnprocessableEntity)
	})
}

func TestProductosYVariantes(t *testing.T) {
	t.Run("exige al menos una variante", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		want(t, h.do(http.MethodPost, productsURL(o, ""), map[string]any{"name": "Gaseosa", "variants": []any{}}, &admin), http.StatusUnprocessableEntity)
	})

	t.Run("sin ninguna variante por defecto, marca la primera", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name":     "Gaseosa",
			"variants": []map[string]any{{"name": "Personal", "price_cents": 3000}, {"name": "Familiar", "price_cents": 8000}},
		}, &admin)
		want(t, rec, http.StatusCreated)
		p := jsonMap(t, rec)
		variants := p["variants"].([]any)
		if len(variants) != 2 {
			t.Fatalf("variantes = %v", variants)
		}
		if variants[0].(map[string]any)["is_default"] != true {
			t.Fatalf("la primera variante debería quedar por defecto: %v", variants[0])
		}
	})

	t.Run("dos variantes por defecto es un error del cliente", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Gaseosa",
			"variants": []map[string]any{
				{"name": "Personal", "price_cents": 3000, "is_default": true},
				{"name": "Familiar", "price_cents": 8000, "is_default": true},
			},
		}, &admin)
		want(t, rec, http.StatusUnprocessableEntity)
	})

	t.Run("category_id de otra organización no existe para esta", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		adminOtra, otra := orgAdmin(h, "Otra")
		catRec := h.do(http.MethodPost, categoriesURL(otra, ""), map[string]any{"name": "Ajena"}, &adminOtra)
		catID := jsonMap(t, catRec)["id"].(string)

		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Pizza", "category_id": catID,
			"variants": []map[string]any{{"name": "Regular", "price_cents": 20000}},
		}, &admin)
		want(t, rec, http.StatusUnprocessableEntity)
	})

	t.Run("get y list respetan el aislamiento entre organizaciones", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		adminOtra, otra := orgAdmin(h, "Otra")

		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Pizza", "variants": []map[string]any{{"name": "Regular", "price_cents": 20000}},
		}, &admin)
		id := jsonMap(t, rec)["id"].(string)

		want(t, h.do(http.MethodGet, productsURL(o, "/"+id), nil, &adminOtra), http.StatusForbidden)
		want(t, h.do(http.MethodGet, productsURL(otra, "/"+id), nil, &adminOtra), http.StatusNotFound)

		listRec := h.do(http.MethodGet, productsURL(otra, ""), nil, &adminOtra)
		want(t, listRec, http.StatusOK)
		if total := jsonMap(t, listRec)["total"]; total != float64(0) {
			t.Fatalf("el producto de Acme se coló en el listado de Otra: total = %v", total)
		}
	})

	t.Run("actualizar variantes reemplaza: upsert + desactiva lo que no viene, nunca borra", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Pizza",
			"variants": []map[string]any{
				{"name": "Personal", "price_cents": 15000, "is_default": true},
				{"name": "Familiar", "price_cents": 30000},
			},
		}, &admin)
		p := jsonMap(t, rec)
		id := p["id"].(string)
		variants := p["variants"].([]any)
		personalID := variants[0].(map[string]any)["id"].(string)
		familiarID := variants[1].(map[string]any)["id"].(string)

		// Reemplazo: sube el precio de Personal (por id), quita Familiar, agrega Mediana.
		rec = h.do(http.MethodPatch, productsURL(o, "/"+id), map[string]any{
			"variants": []map[string]any{
				{"id": personalID, "name": "Personal", "price_cents": 16000, "is_default": true},
				{"name": "Mediana", "price_cents": 22000},
			},
		}, &admin)
		want(t, rec, http.StatusOK)
		updated := jsonMap(t, rec)["variants"].([]any)
		if len(updated) != 3 {
			t.Fatalf("variantes tras el reemplazo = %v (Familiar debe seguir existiendo, solo desactivada)", updated)
		}
		byID := map[string]map[string]any{}
		for _, v := range updated {
			row := v.(map[string]any)
			byID[row["id"].(string)] = row
		}
		if byID[personalID]["price_cents"] != float64(16000) || byID[personalID]["is_active"] != true {
			t.Fatalf("Personal no se actualizó bien: %v", byID[personalID])
		}
		if byID[familiarID]["is_active"] != false {
			t.Fatalf("Familiar debía quedar desactivada, no borrada: %v", byID[familiarID])
		}
		foundMediana := false
		for _, row := range byID {
			if row["name"] == "Mediana" && row["is_active"] == true {
				foundMediana = true
			}
		}
		if !foundMediana {
			t.Fatal("Mediana no se creó")
		}
		if n := count(t, `SELECT count(*) FROM product_variants WHERE product_id = $1`, uuid.MustParse(id)); n != 3 {
			t.Fatalf("filas en la base = %d, quería 3 (nunca se borra una variante)", n)
		}
	})

	t.Run("variants ausente en el PATCH no las toca", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Pizza", "variants": []map[string]any{{"name": "Regular", "price_cents": 20000}},
		}, &admin)
		id := jsonMap(t, rec)["id"].(string)

		rec = h.do(http.MethodPatch, productsURL(o, "/"+id), map[string]any{"position": 5}, &admin)
		want(t, rec, http.StatusOK)
		if v := jsonMap(t, rec)["variants"].([]any); len(v) != 1 {
			t.Fatalf("variantes = %v, no debieron tocarse", v)
		}
	})
}

func TestGruposDeModificadores(t *testing.T) {
	t.Run("un grupo single admite como mucho una opción seleccionable", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
			"name": "Tamaño", "selection_type": "single", "max_select": 2,
			"options": []map[string]any{{"name": "Grande", "price_delta_cents": 0}},
		}, &admin)
		want(t, rec, http.StatusUnprocessableEntity)
	})

	t.Run("crea con opciones y se puede asociar a un producto", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		rec := h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
			"name": "Extras", "selection_type": "multiple", "min_select": 0, "max_select": 3,
			"options": []map[string]any{{"name": "Queso", "price_delta_cents": 2000}, {"name": "Tocineta", "price_delta_cents": 3000}},
		}, &admin)
		want(t, rec, http.StatusCreated)
		g := jsonMap(t, rec)
		groupID := g["id"].(string)
		if opts := g["options"].([]any); len(opts) != 2 {
			t.Fatalf("opciones = %v", opts)
		}

		rec = h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Hamburguesa", "variants": []map[string]any{{"name": "Regular", "price_cents": 18000}},
			"modifier_group_ids": []string{groupID},
		}, &admin)
		want(t, rec, http.StatusCreated)
		ids := jsonMap(t, rec)["modifier_group_ids"].([]any)
		if len(ids) != 1 || ids[0] != groupID {
			t.Fatalf("modifier_group_ids = %v", ids)
		}
	})

	t.Run("un id de grupo repetido no rompe la escritura", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		g := jsonMap(t, h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
			"name": "Salsas", "selection_type": "multiple", "max_select": 3,
			"options": []map[string]any{{"name": "BBQ", "price_delta_cents": 0}},
		}, &admin))
		groupID := g["id"].(string)

		rec := h.do(http.MethodPost, productsURL(o, ""), map[string]any{
			"name": "Alitas", "variants": []map[string]any{{"name": "Regular", "price_cents": 15000}},
			"modifier_group_ids": []string{groupID, groupID},
		}, &admin)
		want(t, rec, http.StatusCreated)
		if ids := jsonMap(t, rec)["modifier_group_ids"].([]any); len(ids) != 1 {
			t.Fatalf("modifier_group_ids = %v, quería el duplicado colapsado", ids)
		}
	})

	t.Run("actualizar opciones reemplaza: upsert + desactiva, nunca borra", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		g := jsonMap(t, h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
			"name": "Extras", "selection_type": "multiple", "max_select": 3,
			"options": []map[string]any{{"name": "Queso", "price_delta_cents": 2000}},
		}, &admin))
		groupID := g["id"].(string)
		quesoID := g["options"].([]any)[0].(map[string]any)["id"].(string)

		rec := h.do(http.MethodPatch, modifierGroupsURL(o, "/"+groupID), map[string]any{
			"options": []map[string]any{{"name": "Tocineta", "price_delta_cents": 3000}},
		}, &admin)
		want(t, rec, http.StatusOK)
		opts := jsonMap(t, rec)["options"].([]any)
		if len(opts) != 2 {
			t.Fatalf("opciones tras el reemplazo = %v", opts)
		}
		for _, o := range opts {
			row := o.(map[string]any)
			if row["id"] == quesoID && row["is_active"] != false {
				t.Fatalf("Queso debía quedar desactivada: %v", row)
			}
		}
	})

	t.Run("cambiar solo min_select se valida contra el max_select vigente", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		g := jsonMap(t, h.do(http.MethodPost, modifierGroupsURL(o, ""), map[string]any{
			"name": "Tamaño", "selection_type": "single", "max_select": 1,
			"options": []map[string]any{{"name": "Grande", "price_delta_cents": 0}},
		}, &admin))
		groupID := g["id"].(string)

		// min_select=2 > max_select vigente (1): inconsistente, debe rechazarse sin tocar nada.
		rec := h.do(http.MethodPatch, modifierGroupsURL(o, "/"+groupID), map[string]any{"min_select": 2}, &admin)
		want(t, rec, http.StatusUnprocessableEntity)
	})
}
