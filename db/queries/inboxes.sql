-- name: InsertWhatsAppAccount :one
INSERT INTO whatsapp_accounts (id, organization_id, waba_id, access_token_enc, app_secret_enc, connected_by)
VALUES (@id, @organization_id, @waba_id, @access_token_enc, sqlc.narg('app_secret_enc'), sqlc.narg('connected_by'))
RETURNING *;

-- name: InsertInbox :one
INSERT INTO inboxes (id, organization_id, name) VALUES (@id, @organization_id, @name)
RETURNING *;

-- name: InsertWhatsAppChannel :one
INSERT INTO whatsapp_channels (inbox_id, organization_id, whatsapp_account_id, phone_number_id, display_phone,
                               verified_name, quality_rating, messaging_tier, status, last_checked_at)
VALUES (@inbox_id, @organization_id, @whatsapp_account_id, @phone_number_id, @display_phone,
        sqlc.narg('verified_name'), sqlc.narg('quality_rating'), sqlc.narg('messaging_tier'), 'connected', now())
RETURNING *;

-- Bandeja con su número y su cuenta, SIN secretos.
-- name: ListInboxes :many
SELECT i.id, i.organization_id, i.name, i.channel_type, i.archived_at, i.created_at, i.updated_at,
       c.phone_number_id, c.display_phone, c.verified_name, c.quality_rating, c.messaging_tier,
       c.status AS channel_status, c.last_checked_at, a.waba_id, a.token_status
FROM inboxes i
LEFT JOIN whatsapp_channels c ON c.inbox_id = i.id
LEFT JOIN whatsapp_accounts a ON a.id = c.whatsapp_account_id
WHERE i.organization_id = @organization_id
  AND (@include_archived::boolean OR i.archived_at IS NULL)
ORDER BY i.name, i.id;

-- name: GetInbox :one
SELECT i.id, i.organization_id, i.name, i.channel_type, i.archived_at, i.created_at, i.updated_at,
       c.phone_number_id, c.display_phone, c.verified_name, c.quality_rating, c.messaging_tier,
       c.status AS channel_status, c.last_checked_at, a.waba_id, a.token_status
FROM inboxes i
LEFT JOIN whatsapp_channels c ON c.inbox_id = i.id
LEFT JOIN whatsapp_accounts a ON a.id = c.whatsapp_account_id
WHERE i.id = @id AND i.organization_id = @organization_id;

-- name: CountActiveInboxes :one
SELECT count(*) FROM inboxes WHERE organization_id = @organization_id AND archived_at IS NULL;

-- name: RenameInbox :one
UPDATE inboxes SET name = @name, updated_at = now()
WHERE id = @id AND organization_id = @organization_id AND archived_at IS NULL
RETURNING *;

-- name: ArchiveInbox :execrows
UPDATE inboxes SET archived_at = now(), updated_at = now()
WHERE id = @id AND organization_id = @organization_id AND archived_at IS NULL;

-- Desconectar libera el número (phone_number_id es único): se puede volver a conectar.
-- name: DeleteWhatsAppChannel :exec
DELETE FROM whatsapp_channels WHERE inbox_id = @inbox_id AND organization_id = @organization_id;

-- name: DeleteOrphanWhatsAppAccounts :exec
DELETE FROM whatsapp_accounts a
WHERE a.organization_id = @organization_id
  AND NOT EXISTS (SELECT 1 FROM whatsapp_channels c WHERE c.whatsapp_account_id = a.id);

-- Lo necesario para hablar con Meta en nombre de una bandeja: el token CIFRADO y la cuenta.
-- name: GetChannelCredentials :one
SELECT c.inbox_id, c.organization_id, c.phone_number_id, c.status AS channel_status,
       a.id AS account_id, a.waba_id, a.access_token_enc, a.app_secret_enc, a.token_status
FROM whatsapp_channels c
JOIN whatsapp_accounts a ON a.id = c.whatsapp_account_id
JOIN inboxes i ON i.id = c.inbox_id
WHERE c.inbox_id = @inbox_id AND c.organization_id = @organization_id AND i.archived_at IS NULL;

-- Enrutamiento del webhook: de qué organización y bandeja es un mensaje, por el número al que llegó.
-- name: GetChannelByPhoneNumberID :one
SELECT c.inbox_id, c.organization_id, c.phone_number_id, c.status AS channel_status,
       a.id AS account_id, a.app_secret_enc, o.status AS organization_status
FROM whatsapp_channels c
JOIN whatsapp_accounts a ON a.id = c.whatsapp_account_id
JOIN inboxes i ON i.id = c.inbox_id
JOIN organizations o ON o.id = c.organization_id
WHERE c.phone_number_id = @phone_number_id AND i.archived_at IS NULL;

-- name: UpdateAccountToken :exec
UPDATE whatsapp_accounts
SET access_token_enc = @access_token_enc, token_status = 'valid', updated_at = now()
WHERE id = @id AND organization_id = @organization_id;

-- name: SetAccountTokenStatus :exec
UPDATE whatsapp_accounts SET token_status = @token_status, updated_at = now()
WHERE id = @id AND organization_id = @organization_id;

-- name: RefreshChannelInfo :exec
UPDATE whatsapp_channels
SET display_phone = @display_phone, verified_name = sqlc.narg('verified_name'),
    quality_rating = sqlc.narg('quality_rating'), messaging_tier = sqlc.narg('messaging_tier'),
    status = 'connected', last_checked_at = now(), updated_at = now()
WHERE inbox_id = @inbox_id AND organization_id = @organization_id;

-- name: SetChannelStatus :exec
UPDATE whatsapp_channels SET status = @status, updated_at = now()
WHERE inbox_id = @inbox_id AND organization_id = @organization_id;
