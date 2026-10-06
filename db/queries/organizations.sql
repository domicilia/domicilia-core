-- name: GetOrganization :one
SELECT * FROM organizations WHERE id = @id;

-- name: GetOrganizationBySlug :one
SELECT * FROM organizations WHERE slug = @slug;

-- Sin distinguir mayúsculas. except_id permite renombrar sin chocar con uno mismo.
-- name: OrganizationNameExists :one
SELECT EXISTS (
    SELECT 1 FROM organizations
    WHERE lower(name) = lower(@name)
      AND (sqlc.narg('except_id')::uuid IS NULL OR id <> sqlc.narg('except_id')::uuid));

-- name: OrganizationSlugExists :one
SELECT EXISTS (SELECT 1 FROM organizations WHERE slug = @slug);

-- Las archivadas no salen en los listados corrientes; se piden con el filtro de estado.
-- name: ListOrganizations :many
SELECT * FROM organizations WHERE status <> 'archived' ORDER BY name;

-- name: ListOrganizationsByUser :many
SELECT o.*
FROM organizations o
JOIN user_organizations uo ON uo.organization_id = o.id
WHERE uo.user_id = @user_id AND o.status <> 'archived'
ORDER BY o.name;

-- Listado de la plataforma. `search` ya viene escapado para LIKE.
-- name: SearchOrganizations :many
SELECT * FROM organizations o
WHERE (sqlc.narg('search')::text IS NULL
        OR o.name ILIKE '%' || sqlc.narg('search')::text || '%'
        OR o.slug ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('status')::text IS NULL OR o.status = sqlc.narg('status')::text)
  AND (sqlc.narg('plan_tier')::text IS NULL OR o.plan_tier = sqlc.narg('plan_tier')::text)
ORDER BY o.name, o.id
LIMIT @page_size OFFSET @page_offset;

-- name: CountSearchOrganizations :one
SELECT count(*) FROM organizations o
WHERE (sqlc.narg('search')::text IS NULL
        OR o.name ILIKE '%' || sqlc.narg('search')::text || '%'
        OR o.slug ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('status')::text IS NULL OR o.status = sqlc.narg('status')::text)
  AND (sqlc.narg('plan_tier')::text IS NULL OR o.plan_tier = sqlc.narg('plan_tier')::text);

-- Directorio público (app de cliente): solo activas, solo la cara del negocio.
-- Sin business_hours/open_now aquí a propósito — eso es coste por fila (parsear
-- jsonb y calcular contra la hora actual) que solo vale la pena en el detalle
-- de una organización (ver Public/GetOrganizationBySlug), no en un listado.
-- LEFT JOIN a propósito, igual que el fallback a DefaultSettings() en
-- Settings(): una organización sin fila de ajustes (no debería pasar, pero
-- que no desaparezca en silencio del directorio si pasa) sale con
-- logo_url/city en null, no fuera de la lista.
-- name: ListPublicOrganizations :many
SELECT o.name, o.slug, o.description, s.logo_url, s.city
FROM organizations o
LEFT JOIN organization_settings s ON s.organization_id = o.id
WHERE o.status = 'active'
  AND (sqlc.narg('search')::text IS NULL
        OR o.name ILIKE '%' || sqlc.narg('search')::text || '%'
        OR o.slug ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY o.name, o.id
LIMIT @page_size OFFSET @page_offset;

-- name: CountPublicOrganizations :one
SELECT count(*)
FROM organizations o
WHERE o.status = 'active'
  AND (sqlc.narg('search')::text IS NULL
        OR o.name ILIKE '%' || sqlc.narg('search')::text || '%'
        OR o.slug ILIKE '%' || sqlc.narg('search')::text || '%');

-- name: InsertOrganization :one
INSERT INTO organizations (id, name, slug, description, status, plan_tier, status_changed_at, created_at, updated_at)
VALUES (@id, @name, @slug, sqlc.narg('description'), 'active', @plan_tier, now(),
        timezone('utc', now()), timezone('utc', now()))
RETURNING *;

-- Bloquea la fila: los cambios de estado y de plan leen y escriben en una transacción.
-- name: GetOrganizationForUpdate :one
SELECT * FROM organizations WHERE id = @id FOR UPDATE;

-- set_description distingue "no tocar" de "dejar en blanco".
-- name: UpdateOrganizationProfile :one
UPDATE organizations
SET name = COALESCE(sqlc.narg('name'), name),
    description = CASE WHEN @set_description::boolean THEN sqlc.narg('description') ELSE description END,
    updated_at = timezone('utc', now())
WHERE id = @id
RETURNING *;

-- name: SetOrganizationStatus :one
UPDATE organizations
SET status = @status, status_reason = sqlc.narg('reason'), status_changed_at = now(),
    updated_at = timezone('utc', now())
WHERE id = @id
RETURNING *;

-- name: SetOrganizationPlan :one
UPDATE organizations
SET plan_tier = @plan_tier, updated_at = timezone('utc', now())
WHERE id = @id
RETURNING *;

-- name: GetMembership :one
SELECT uo.user_id, uo.organization_id, r.id AS role_id, r.code AS role_code, r.name AS role_name
FROM user_organizations uo
JOIN roles r ON r.id = uo.role_id
WHERE uo.user_id = @user_id AND uo.organization_id = @organization_id;

-- name: InsertMembership :exec
INSERT INTO user_organizations (user_id, organization_id, role_id, created_at, updated_at)
VALUES (@user_id, @organization_id, @role_id, timezone('utc', now()), timezone('utc', now()));

-- name: UpdateMembershipRole :execrows
UPDATE user_organizations
SET role_id = @role_id, updated_at = timezone('utc', now())
WHERE user_id = @user_id AND organization_id = @organization_id;

-- name: DeleteMembership :execrows
DELETE FROM user_organizations WHERE user_id = @user_id AND organization_id = @organization_id;

-- name: ListMembers :many
SELECT uo.user_id, uo.organization_id, r.id AS role_id, r.code AS role_code, r.name AS role_name
FROM user_organizations uo
JOIN roles r ON r.id = uo.role_id
WHERE uo.organization_id = @organization_id
ORDER BY uo.created_at, uo.user_id;

-- Cuántos miembros tiene un rol (para no borrar uno en uso con un error de FK).
-- name: CountMembersWithRole :one
SELECT count(*) FROM user_organizations WHERE role_id = @role_id;

-- name: CountMembers :one
SELECT count(*) FROM user_organizations WHERE organization_id = @organization_id;

-- Miembros que pueden administrar miembros: una organización nunca debe quedarse sin
-- ninguno.
-- name: CountOrgManagers :one
SELECT count(*)
FROM user_organizations uo
JOIN role_permissions rp ON rp.role_id = uo.role_id AND rp.permission_code = 'org.members.manage'
WHERE uo.organization_id = @organization_id;

-- Serializa los cambios de miembros de UNA organización (la clave es su id): dos
-- peticiones simultáneas que cada una "deja a uno" dejarían a cero.
-- name: LockOrganizationMembers :exec
SELECT pg_advisory_xact_lock(hashtextextended('org-members:' || @organization_id::text, 0));
