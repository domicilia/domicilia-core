-- Bandeja de WhatsApp: cuentas y números conectados, contactos, conversaciones y mensajes.
-- Diseño en docs/whatsapp.md (sección 2).
--
-- Aislamiento entre inquilinos EN LA BASE, no solo en el código: cada tabla hija lleva
-- organization_id y su clave foránea es COMPUESTA (id, organization_id) hacia el padre.
-- Una fila no puede apuntar a una conversación, un contacto o una bandeja de OTRA
-- organización aunque un bug del código lo intente.

-- +goose Up

-- Contador por organización: el número de conversación que ve el equipo (#1, #2...).
CREATE TABLE organization_counters (
    organization_id  uuid   NOT NULL,
    conversation_seq bigint NOT NULL DEFAULT 0,
    CONSTRAINT organization_counters_pkey PRIMARY KEY (organization_id),
    CONSTRAINT organization_counters_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE
);

-- ---------------------------------------------------------------------------
-- Bandejas y canal de WhatsApp
-- ---------------------------------------------------------------------------

CREATE TABLE inboxes (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL,
    name            text        NOT NULL,
    channel_type    text        NOT NULL DEFAULT 'whatsapp',
    archived_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inboxes_pkey PRIMARY KEY (id),
    CONSTRAINT inboxes_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT inboxes_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT inboxes_channel_type_check CHECK (channel_type IN ('whatsapp')),
    CONSTRAINT inboxes_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 100)
);
CREATE UNIQUE INDEX inboxes_organization_name_key ON inboxes (organization_id, lower(name)) WHERE archived_at IS NULL;

-- Una cuenta de WhatsApp Business (WABA). waba_id es ÚNICO en toda la plataforma: una cuenta
-- solo puede pertenecer a UNA organización, así nadie "reclama" la cuenta de otro.
CREATE TABLE whatsapp_accounts (
    id               uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id  uuid        NOT NULL,
    waba_id          text        NOT NULL,
    -- Cifrados con AES-GCM (internal/platform/secretbox): nunca en claro.
    access_token_enc text        NOT NULL,
    app_secret_enc   text,
    token_status     text        NOT NULL DEFAULT 'valid',
    connected_by     uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT whatsapp_accounts_pkey PRIMARY KEY (id),
    CONSTRAINT whatsapp_accounts_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT whatsapp_accounts_waba_id_key UNIQUE (waba_id),
    CONSTRAINT whatsapp_accounts_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT whatsapp_accounts_connected_by_fkey FOREIGN KEY (connected_by) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT whatsapp_accounts_token_status_check CHECK (token_status IN ('valid', 'invalid'))
);

