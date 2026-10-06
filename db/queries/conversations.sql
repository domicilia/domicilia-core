-- Número de conversación por organización (#1, #2...), sin huecos ni repetidos aunque haya
-- altas simultáneas: la fila del contador se bloquea dentro de la transacción del alta.
-- name: NextConversationDisplayID :one
INSERT INTO organization_counters (organization_id, conversation_seq) VALUES (@organization_id, 1)
ON CONFLICT (organization_id) DO UPDATE SET conversation_seq = organization_counters.conversation_seq + 1
RETURNING conversation_seq;

-- name: InsertConversation :one
INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id, last_activity_at)
VALUES (@organization_id, @inbox_id, @contact_id, @display_id, @last_activity_at)
ON CONFLICT (inbox_id, contact_id) WHERE status <> 'resolved' DO NOTHING
RETURNING *;

-- La conversación abierta (no resuelta) de un contacto en una bandeja, bloqueada para actualizarla.
-- name: GetActiveConversationForUpdate :one
SELECT * FROM conversations
WHERE inbox_id = @inbox_id AND contact_id = @contact_id AND organization_id = @organization_id AND status <> 'resolved'
FOR UPDATE;

-- channel_status es NULL si la bandeja ya no tiene número conectado (archivada).
-- name: GetConversation :one
SELECT c.*, ct.name AS contact_name, ct.phone_e164 AS contact_phone, ct.blocked AS contact_blocked,
       wc.status AS channel_status
FROM conversations c
JOIN contacts ct ON ct.id = c.contact_id
LEFT JOIN whatsapp_channels wc ON wc.inbox_id = c.inbox_id
WHERE c.id = @id AND c.organization_id = @organization_id;

-- name: GetConversationForUpdate :one
SELECT * FROM conversations WHERE id = @id AND organization_id = @organization_id FOR UPDATE;

-- Listado con cursor por actividad. `assignee_mode`: all | me | unassigned | user. `search` ya viene
-- escapado para LIKE.
-- name: ListConversations :many
SELECT c.*, ct.name AS contact_name, ct.phone_e164 AS contact_phone, ct.blocked AS contact_blocked,
       wc.status AS channel_status
FROM conversations c
JOIN contacts ct ON ct.id = c.contact_id
LEFT JOIN whatsapp_channels wc ON wc.inbox_id = c.inbox_id
WHERE c.organization_id = @organization_id
  AND (sqlc.narg('inbox_id')::uuid IS NULL OR c.inbox_id = sqlc.narg('inbox_id')::uuid)
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (@assignee_mode::text = 'all'
        OR (@assignee_mode::text = 'unassigned' AND c.assignee_id IS NULL)
        OR (@assignee_mode::text IN ('me', 'user') AND c.assignee_id = sqlc.narg('assignee_id')::uuid))
  AND (sqlc.narg('search')::text IS NULL
        OR ct.name ILIKE '%' || sqlc.narg('search')::text || '%'
        OR ct.phone_e164 LIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('before_at')::timestamptz IS NULL
        OR (c.last_activity_at, c.id) < (sqlc.narg('before_at')::timestamptz, sqlc.narg('before_id')::uuid))
ORDER BY c.last_activity_at DESC, c.id DESC
LIMIT @page_size;

-- Pestañas de la bandeja: mías, sin asignar, todas.
-- name: CountConversationTabs :one
SELECT count(*) FILTER (WHERE assignee_id = sqlc.narg('user_id')::uuid) AS mine,
       count(*) FILTER (WHERE assignee_id IS NULL) AS unassigned,
       count(*) AS total
FROM conversations
WHERE organization_id = @organization_id
  AND (sqlc.narg('inbox_id')::uuid IS NULL OR inbox_id = sqlc.narg('inbox_id')::uuid)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text);

-- Cambio parcial de una conversación.
-- name: UpdateConversation :one
UPDATE conversations
SET status = COALESCE(sqlc.narg('status')::text, status),
    priority = CASE WHEN @set_priority::boolean THEN sqlc.narg('priority')::text ELSE priority END,
    assignee_id = CASE WHEN @set_assignee::boolean THEN sqlc.narg('assignee_id')::uuid ELSE assignee_id END,
    handled_by = COALESCE(sqlc.narg('handled_by')::text, handled_by),
    snoozed_until = CASE WHEN @set_snoozed::boolean THEN sqlc.narg('snoozed_until')::timestamptz ELSE snoozed_until END,
    updated_at = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;

-- name: MarkConversationRead :exec
UPDATE conversations SET unread_count = 0, updated_at = now()
WHERE id = @id AND organization_id = @organization_id AND unread_count <> 0;

