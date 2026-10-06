-- name: GetUser :one
SELECT * FROM users WHERE id = @id;

-- Sin distinguir mayúsculas: hay correos históricos guardados como se escribieron,
-- y el índice único sí las distingue.
-- name: UserEmailExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower(@email));

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(@email);

-- name: InsertUser :one
INSERT INTO users (id, email, full_name, is_active, created_at, updated_at)
VALUES (@id, @email, sqlc.narg('full_name'), TRUE, timezone('utc', now()), timezone('utc', now()))
RETURNING *;

-- name: UpdateUserFullName :one
UPDATE users
SET full_name = COALESCE(sqlc.narg('full_name'), full_name),
    updated_at = timezone('utc', now())
WHERE id = @id
RETURNING *;

-- name: SetUserActive :one
UPDATE users
SET is_active = @is_active, updated_at = timezone('utc', now())
WHERE id = @id
RETURNING *;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- Filtros del listado de la plataforma. `search` ya viene escapado para LIKE.
-- `role` casa con el código de un rol de plataforma o de organización;
-- `organization_id`, con pertenecer a esa organización.
-- name: ListUsers :many
SELECT u.* FROM users u
WHERE (sqlc.narg('search')::text IS NULL
        OR u.email ILIKE '%' || sqlc.narg('search')::text || '%'
        OR u.full_name ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('is_active')::boolean IS NULL OR u.is_active = sqlc.narg('is_active')::boolean)
  AND (sqlc.narg('role')::text IS NULL
        OR EXISTS (SELECT 1 FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
                   WHERE upr.user_id = u.id AND r.code = sqlc.narg('role')::text)
        OR EXISTS (SELECT 1 FROM user_organizations uo JOIN roles r ON r.id = uo.role_id
                   WHERE uo.user_id = u.id AND r.code = sqlc.narg('role')::text))
  AND (sqlc.narg('organization_id')::uuid IS NULL
        OR EXISTS (SELECT 1 FROM user_organizations uo
                   WHERE uo.user_id = u.id AND uo.organization_id = sqlc.narg('organization_id')::uuid))
ORDER BY u.email, u.id
LIMIT @page_size OFFSET @page_offset;

-- name: CountUsersFiltered :one
SELECT count(*) FROM users u
WHERE (sqlc.narg('search')::text IS NULL
        OR u.email ILIKE '%' || sqlc.narg('search')::text || '%'
        OR u.full_name ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('is_active')::boolean IS NULL OR u.is_active = sqlc.narg('is_active')::boolean)
  AND (sqlc.narg('role')::text IS NULL
        OR EXISTS (SELECT 1 FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
                   WHERE upr.user_id = u.id AND r.code = sqlc.narg('role')::text)
        OR EXISTS (SELECT 1 FROM user_organizations uo JOIN roles r ON r.id = uo.role_id
                   WHERE uo.user_id = u.id AND r.code = sqlc.narg('role')::text))
  AND (sqlc.narg('organization_id')::uuid IS NULL
        OR EXISTS (SELECT 1 FROM user_organizations uo
                   WHERE uo.user_id = u.id AND uo.organization_id = sqlc.narg('organization_id')::uuid));

-- name: ListMembershipsByUser :many
SELECT uo.organization_id, o.name AS organization_name, o.slug AS organization_slug,
       o.status AS organization_status, o.plan_tier AS plan_tier,
       r.id AS role_id, r.code AS role_code, r.name AS role_name
FROM user_organizations uo
JOIN organizations o ON o.id = uo.organization_id
JOIN roles r ON r.id = uo.role_id
WHERE uo.user_id = @user_id
ORDER BY o.name;

-- name: ListMembershipsByUsers :many
SELECT uo.user_id, uo.organization_id, o.name AS organization_name, o.slug AS organization_slug,
       r.id AS role_id, r.code AS role_code, r.name AS role_name
FROM user_organizations uo
JOIN organizations o ON o.id = uo.organization_id
JOIN roles r ON r.id = uo.role_id
WHERE uo.user_id = ANY(@user_ids::uuid[])
ORDER BY uo.user_id, o.name;
