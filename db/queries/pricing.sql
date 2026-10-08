-- name: GetPricingSettings :one
SELECT * FROM pricing_settings WHERE id;

-- name: UpdatePricingSettings :one
UPDATE pricing_settings
SET platform_fee_bps = @platform_fee_bps, promo_platform_fee_bps = @promo_platform_fee_bps,
    courier_fee_bps = @courier_fee_bps, delivery_fee_cents = @delivery_fee_cents,
    gateway_plan_code = @gateway_plan_code, split_enabled = @split_enabled,
    updated_by = @updated_by, updated_at = now()
WHERE id
RETURNING *;

-- name: ListGatewayFeePlans :many
SELECT * FROM gateway_fee_plans ORDER BY code;

-- name: GetGatewayFeePlan :one
SELECT * FROM gateway_fee_plans WHERE code = @code;

-- name: UpdateGatewayFeePlan :one
UPDATE gateway_fee_plans
SET name = @name, card_percent_bps = @card_percent_bps, card_fixed_cents = @card_fixed_cents,
    international_extra_bps = @international_extra_bps,
    wallet_percent_bps = @wallet_percent_bps, wallet_fixed_cents = @wallet_fixed_cents,
    pse_percent_bps = @pse_percent_bps, pse_fixed_cents = @pse_fixed_cents,
    pse_small_threshold_cents = @pse_small_threshold_cents, pse_small_fixed_cents = @pse_small_fixed_cents,
    vat_bps = @vat_bps, notes = sqlc.narg('notes'), updated_by = @updated_by, updated_at = now()
WHERE code = @code
RETURNING *;

-- name: GetOrganizationPricing :one
SELECT * FROM organization_pricing WHERE organization_id = @organization_id;

-- Reemplaza la configuración propia de una organización. Cada campo NULL = hereda de la general.
-- name: UpsertOrganizationPricing :one
INSERT INTO organization_pricing (organization_id, platform_fee_bps, promo_platform_fee_bps, courier_fee_bps,
    delivery_fee_cents, gateway_plan_code, epayco_merchant_id, updated_by)
VALUES (@organization_id, sqlc.narg('platform_fee_bps'), sqlc.narg('promo_platform_fee_bps'), sqlc.narg('courier_fee_bps'),
    sqlc.narg('delivery_fee_cents'), sqlc.narg('gateway_plan_code'), sqlc.narg('epayco_merchant_id'), @updated_by)
ON CONFLICT (organization_id) DO UPDATE
SET platform_fee_bps = EXCLUDED.platform_fee_bps, promo_platform_fee_bps = EXCLUDED.promo_platform_fee_bps,
    courier_fee_bps = EXCLUDED.courier_fee_bps, delivery_fee_cents = EXCLUDED.delivery_fee_cents,
    gateway_plan_code = EXCLUDED.gateway_plan_code, epayco_merchant_id = EXCLUDED.epayco_merchant_id,
    updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- La vista general del superadmin: todas las organizaciones (no archivadas) con lo que tengan
-- sobrescrito, si algo.
-- name: ListOrganizationsPricing :many
SELECT o.id, o.name, o.slug, o.status,
       op.platform_fee_bps, op.promo_platform_fee_bps, op.courier_fee_bps, op.delivery_fee_cents,
       op.gateway_plan_code, op.epayco_merchant_id, op.updated_at
FROM organizations o
LEFT JOIN organization_pricing op ON op.organization_id = o.id
WHERE o.status <> 'archived'
ORDER BY o.name;
