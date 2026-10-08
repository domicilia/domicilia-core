-- ---------------------------------------------------------------------------
-- Categorías
-- ---------------------------------------------------------------------------

-- name: InsertCategory :one
INSERT INTO categories (organization_id, name, position)
VALUES (@organization_id, @name, @position)
RETURNING *;

-- name: GetCategory :one
SELECT * FROM categories WHERE id = @id AND organization_id = @organization_id;

-- name: ListCategories :many
SELECT * FROM categories WHERE organization_id = @organization_id ORDER BY position, id;

-- name: UpdateCategory :one
UPDATE categories
SET name       = COALESCE(sqlc.narg('name'), name),
    position   = COALESCE(sqlc.narg('position'), position),
    is_active  = COALESCE(sqlc.narg('is_active'), is_active),
    updated_at = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;

-- ---------------------------------------------------------------------------
-- Productos
-- ---------------------------------------------------------------------------

-- name: InsertProduct :one
INSERT INTO products (organization_id, category_id, name, description, image_url, position, ingredients, channels,
                      promo_discount_bps)
VALUES (@organization_id, sqlc.narg('category_id'), @name, sqlc.narg('description'), sqlc.narg('image_url'), @position,
        @ingredients::text[], @channels::text[], @promo_discount_bps)
RETURNING *;

-- name: GetProduct :one
SELECT * FROM products WHERE id = @id AND organization_id = @organization_id;

-- name: ListProducts :many
SELECT * FROM products
WHERE organization_id = @organization_id
  AND (sqlc.narg('category_id')::uuid IS NULL OR category_id = sqlc.narg('category_id'))
ORDER BY position, id
LIMIT @page_size OFFSET @page_offset;

-- name: CountProducts :one
SELECT count(*) FROM products
WHERE organization_id = @organization_id
  AND (sqlc.narg('category_id')::uuid IS NULL OR category_id = sqlc.narg('category_id'));