-- Un mensaje del cliente: abre la ventana de 24 h, sube los no leídos y reabre lo pendiente o pospuesto.
-- Una entrega atrasada (Meta reintenta) nunca hace retroceder la ventana ni la vista previa.
-- name: TouchConversationInbound :exec
UPDATE conversations
SET last_customer_message_at = GREATEST(last_customer_message_at, @at),
    last_activity_at = GREATEST(last_activity_at, @at),
    last_message_preview = CASE WHEN last_message_preview IS NULL OR @at >= last_activity_at THEN @preview ELSE last_message_preview END,
    last_message_direction = CASE WHEN last_message_direction IS NULL OR @at >= last_activity_at THEN 'inbound' ELSE last_message_direction END,
    unread_count = unread_count + 1,
    waiting_since = COALESCE(waiting_since, @at),
    status = CASE WHEN status IN ('pending', 'snoozed') THEN 'open' ELSE status END,
    snoozed_until = CASE WHEN status IN ('pending', 'snoozed') THEN NULL ELSE snoozed_until END,
    updated_at = now()
WHERE id = @id AND organization_id = @organization_id;

-- Una respuesta del equipo: ya no se está esperando al agente.
-- name: TouchConversationOutbound :exec
UPDATE conversations
SET last_activity_at = @at, last_message_preview = @preview, last_message_direction = 'outbound',
    first_response_at = COALESCE(first_response_at, @at),
    waiting_since = NULL, updated_at = now()
WHERE id = @id AND organization_id = @organization_id;

-- ---------------------------------------------------------------------------
-- Mensajes
-- ---------------------------------------------------------------------------

-- Entrante. Si el wamid ya existe (reentrega de Meta) no inserta y no devuelve fila.
-- name: InsertInboundMessage :one
INSERT INTO messages (organization_id, conversation_id, inbox_id, direction, kind, body, wamid, status, payload,
                      reply_to_message_id, created_at)
VALUES (@organization_id, @conversation_id, @inbox_id, 'inbound', @kind, sqlc.narg('body'), @wamid, 'received', @payload,
        sqlc.narg('reply_to_message_id'), @created_at)
ON CONFLICT (inbox_id, wamid) WHERE wamid IS NOT NULL DO NOTHING
RETURNING *;

-- name: InsertOutboundMessage :one
INSERT INTO messages (organization_id, conversation_id, inbox_id, direction, kind, body, status, next_attempt_at,
                      sender_user_id, reply_to_message_id)
VALUES (@organization_id, @conversation_id, @inbox_id, 'outbound', @kind, @body, 'queued', now(),
        sqlc.narg('sender_user_id'), sqlc.narg('reply_to_message_id'))
RETURNING *;

-- name: InsertMessageAttachment :exec
INSERT INTO message_attachments (organization_id, message_id, kind, mime_type, filename, size_bytes, sha256, wa_media_id, caption)
VALUES (@organization_id, @message_id, @kind, sqlc.narg('mime_type'), sqlc.narg('filename'), sqlc.narg('size_bytes'),
        sqlc.narg('sha256'), sqlc.narg('wa_media_id'), sqlc.narg('caption'));

-- name: GetMessageByWamid :one
SELECT * FROM messages WHERE inbox_id = @inbox_id AND wamid = @wamid;

-- name: GetMessage :one
SELECT * FROM messages WHERE id = @id AND organization_id = @organization_id;

