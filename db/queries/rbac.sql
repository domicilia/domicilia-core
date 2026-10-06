-- ---------------------------------------------------------------------------
-- Roles
-- ---------------------------------------------------------------------------

-- name: GetRole :one
SELECT * FROM roles WHERE id = @id;

-- Rol de sistema (sin organización) por código y alcance.
-- name: GetSystemRoleByCode :one
SELECT * FROM roles WHERE organization_id IS NULL AND scope = @scope AND code = @code;

-- Rol personalizado de una organización por código.
-- name: GetOrgRoleByCode :one
SELECT * FROM roles WHERE organization_id = @organization_id AND code = @code;

-- Todos los roles, con filtros opcionales (consola de la plataforma).
-- name: ListRoles :many
SELECT * FROM roles
WHERE (sqlc.narg('scope')::text IS NULL OR scope = sqlc.narg('scope')::text)
  AND (sqlc.narg('organization_id')::uuid IS NULL OR organization_id = sqlc.narg('organization_id')::uuid)
ORDER BY scope, is_system DESC, code;

-- Los roles que se pueden dar en una organización: los de sistema de alcance
-- organización más los personalizados de esa organización.
-- name: ListRolesForOrganization :many
SELECT * FROM roles
WHERE scope = 'organization' AND (organization_id IS NULL OR organization_id = @organization_id)
ORDER BY is_system DESC, code;

-- name: InsertRole :one
INSERT INTO roles (code, name, description, scope, organization_id, is_system, org_assignable)
VALUES (@code, @name, sqlc.narg('description'), @scope, sqlc.narg('organization_id'), FALSE, @org_assignable)
RETURNING *;

-- Solo los personalizados: los de sistema son inmutables.
-- name: UpdateRole :one
UPDATE roles
SET name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    org_assignable = COALESCE(sqlc.narg('org_assignable'), org_assignable),
    updated_at = now()
WHERE id = @id AND NOT is_system
RETURNING *;

-- name: DeleteRole :execrows
DELETE FROM roles WHERE id = @id AND NOT is_system;

-- ---------------------------------------------------------------------------
-- Permisos de un rol
-- ---------------------------------------------------------------------------

-- name: ListPermissions :many
SELECT * FROM permissions ORDER BY scope, code;

-- name: ListRolePermissions :many
SELECT role_id, permission_code FROM role_permissions
WHERE role_id = ANY(@role_ids::uuid[])
ORDER BY role_id, permission_code;

-- name: InsertRolePermissions :exec
INSERT INTO role_permissions (role_id, permission_code)
SELECT @role_id, unnest(@permission_codes::text[]);

-- name: DeleteRolePermissions :exec
DELETE FROM role_permissions WHERE role_id = @role_id;

-- ---------------------------------------------------------------------------
-- Roles de plataforma de un usuario
-- ---------------------------------------------------------------------------

-- name: ListUserPlatformRoles :many
SELECT r.id AS role_id, r.code AS role_code, r.name AS role_name
FROM user_platform_roles upr
JOIN roles r ON r.id = upr.role_id
WHERE upr.user_id = @user_id
ORDER BY r.code;

-- name: ListPlatformRolesForUsers :many
SELECT upr.user_id, r.id AS role_id, r.code AS role_code, r.name AS role_name
FROM user_platform_roles upr
JOIN roles r ON r.id = upr.role_id
WHERE upr.user_id = ANY(@user_ids::uuid[])
ORDER BY upr.user_id, r.code;

-- Idempotente: dar dos veces el mismo rol no es un error.
-- name: InsertUserPlatformRole :execrows
INSERT INTO user_platform_roles (user_id, role_id, granted_by)
VALUES (@user_id, @role_id, sqlc.narg('granted_by'))
ON CONFLICT (user_id, role_id) DO NOTHING;

