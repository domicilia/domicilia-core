-- name: InsertPromotion :one
INSERT INTO promotions (
    organization_id, code, discount_type, value, min_order_cents,
    starts_at, ends_at, max_uses, per_customer_limit
) VALUES (
    @organization_id, @code, @discount_type, @value, @min_order_cents,
    sqlc.narg('starts_at'), sqlc.narg('ends_at'), sqlc.narg('max_uses'), sqlc.narg('per_customer_limit')
)
RETURNING *;

-- name: GetPromotion :one
SELECT * FROM promotions WHERE id = @id AND organization_id = @organization_id;

-- Sin distinguir mayúsculas, y solo entre las activas — el mismo criterio que hace único el
-- código (ver el índice parcial de la migración): si hay una desactivada con el mismo texto, no
-- es ambigüedad, esa ya no cuenta.
-- name: GetActivePromotionByCode :one
SELECT * FROM promotions WHERE organization_id = @organization_id AND upper(code) = upper(@code::text) AND is_active;

-- name: ListPromotions :many
SELECT * FROM promotions WHERE organization_id = @organization_id ORDER BY created_at DESC, id;

-- Cambio parcial: set_* distingue "no tocar" de "dejar en blanco" en starts_at/ends_at/max_uses/
-- per_customer_limit (sí admiten null: "sin fecha de inicio", "sin límite de usos"...). code,
-- discount_type, value y min_order_cents nunca se ponen en blanco, así que un ausente
-- (COALESCE) basta.
-- name: UpdatePromotion :one
UPDATE promotions
SET code               = COALESCE(sqlc.narg('code'), code),
    discount_type      = COALESCE(sqlc.narg('discount_type'), discount_type),
    value              = COALESCE(sqlc.narg('value'), value),
    min_order_cents    = COALESCE(sqlc.narg('min_order_cents'), min_order_cents),
    starts_at          = CASE WHEN @set_starts_at::boolean THEN sqlc.narg('starts_at') ELSE starts_at END,
    ends_at            = CASE WHEN @set_ends_at::boolean THEN sqlc.narg('ends_at') ELSE ends_at END,
    max_uses           = CASE WHEN @set_max_uses::boolean THEN sqlc.narg('max_uses') ELSE max_uses END,
    per_customer_limit = CASE WHEN @set_per_customer_limit::boolean THEN sqlc.narg('per_customer_limit') ELSE per_customer_limit END,
    is_active          = COALESCE(sqlc.narg('is_active'), is_active),
    updated_at         = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;

-- Un canje solo cuenta si el pedido ya dejó de ser un borrador Y sigue siendo esta la promoción
-- vigente en él (ver el comentario de la migración 00008): editar el carrito después de aplicar
-- un código lo borra de orders.promotion_id, así que ese canje deja de contar sin que nadie tenga
-- que limpiarlo a mano.
-- name: CountPromotionRedemptions :one
SELECT count(*) FROM promotion_redemptions pr
JOIN orders o ON o.id = pr.order_id
WHERE pr.promotion_id = @promotion_id AND o.status <> 'draft' AND o.promotion_id = pr.promotion_id;

-- name: CountCustomerPromotionRedemptions :one
SELECT count(*) FROM promotion_redemptions pr
JOIN orders o ON o.id = pr.order_id
WHERE pr.promotion_id = @promotion_id AND pr.customer_id = @customer_id
  AND o.status <> 'draft' AND o.promotion_id = pr.promotion_id;

-- Reemplaza el canje del pedido (nunca lo acumula): aplicar OTRO código sobre el mismo carrito
-- pisa el anterior. Se borra primero (no ON CONFLICT) porque el nuevo puede ser de OTRA
-- promoción, y el UNIQUE es solo por order_id.
-- name: DeletePromotionRedemptionByOrder :exec
DELETE FROM promotion_redemptions WHERE order_id = @order_id;

-- name: InsertPromotionRedemption :one
INSERT INTO promotion_redemptions (promotion_id, organization_id, order_id, customer_id, discount_cents)
VALUES (@promotion_id, @organization_id, @order_id, @customer_id, @discount_cents)
RETURNING *;
