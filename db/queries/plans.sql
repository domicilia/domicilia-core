-- name: GetCurrentSubscription :one
SELECT * FROM organization_subscriptions
WHERE organization_id = @organization_id AND ended_at IS NULL;

-- name: EndCurrentSubscription :execrows
UPDATE organization_subscriptions SET ended_at = now()
WHERE organization_id = @organization_id AND ended_at IS NULL;

-- name: InsertSubscription :one
INSERT INTO organization_subscriptions (organization_id, plan_tier, changed_by, reason)
VALUES (@organization_id, @plan_tier, sqlc.narg('changed_by'), sqlc.narg('reason'))
RETURNING *;

-- name: ListSubscriptions :many
SELECT * FROM organization_subscriptions
WHERE organization_id = @organization_id
ORDER BY started_at DESC, id;

-- name: ListFeatureOverrides :many
SELECT * FROM organization_feature_overrides
WHERE organization_id = @organization_id
ORDER BY feature;

-- name: UpsertFeatureOverride :one
INSERT INTO organization_feature_overrides (organization_id, feature, enabled, reason, created_by)
VALUES (@organization_id, @feature, @enabled, sqlc.narg('reason'), sqlc.narg('created_by'))
ON CONFLICT (organization_id, feature)
DO UPDATE SET enabled = EXCLUDED.enabled, reason = EXCLUDED.reason,
              created_by = EXCLUDED.created_by, created_at = now()
RETURNING *;

-- name: DeleteFeatureOverride :execrows
DELETE FROM organization_feature_overrides
WHERE organization_id = @organization_id AND feature = @feature;