-- Lo mismo por código de rol de sistema (altas de cliente y de domiciliario).
-- name: InsertUserPlatformRoleByCode :execrows
INSERT INTO user_platform_roles (user_id, role_id, granted_by)
SELECT @user_id::uuid, r.id, sqlc.narg('granted_by')::uuid
FROM roles r
WHERE r.scope = 'platform' AND r.organization_id IS NULL AND r.code = @code
ON CONFLICT (user_id, role_id) DO NOTHING;

-- name: DeleteUserPlatformRole :execrows
DELETE FROM user_platform_roles WHERE user_id = @user_id AND role_id = @role_id;

-- Usuarios ACTIVOS con un rol de plataforma: para no quedarse sin superadmin.
-- name: CountActiveUsersWithPlatformRole :one
SELECT count(*) FROM user_platform_roles upr
JOIN roles r ON r.id = upr.role_id
JOIN users u ON u.id = upr.user_id
WHERE r.scope = 'platform' AND r.organization_id IS NULL AND r.code = @code AND u.is_active;

-- name: CountUsersWithPlatformRole :one
SELECT count(*) FROM user_platform_roles upr
JOIN roles r ON r.id = upr.role_id
WHERE r.scope = 'platform' AND r.organization_id IS NULL AND r.code = @code;

-- name: CountUsersWithPlatformRoleID :one
SELECT count(*) FROM user_platform_roles WHERE role_id = @role_id;

-- ---------------------------------------------------------------------------
-- Acceso efectivo de un usuario (lo carga identity en cada petición)
-- ---------------------------------------------------------------------------

-- name: ListUserPlatformPermissions :many
SELECT DISTINCT rp.permission_code
FROM user_platform_roles upr
JOIN role_permissions rp ON rp.role_id = upr.role_id
WHERE upr.user_id = @user_id;

-- name: ListUserMembershipAccess :many
SELECT uo.organization_id, r.id AS role_id, r.code AS role_code, r.name AS role_name,
       COALESCE(array_agg(rp.permission_code ORDER BY rp.permission_code)
                FILTER (WHERE rp.permission_code IS NOT NULL), '{}')::text[] AS permissions
FROM user_organizations uo
JOIN roles r ON r.id = uo.role_id
LEFT JOIN role_permissions rp ON rp.role_id = r.id
WHERE uo.user_id = @user_id
GROUP BY uo.organization_id, r.id, r.code, r.name;

-- ---------------------------------------------------------------------------
-- Auditoría
-- ---------------------------------------------------------------------------

-- name: InsertAudit :exec
INSERT INTO audit_log (actor_id, action, target_user_id, organization_id, role_id, role_code, detail)
VALUES (sqlc.narg('actor_id'), @action, sqlc.narg('target_user_id'), sqlc.narg('organization_id'),
        sqlc.narg('role_id'), sqlc.narg('role_code'), sqlc.narg('detail'));

-- name: ListAudit :many
SELECT * FROM audit_log
WHERE (sqlc.narg('organization_id')::uuid IS NULL OR organization_id = sqlc.narg('organization_id')::uuid)
  AND (sqlc.narg('target_user_id')::uuid IS NULL OR target_user_id = sqlc.narg('target_user_id')::uuid)
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action')::text)
ORDER BY created_at DESC, id
LIMIT @page_size OFFSET @page_offset;

-- name: CountAudit :one
SELECT count(*) FROM audit_log
WHERE (sqlc.narg('organization_id')::uuid IS NULL OR organization_id = sqlc.narg('organization_id')::uuid)
  AND (sqlc.narg('target_user_id')::uuid IS NULL OR target_user_id = sqlc.narg('target_user_id')::uuid)
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action')::text);

-- Serializa los cambios que pueden dejar la plataforma sin superadmin: dos
-- operaciones simultáneas que cada una "deja a uno" dejarían a cero.
-- name: LockSuperadmins :exec
SELECT pg_advisory_xact_lock(7101001);