-- Hilo de una conversación, del más nuevo al más viejo, con cursor.
-- name: ListMessages :many
SELECT * FROM messages
WHERE conversation_id = @conversation_id AND organization_id = @organization_id
  AND (sqlc.narg('before_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('before_at')::timestamptz, sqlc.narg('before_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: ListAttachmentsForMessages :many
SELECT * FROM message_attachments WHERE message_id = ANY(@message_ids::uuid[]) ORDER BY created_at, id;

-- ---------------------------------------------------------------------------
-- Cola de salida
-- ---------------------------------------------------------------------------

-- Reclama mensajes listos para enviar con un arrendamiento: dos trabajadores nunca toman el mismo
-- mensaje, y si uno muere el arrendamiento vence y otro lo retoma.
-- name: ClaimOutbox :many
WITH due AS (
    SELECT m.id FROM messages m
    WHERE m.direction = 'outbound' AND m.status = 'queued'
      AND m.next_attempt_at <= now() AND (m.locked_until IS NULL OR m.locked_until < now())
      -- Solo el mensaje más viejo de cada conversación: se entregan EN ORDEN y nunca dos a la vez.
      AND NOT EXISTS (SELECT 1 FROM messages e
                      WHERE e.conversation_id = m.conversation_id AND e.direction = 'outbound'
                        AND e.status = 'queued' AND (e.created_at, e.id) < (m.created_at, m.id))
    ORDER BY m.next_attempt_at, m.created_at
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
)
UPDATE messages m
SET locked_until = now() + make_interval(secs => @lease_seconds::int), attempts = m.attempts + 1
FROM due WHERE m.id = due.id
RETURNING m.*;

-- name: MarkMessageSent :exec
UPDATE messages
SET status = CASE WHEN status = 'queued' THEN 'sent' ELSE status END,
    wamid = @wamid, sent_at = COALESCE(sent_at, now()), locked_until = NULL, next_attempt_at = NULL,
    error_code = NULL, error_detail = NULL
WHERE id = @id AND organization_id = @organization_id;

-- name: MarkMessageFailed :exec
UPDATE messages
SET status = 'failed', failed_at = now(), locked_until = NULL, next_attempt_at = NULL,
    error_code = @error_code, error_detail = @error_detail
WHERE id = @id AND organization_id = @organization_id AND status IN ('queued');

-- name: ScheduleMessageRetry :exec
UPDATE messages
SET next_attempt_at = @next_attempt_at, locked_until = NULL, error_code = @error_code, error_detail = @error_detail
WHERE id = @id AND organization_id = @organization_id AND status = 'queued';

-- Estado de un mensaje nuestro que llegó por webhook. Solo AVANZA: queued < sent < delivered < read;
-- "failed" puede llegar mientras no se haya entregado; nada retrocede (Meta no garantiza el orden).
-- name: ApplyMessageStatus :execrows
UPDATE messages
SET status = @status,
    sent_at = CASE WHEN @status IN ('sent', 'delivered', 'read') THEN COALESCE(sent_at, @at) ELSE sent_at END,
    delivered_at = CASE WHEN @status IN ('delivered', 'read') THEN COALESCE(delivered_at, @at) ELSE delivered_at END,
    read_at = CASE WHEN @status = 'read' THEN COALESCE(read_at, @at) ELSE read_at END,
    failed_at = CASE WHEN @status = 'failed' THEN COALESCE(failed_at, @at) ELSE failed_at END,
    error_code = CASE WHEN @status = 'failed' THEN sqlc.narg('error_code') ELSE error_code END,
    error_detail = CASE WHEN @status = 'failed' THEN sqlc.narg('error_detail') ELSE error_detail END,
    pricing_category = COALESCE(sqlc.narg('pricing_category'), pricing_category),
    billable = COALESCE(sqlc.narg('billable'), billable),
    locked_until = NULL, next_attempt_at = NULL
WHERE inbox_id = @inbox_id AND wamid = @wamid AND direction = 'outbound'
  AND (
      (@status IN ('sent', 'delivered', 'read') AND
        CASE status WHEN 'queued' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 99 END
        < CASE @status::text WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 0 END)
      OR (@status = 'failed' AND status IN ('queued', 'sent'))
  );

-- name: InsertUnmatchedStatus :exec
INSERT INTO unmatched_statuses (wamid, status, occurred_at, error_code, error_detail, pricing_category, billable)
VALUES (@wamid, @status, @occurred_at, sqlc.narg('error_code'), sqlc.narg('error_detail'), sqlc.narg('pricing_category'), sqlc.narg('billable'));

-- name: ListUnmatchedStatuses :many
SELECT * FROM unmatched_statuses WHERE wamid = @wamid ORDER BY occurred_at, created_at;

-- name: DeleteUnmatchedStatuses :exec
DELETE FROM unmatched_statuses WHERE wamid = @wamid;

-- name: PruneUnmatchedStatuses :execrows
DELETE FROM unmatched_statuses WHERE created_at < now() - interval '1 day';

-- ---------------------------------------------------------------------------
-- Webhooks crudos
-- ---------------------------------------------------------------------------

-- Registra la entrega o devuelve la ya registrada (misma clave): así se sabe si es nueva o reentrega.
-- name: UpsertWebhookEvent :one
INSERT INTO webhook_events (source, event_key, payload) VALUES (@source, @event_key, @payload)
ON CONFLICT (source, event_key) DO UPDATE SET source = webhook_events.source
RETURNING id, processed_at, (xmax = 0) AS inserted;

-- name: MarkWebhookProcessed :exec
UPDATE webhook_events SET processed_at = now(), error = NULL WHERE id = @id;

-- name: MarkWebhookFailed :exec
UPDATE webhook_events SET error = @error WHERE id = @id;

-- name: PruneWebhookEvents :execrows
DELETE FROM webhook_events WHERE received_at < now() - interval '30 days';

-- ¿Puede este usuario atender conversaciones de la organización? (es miembro y su rol tiene org.inbox.reply,
-- o es de la plataforma con acceso a todas). Es lo que se exige a quien se le asigna una conversación.
-- name: UserCanReplyInOrganization :one
SELECT EXISTS (
    SELECT 1 FROM user_organizations uo
    JOIN role_permissions rp ON rp.role_id = uo.role_id AND rp.permission_code = 'org.inbox.reply'
    JOIN users u ON u.id = uo.user_id AND u.is_active
    WHERE uo.user_id = @user_id AND uo.organization_id = @organization_id
);