-- Cambio parcial: set_* distingue "no tocar" de "reemplazar" para los campos que no se pueden
-- expresar con un simple ausente (category_id/description/image_url admiten null; ingredients y
-- channels son arreglos, y un arreglo vacío es un valor real — "sin ingredientes", "sin publicar
-- en ningún canal" — no "no tocar"). name/position/is_active nunca se ponen en blanco, así que
-- un ausente (COALESCE) basta ahí.
-- name: UpdateProduct :one
UPDATE products
SET category_id  = CASE WHEN @set_category::boolean THEN sqlc.narg('category_id') ELSE category_id END,
    name         = COALESCE(sqlc.narg('name'), name),
    description  = CASE WHEN @set_description::boolean THEN sqlc.narg('description') ELSE description END,
    image_url    = CASE WHEN @set_image::boolean THEN sqlc.narg('image_url') ELSE image_url END,
    position     = COALESCE(sqlc.narg('position'), position),
    is_active    = COALESCE(sqlc.narg('is_active'), is_active),
    ingredients  = CASE WHEN @set_ingredients::boolean THEN @ingredients::text[] ELSE ingredients END,
    channels     = CASE WHEN @set_channels::boolean THEN @channels::text[] ELSE channels END,
    promo_discount_bps = COALESCE(sqlc.narg('promo_discount_bps'), promo_discount_bps),
    updated_at   = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;

-- ---------------------------------------------------------------------------
-- Variantes — nunca DELETE real (docs/ecommerce.md §1.1.6): se reemplazan por upsert +
-- desactivación de las que ya no vienen en la lista nueva.
-- ---------------------------------------------------------------------------

-- Recibe uno o varios product_id — así Get (un id) y List (una página completa) usan la misma
-- consulta sin caer en N+1 al hidratar varios productos con sus variantes.
-- name: ListVariantsByProducts :many
SELECT * FROM product_variants WHERE product_id = ANY(@product_ids::uuid[]) ORDER BY product_id, position, id;

-- name: InsertVariant :one
INSERT INTO product_variants (product_id, organization_id, name, price_cents, is_default, position)
VALUES (@product_id, @organization_id, @name, @price_cents, @is_default, @position)
RETURNING *;

-- name: UpdateVariant :one
UPDATE product_variants
SET name        = @name,
    price_cents = @price_cents,
    is_default  = @is_default,
    is_active   = true,
    position    = @position,
    updated_at  = now()
WHERE id = @id AND product_id = @product_id AND organization_id = @organization_id
RETURNING *;

-- Todas las que no vinieron en el reemplazo, de un solo golpe.
-- name: DeactivateOtherVariants :exec
UPDATE product_variants
SET is_active = false, updated_at = now()
WHERE product_id = @product_id AND organization_id = @organization_id
  AND NOT (id = ANY(@keep_ids::uuid[]));

-- ---------------------------------------------------------------------------
-- Grupos de modificadores
-- ---------------------------------------------------------------------------

-- name: InsertModifierGroup :one
INSERT INTO modifier_groups (organization_id, name, selection_type, min_select, max_select)
VALUES (@organization_id, @name, @selection_type, @min_select, @max_select)
RETURNING *;

-- Usada por internal/orders (a través de catalog.Repository) para armar la línea de un pedido:
-- necesita TODOS los grupos que aplican a un producto para validar obligatoriedad y min/max,
-- no solo los que el cliente eligió.
-- name: GetModifierGroupsByIDs :many
SELECT * FROM modifier_groups WHERE id = ANY(@ids::uuid[]) AND organization_id = @organization_id;

-- name: GetModifierGroup :one
SELECT * FROM modifier_groups WHERE id = @id AND organization_id = @organization_id;

-- name: ListModifierGroups :many
SELECT * FROM modifier_groups WHERE organization_id = @organization_id ORDER BY name, id;

-- name: UpdateModifierGroup :one
UPDATE modifier_groups
SET name           = COALESCE(sqlc.narg('name'), name),
    selection_type = COALESCE(sqlc.narg('selection_type'), selection_type),
    min_select     = COALESCE(sqlc.narg('min_select'), min_select),
    max_select     = COALESCE(sqlc.narg('max_select'), max_select),
    is_active      = COALESCE(sqlc.narg('is_active'), is_active),
    updated_at     = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;

-- Uno o varios modifier_group_id — mismo motivo que ListVariantsByProducts.
-- name: ListModifierOptionsByGroups :many
SELECT * FROM modifier_options WHERE modifier_group_id = ANY(@modifier_group_ids::uuid[]) ORDER BY modifier_group_id, position, id;

-- name: InsertModifierOption :one
INSERT INTO modifier_options (modifier_group_id, organization_id, name, price_delta_cents, position)
VALUES (@modifier_group_id, @organization_id, @name, @price_delta_cents, @position)
RETURNING *;

-- name: UpdateModifierOption :one
UPDATE modifier_options
SET name              = @name,
    price_delta_cents = @price_delta_cents,
    is_active         = true,
    position          = @position,
    updated_at        = now()
WHERE id = @id AND modifier_group_id = @modifier_group_id AND organization_id = @organization_id
RETURNING *;

-- name: DeactivateOtherModifierOptions :exec
UPDATE modifier_options
SET is_active = false, updated_at = now()
WHERE modifier_group_id = @modifier_group_id AND organization_id = @organization_id
  AND NOT (id = ANY(@keep_ids::uuid[]));

-- ---------------------------------------------------------------------------
-- Qué grupos de modificadores aplican a cada producto (N:M) — se reemplaza completo.
-- ---------------------------------------------------------------------------

-- Uno o varios product_id — mismo motivo que ListVariantsByProducts.
-- name: ListModifierGroupIDsByProducts :many
SELECT product_id, modifier_group_id FROM product_modifier_groups WHERE product_id = ANY(@product_ids::uuid[]) ORDER BY product_id, position;

-- name: DeleteProductModifierGroups :exec
DELETE FROM product_modifier_groups WHERE product_id = @product_id AND organization_id = @organization_id;

-- name: InsertProductModifierGroup :exec
INSERT INTO product_modifier_groups (product_id, organization_id, modifier_group_id, position)
VALUES (@product_id, @organization_id, @modifier_group_id, @position);

-- ---------------------------------------------------------------------------
-- Feed público: productos de VARIAS organizaciones a la vez (Inicio del cliente, estilo
-- Rappi/UberEats/DiDi). Sin sesión, mismo criterio que ListPublicOrganizations: organización
-- activa (o.status = 'active') y producto activo Y publicado a este canal
-- ('ecommerce' = ANY(p.channels) — ver la migración 00009: una organización tiene que elegir
-- publicar un producto a propósito, no basta con crearlo). El JOIN (no LEFT JOIN) con una
-- variante activa excluye a propósito un producto sin nada que vender — no hay precio que
-- mostrar ni variante que agregar al carrito.
--
-- organization_id/organization_slug filtran al menú de UN restaurante (la vitrina de la
-- organización los reutiliza — slug porque esa página solo conoce el slug de la URL, nunca el
-- id); sin ninguno de los dos, cruza todas.
-- ---------------------------------------------------------------------------

-- name: ListPublicProducts :many
SELECT p.id, p.organization_id, o.name AS organization_name, o.slug AS organization_slug,
       s.logo_url AS organization_logo_url, p.category_id, c.name AS category_name,
       p.name, p.description, p.image_url, p.promo_discount_bps, MIN(v.price_cents)::int AS min_price_cents
FROM products p
JOIN organizations o ON o.id = p.organization_id
LEFT JOIN organization_settings s ON s.organization_id = o.id
LEFT JOIN categories c ON c.id = p.category_id
JOIN product_variants v ON v.product_id = p.id AND v.is_active
WHERE o.status = 'active' AND p.is_active AND 'ecommerce' = ANY(p.channels)
  AND (sqlc.narg('organization_id')::uuid IS NULL OR p.organization_id = sqlc.narg('organization_id'))
  AND (sqlc.narg('organization_slug')::text IS NULL OR o.slug = sqlc.narg('organization_slug'))
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('category')::text IS NULL OR c.name ILIKE sqlc.narg('category')::text)
GROUP BY p.id, p.organization_id, o.name, o.slug, s.logo_url, p.category_id, c.name, p.name, p.description, p.image_url,
         p.promo_discount_bps
ORDER BY p.created_at DESC, p.id
LIMIT @page_size OFFSET @page_offset;

-- Mismos JOIN y filtros que ListPublicProducts — tiene que coincidir siempre con esa consulta.
-- name: CountPublicProducts :one
SELECT count(DISTINCT p.id)
FROM products p
JOIN organizations o ON o.id = p.organization_id
LEFT JOIN categories c ON c.id = p.category_id
JOIN product_variants v ON v.product_id = p.id AND v.is_active
WHERE o.status = 'active' AND p.is_active AND 'ecommerce' = ANY(p.channels)
  AND (sqlc.narg('organization_id')::uuid IS NULL OR p.organization_id = sqlc.narg('organization_id'))
  AND (sqlc.narg('organization_slug')::text IS NULL OR o.slug = sqlc.narg('organization_slug'))
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('category')::text IS NULL OR c.name ILIKE sqlc.narg('category')::text);