-- El número de una bandeja. phone_number_id es la llave de enrutamiento del webhook: Meta no dice
-- de qué organización es un mensaje, solo a qué número llegó. Único en toda la plataforma.
CREATE TABLE whatsapp_channels (
    inbox_id            uuid        NOT NULL,
    organization_id     uuid        NOT NULL,
    whatsapp_account_id uuid        NOT NULL,
    phone_number_id     text        NOT NULL,
    display_phone       text        NOT NULL,
    verified_name       text,
    quality_rating      text,
    messaging_tier      text,
    status              text        NOT NULL DEFAULT 'connected',
    last_checked_at     timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT whatsapp_channels_pkey PRIMARY KEY (inbox_id),
    CONSTRAINT whatsapp_channels_phone_number_id_key UNIQUE (phone_number_id),
    CONSTRAINT whatsapp_channels_inbox_fkey FOREIGN KEY (inbox_id, organization_id) REFERENCES inboxes (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT whatsapp_channels_account_fkey FOREIGN KEY (whatsapp_account_id, organization_id) REFERENCES whatsapp_accounts (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT whatsapp_channels_status_check CHECK (status IN ('connected', 'needs_reauth'))
);
CREATE INDEX ix_whatsapp_channels_account ON whatsapp_channels (whatsapp_account_id);

-- ---------------------------------------------------------------------------
-- Contactos (por organización: el mismo teléfono puede ser contacto de varios negocios)
-- ---------------------------------------------------------------------------

CREATE TABLE contacts (
    id                uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id   uuid        NOT NULL,
    phone_e164        text        NOT NULL,
    name              text,
    email             text,
    customer_user_id  uuid,
    custom_attributes jsonb       NOT NULL DEFAULT '{}'::jsonb,
    blocked           boolean     NOT NULL DEFAULT false,
    source            text        NOT NULL,
    last_activity_at  timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_pkey PRIMARY KEY (id),
    CONSTRAINT contacts_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT contacts_organization_phone_key UNIQUE (organization_id, phone_e164),
    CONSTRAINT contacts_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT contacts_customer_user_id_fkey FOREIGN KEY (customer_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT contacts_phone_e164_check CHECK (phone_e164 ~ '^\+[1-9][0-9]{6,14}$'),
    CONSTRAINT contacts_source_check CHECK (source IN ('inbound', 'manual', 'import', 'order', 'api')),
    CONSTRAINT contacts_attributes_check CHECK (jsonb_typeof(custom_attributes) = 'object')
);
CREATE INDEX ix_contacts_organization_activity ON contacts (organization_id, last_activity_at DESC NULLS LAST, id);

-- ---------------------------------------------------------------------------
-- Conversaciones
-- ---------------------------------------------------------------------------

CREATE TABLE conversations (
    id                       uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id          uuid        NOT NULL,
    inbox_id                 uuid        NOT NULL,
    contact_id               uuid        NOT NULL,
    display_id               bigint      NOT NULL,
    status                   text        NOT NULL DEFAULT 'open',
    priority                 text,
    assignee_id              uuid,
    handled_by               text        NOT NULL DEFAULT 'human',
    -- Fin de la ventana de 24 h = last_customer_message_at + 24 h. Se guarda, no se calcula
    -- mirando mensajes: es lo que decide si se puede responder con texto libre.
    last_customer_message_at timestamptz,
    last_activity_at         timestamptz NOT NULL DEFAULT now(),
    last_message_preview     text,
    last_message_direction   text,
    first_response_at        timestamptz,
    waiting_since            timestamptz,
    snoozed_until            timestamptz,
    unread_count             integer     NOT NULL DEFAULT 0,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conversations_pkey PRIMARY KEY (id),
    CONSTRAINT conversations_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT conversations_display_key UNIQUE (organization_id, display_id),
    CONSTRAINT conversations_inbox_fkey FOREIGN KEY (inbox_id, organization_id) REFERENCES inboxes (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT conversations_contact_fkey FOREIGN KEY (contact_id, organization_id) REFERENCES contacts (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT conversations_assignee_id_fkey FOREIGN KEY (assignee_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT conversations_status_check CHECK (status IN ('open', 'pending', 'resolved', 'snoozed')),
    CONSTRAINT conversations_priority_check CHECK (priority IS NULL OR priority IN ('low', 'medium', 'high', 'urgent')),
    CONSTRAINT conversations_handled_by_check CHECK (handled_by IN ('bot', 'human')),
    CONSTRAINT conversations_unread_check CHECK (unread_count >= 0)
);
-- Una sola conversación NO resuelta por contacto y bandeja: dos mensajes simultáneos de alguien nuevo
-- no crean dos conversaciones (el segundo choca con este índice y reutiliza la primera).
CREATE UNIQUE INDEX conversations_one_active_key ON conversations (inbox_id, contact_id) WHERE status <> 'resolved';
CREATE INDEX ix_conversations_list ON conversations (organization_id, inbox_id, status, last_activity_at DESC, id DESC);
CREATE INDEX ix_conversations_assignee ON conversations (organization_id, assignee_id, status, last_activity_at DESC) WHERE assignee_id IS NOT NULL;
CREATE INDEX ix_conversations_contact ON conversations (contact_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Mensajes (entrantes y salientes; los salientes nacen "queued" y los envía un trabajador)
-- ---------------------------------------------------------------------------

CREATE TABLE messages (
    id                  uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id     uuid        NOT NULL,
    conversation_id     uuid        NOT NULL,
    inbox_id            uuid        NOT NULL,
    direction           text        NOT NULL,
    kind                text        NOT NULL,
    body                text,
    wamid               text,
    status              text        NOT NULL,
    error_code          text,
    error_detail        text,
    attempts            integer     NOT NULL DEFAULT 0,
    next_attempt_at     timestamptz,
    -- Arrendamiento: quien reclama un mensaje para enviarlo lo bloquea hasta este instante;
    -- si el trabajador muere, vence y otro lo retoma.
    locked_until        timestamptz,
    sent_at             timestamptz,
    delivered_at        timestamptz,
    read_at             timestamptz,
    failed_at           timestamptz,
    sender_user_id      uuid,
    reply_to_message_id uuid,
    payload             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- Lo que Meta dice haber cobrado: base del libro de uso.
    pricing_category    text,
    billable            boolean,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_pkey PRIMARY KEY (id),
    CONSTRAINT messages_conversation_fkey FOREIGN KEY (conversation_id, organization_id) REFERENCES conversations (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT messages_inbox_fkey FOREIGN KEY (inbox_id, organization_id) REFERENCES inboxes (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT messages_sender_user_id_fkey FOREIGN KEY (sender_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT messages_reply_to_fkey FOREIGN KEY (reply_to_message_id) REFERENCES messages (id) ON DELETE SET NULL,
    CONSTRAINT messages_direction_check CHECK (direction IN ('inbound', 'outbound')),
    CONSTRAINT messages_kind_check CHECK (kind IN ('text', 'image', 'audio', 'video', 'document', 'sticker', 'location',
        'contacts', 'interactive', 'button', 'template', 'reaction', 'unsupported')),
    CONSTRAINT messages_status_check CHECK (status IN ('received', 'queued', 'sent', 'delivered', 'read', 'failed')),
    CONSTRAINT messages_direction_status_check CHECK (
        (direction = 'inbound' AND status = 'received') OR (direction = 'outbound' AND status <> 'received')),
    CONSTRAINT messages_body_length_check CHECK (body IS NULL OR char_length(body) <= 4096)
);
-- El mismo mensaje entrante procesado dos veces (Meta reintenta) no se duplica.
CREATE UNIQUE INDEX messages_inbox_wamid_key ON messages (inbox_id, wamid) WHERE wamid IS NOT NULL;
CREATE INDEX ix_messages_conversation ON messages (conversation_id, created_at DESC, id DESC);
CREATE INDEX ix_messages_outbox ON messages (next_attempt_at) WHERE direction = 'outbound' AND status = 'queued';

CREATE TABLE message_attachments (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL,
    message_id      uuid        NOT NULL,
    kind            text        NOT NULL,
    mime_type       text,
    filename        text,
    size_bytes      bigint,
    sha256          text,
    -- Id del archivo en Meta: la URL de descarga se pide aparte y caduca.
    wa_media_id     text,
    caption         text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT message_attachments_pkey PRIMARY KEY (id),
    CONSTRAINT message_attachments_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT message_attachments_message_id_fkey FOREIGN KEY (message_id) REFERENCES messages (id) ON DELETE CASCADE,
    CONSTRAINT message_attachments_kind_check CHECK (kind IN ('image', 'audio', 'video', 'document', 'sticker'))
);
CREATE INDEX ix_message_attachments_message ON message_attachments (message_id);

-- Estados que llegaron ANTES de que el envío registrara su wamid (carrera rara pero real):
-- se aplican cuando el mensaje recibe su id.
CREATE TABLE unmatched_statuses (
    id               uuid        NOT NULL DEFAULT gen_random_uuid(),
    wamid            text        NOT NULL,
    status           text        NOT NULL,
    occurred_at      timestamptz NOT NULL,
    error_code       text,
    error_detail     text,
    pricing_category text,
    billable         boolean,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT unmatched_statuses_pkey PRIMARY KEY (id)
);
CREATE INDEX ix_unmatched_statuses_wamid ON unmatched_statuses (wamid);
CREATE INDEX ix_unmatched_statuses_created ON unmatched_statuses (created_at);

-- Cada entrega de Meta, cruda. event_key = sha-256 del cuerpo: una reentrega idéntica no se
-- procesa dos veces. Se poda a los 30 días (contiene teléfonos y texto de clientes).
CREATE TABLE webhook_events (
    id           uuid        NOT NULL DEFAULT gen_random_uuid(),
    source       text        NOT NULL DEFAULT 'whatsapp',
    event_key    bytea       NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    error        text,
    payload      jsonb       NOT NULL,
    CONSTRAINT webhook_events_pkey PRIMARY KEY (id),
    CONSTRAINT webhook_events_key UNIQUE (source, event_key)
);
CREATE INDEX ix_webhook_events_received ON webhook_events (received_at);

-- ---------------------------------------------------------------------------
-- Permisos. El admin recibe todos; el empleado (call center) atiende y gestiona contactos.
-- ---------------------------------------------------------------------------

INSERT INTO permissions (code, scope, description, delegable) VALUES
    ('org.inbox.read',       'organization', 'Ver las bandejas, las conversaciones y los mensajes', true),
    ('org.inbox.reply',      'organization', 'Responder, asignar, cerrar y reabrir conversaciones', true),
    ('org.inbox.manage',     'organization', 'Conectar, editar y desconectar las bandejas de WhatsApp', false),
    ('org.contacts.read',    'organization', 'Ver los contactos', true),
    ('org.contacts.manage',  'organization', 'Crear y editar contactos', true);

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'admin' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code IN ('org.inbox.read', 'org.inbox.reply', 'org.inbox.manage', 'org.contacts.read', 'org.contacts.manage');

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'employee' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code IN ('org.inbox.read', 'org.inbox.reply', 'org.contacts.read', 'org.contacts.manage');

-- +goose Down
-- Borra la bandeja completa (conversaciones y mensajes incluidos): solo para desarrollo.

DELETE FROM role_permissions WHERE permission_code IN
    ('org.inbox.read', 'org.inbox.reply', 'org.inbox.manage', 'org.contacts.read', 'org.contacts.manage');
DELETE FROM permissions WHERE code IN
    ('org.inbox.read', 'org.inbox.reply', 'org.inbox.manage', 'org.contacts.read', 'org.contacts.manage');

DROP TABLE webhook_events;
DROP TABLE unmatched_statuses;
DROP TABLE message_attachments;
DROP TABLE messages;
DROP TABLE conversations;
DROP TABLE contacts;
DROP TABLE whatsapp_channels;
DROP TABLE whatsapp_accounts;
DROP TABLE inboxes;
DROP TABLE organization_counters;
