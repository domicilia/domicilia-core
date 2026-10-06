-- Alta o actualización por teléfono, a prueba de carreras: dos mensajes simultáneos de una persona
-- nueva no chocan. El nombre del perfil de WhatsApp solo se usa si el contacto aún no tiene nombre.
-- name: UpsertContactByPhone :one
INSERT INTO contacts (organization_id, phone_e164, name, source, last_activity_at)
VALUES (@organization_id, @phone_e164, sqlc.narg('name'), @source, now())
ON CONFLICT (organization_id, phone_e164)
DO UPDATE SET name = COALESCE(contacts.name, EXCLUDED.name),
              last_activity_at = now(), updated_at = now()
RETURNING *;

-- name: InsertContact :one
INSERT INTO contacts (organization_id, phone_e164, name, email, custom_attributes, source)
VALUES (@organization_id, @phone_e164, sqlc.narg('name'), sqlc.narg('email'), @custom_attributes, @source)
RETURNING *;

-- name: GetContact :one
SELECT * FROM contacts WHERE id = @id AND organization_id = @organization_id;

-- Listado por creación (el más nuevo primero) con cursor. `search` ya viene escapado para LIKE.
-- name: ListContacts :many
SELECT * FROM contacts
WHERE organization_id = @organization_id
  AND (sqlc.narg('search')::text IS NULL
        OR name ILIKE '%' || sqlc.narg('search')::text || '%'
        OR phone_e164 LIKE '%' || sqlc.narg('search')::text || '%'
        OR email ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('before_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('before_at')::timestamptz, sqlc.narg('before_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- Cambio parcial: set_* distingue "no tocar" de "dejar en blanco".
-- name: UpdateContact :one
UPDATE contacts
SET name = CASE WHEN @set_name::boolean THEN sqlc.narg('name') ELSE name END,
    email = CASE WHEN @set_email::boolean THEN sqlc.narg('email') ELSE email END,
    custom_attributes = COALESCE(sqlc.narg('custom_attributes')::jsonb, custom_attributes),
    blocked = COALESCE(sqlc.narg('blocked')::boolean, blocked),
    updated_at = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;
