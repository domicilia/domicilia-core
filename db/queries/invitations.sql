-- Reinvitar a un correo con invitación pendiente la REEMPLAZA (nuevo token, nueva
-- vigencia, nuevo rol): así "reenviar" no deja dos enlaces vivos.
-- name: UpsertInvitation :one
INSERT INTO organization_invitations (organization_id, email, role_id, token_hash, invited_by, expires_at)
VALUES (@organization_id, @email, @role_id, @token_hash, sqlc.narg('invited_by'), @expires_at)
ON CONFLICT (organization_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL
DO UPDATE SET role_id = EXCLUDED.role_id, token_hash = EXCLUDED.token_hash,
              invited_by = EXCLUDED.invited_by, expires_at = EXCLUDED.expires_at,
              created_at = now()
RETURNING *;

-- name: GetInvitation :one
SELECT i.*, r.code AS role_code, r.name AS role_name
FROM organization_invitations i JOIN roles r ON r.id = i.role_id
WHERE i.id = @id AND i.organization_id = @organization_id;

-- El token se busca por su hash. La invitación solo es utilizable si sigue pendiente
-- y vigente; el servicio lo comprueba con la fila completa.
-- name: GetInvitationByTokenHash :one
SELECT i.*, r.code AS role_code, r.name AS role_name, o.name AS organization_name,
       o.slug AS organization_slug, o.status AS organization_status
FROM organization_invitations i
JOIN roles r ON r.id = i.role_id
JOIN organizations o ON o.id = i.organization_id
WHERE i.token_hash = @token_hash;

-- name: GetInvitationByTokenHashForUpdate :one
SELECT * FROM organization_invitations WHERE token_hash = @token_hash FOR UPDATE;

-- name: ListPendingInvitations :many
SELECT i.*, r.code AS role_code, r.name AS role_name
FROM organization_invitations i JOIN roles r ON r.id = i.role_id
WHERE i.organization_id = @organization_id AND i.accepted_at IS NULL AND i.revoked_at IS NULL
ORDER BY i.created_at DESC, i.id;

-- name: CountPendingInvitations :one
SELECT count(*) FROM organization_invitations
WHERE organization_id = @organization_id AND accepted_at IS NULL AND revoked_at IS NULL
  AND expires_at > now();

-- name: RevokeInvitation :execrows
UPDATE organization_invitations SET revoked_at = now()
WHERE id = @id AND organization_id = @organization_id
  AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: AcceptInvitation :execrows
UPDATE organization_invitations SET accepted_at = now(), accepted_by = @accepted_by
WHERE id = @id AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- Solo cuenta la que aún vale: una caducada no ocupa lugar y reinvitar la reemplaza.
-- name: PendingInvitationExists :one
SELECT EXISTS (
    SELECT 1 FROM organization_invitations
    WHERE organization_id = @organization_id AND email = @email
      AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now());
