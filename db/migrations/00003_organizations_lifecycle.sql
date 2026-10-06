-- Ciclo de vida de la organización: estado, ajustes, planes e invitaciones.
-- Diseño en docs/organizaciones.md.
--
-- Cambios sobre tablas existentes:
--   organizations.is_active  →  status ('active' | 'suspended' | 'archived') con motivo.
--                               El JSON de la API sigue diciendo is_active = (status = 'active').
--   organizations.plan_tier  →  'basic' pasa a 'starter' y solo admite los cuatro planes.
--                               Un valor desconocido pasa a 'starter' (el más restringido).
--   role_audit_log           →  audit_log: ya no registra solo roles.
--
-- Tablas nuevas: organization_settings, organization_subscriptions,
-- organization_feature_overrides, organization_invitations.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Estado y plan
-- ---------------------------------------------------------------------------

ALTER TABLE organizations
    ADD COLUMN status            text        NOT NULL DEFAULT 'active',
    ADD COLUMN status_reason     text,
    ADD COLUMN status_changed_at timestamptz,
    ADD CONSTRAINT organizations_status_check CHECK (status IN ('active', 'suspended', 'archived'));
UPDATE organizations SET status = 'suspended' WHERE NOT is_active;
ALTER TABLE organizations DROP COLUMN is_active;

UPDATE organizations SET plan_tier = 'starter'
WHERE plan_tier NOT IN ('starter', 'pro', 'outreach', 'enterprise');
ALTER TABLE organizations
    ALTER COLUMN plan_tier SET DEFAULT 'starter',
    ADD CONSTRAINT organizations_plan_tier_check CHECK (plan_tier IN ('starter', 'pro', 'outreach', 'enterprise'));
CREATE INDEX ix_organizations_status ON organizations (status);

-- ---------------------------------------------------------------------------
-- Ajustes del negocio (1:1)
-- ---------------------------------------------------------------------------

CREATE TABLE organization_settings (
    organization_id uuid        NOT NULL,
    legal_name      text,
    tax_id          text,
    contact_email   text,
    contact_phone   text,
    address         text,
    city            text,
    logo_url        text,
    timezone        text        NOT NULL DEFAULT 'America/Bogota',
    locale          text        NOT NULL DEFAULT 'es-CO',
    currency        char(3)     NOT NULL DEFAULT 'COP',
    -- {"mon": [{"open": "08:00", "close": "20:00"}], ...}; el código lo valida.
    business_hours  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT organization_settings_pkey PRIMARY KEY (organization_id),
    CONSTRAINT organization_settings_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT organization_settings_currency_check CHECK (currency ~ '^[A-Z]{3}$')
);
INSERT INTO organization_settings (organization_id) SELECT id FROM organizations;

-- ---------------------------------------------------------------------------
-- Suscripciones: historial de planes. Una sola vigente (ended_at NULL) por organización.
-- organizations.plan_tier es la copia del plan vigente; ambas cambian en la misma
-- transacción.
-- ---------------------------------------------------------------------------

CREATE TABLE organization_subscriptions (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL,
    plan_tier       text        NOT NULL,
    started_at      timestamptz NOT NULL DEFAULT now(),
    ended_at        timestamptz,
    changed_by      uuid,
    reason          text,
    CONSTRAINT organization_subscriptions_pkey PRIMARY KEY (id),
    CONSTRAINT organization_subscriptions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT organization_subscriptions_changed_by_fkey FOREIGN KEY (changed_by) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT organization_subscriptions_plan_tier_check CHECK (plan_tier IN ('starter', 'pro', 'outreach', 'enterprise')),
    CONSTRAINT organization_subscriptions_period_check CHECK (ended_at IS NULL OR ended_at >= started_at)
);
CREATE UNIQUE INDEX organization_subscriptions_current_key ON organization_subscriptions (organization_id) WHERE ended_at IS NULL;
CREATE INDEX ix_organization_subscriptions_history ON organization_subscriptions (organization_id, started_at DESC);
INSERT INTO organization_subscriptions (organization_id, plan_tier, started_at, reason)
SELECT id, plan_tier, created_at AT TIME ZONE 'utc', 'migración inicial' FROM organizations;

-- ---------------------------------------------------------------------------
-- Excepciones por función: enciende o apaga una función para una organización,
-- por encima de lo que diga su plan. La clave la valida el código (internal/plans).
-- ---------------------------------------------------------------------------

CREATE TABLE organization_feature_overrides (
    organization_id uuid        NOT NULL,
    feature         text        NOT NULL,
    enabled         boolean     NOT NULL,
    reason          text,
    created_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT organization_feature_overrides_pkey PRIMARY KEY (organization_id, feature),
    CONSTRAINT organization_feature_overrides_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT organization_feature_overrides_created_by_fkey FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL
);

-- ---------------------------------------------------------------------------
-- Invitaciones con token. Solo se guarda el hash del token.
-- ---------------------------------------------------------------------------

CREATE TABLE organization_invitations (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL,
    email           text        NOT NULL,
    role_id         uuid        NOT NULL,
    token_hash      bytea       NOT NULL,
    invited_by      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    accepted_at     timestamptz,
    accepted_by     uuid,
    revoked_at      timestamptz,
    CONSTRAINT organization_invitations_pkey PRIMARY KEY (id),
    CONSTRAINT organization_invitations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    -- Si se borra el rol personalizado, sus invitaciones pendientes dejan de tener sentido.
    CONSTRAINT organization_invitations_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE CASCADE,
    CONSTRAINT organization_invitations_invited_by_fkey FOREIGN KEY (invited_by) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT organization_invitations_accepted_by_fkey FOREIGN KEY (accepted_by) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT organization_invitations_email_lower_check CHECK (email = lower(email)),
    CONSTRAINT organization_invitations_final_state_check CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);
CREATE UNIQUE INDEX organization_invitations_token_key ON organization_invitations (token_hash);
-- Una invitación pendiente por correo y organización; reinvitar la reemplaza.
CREATE UNIQUE INDEX organization_invitations_pending_key ON organization_invitations (organization_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX ix_organization_invitations_email ON organization_invitations (email);

-- ---------------------------------------------------------------------------
-- Auditoría general
-- ---------------------------------------------------------------------------

ALTER TABLE role_audit_log RENAME TO audit_log;
ALTER TABLE audit_log RENAME CONSTRAINT role_audit_log_pkey TO audit_log_pkey;
ALTER TABLE audit_log RENAME CONSTRAINT role_audit_log_actor_id_fkey TO audit_log_actor_id_fkey;
ALTER TABLE audit_log RENAME CONSTRAINT role_audit_log_target_user_id_fkey TO audit_log_target_user_id_fkey;
ALTER TABLE audit_log RENAME CONSTRAINT role_audit_log_organization_id_fkey TO audit_log_organization_id_fkey;
ALTER TABLE audit_log RENAME CONSTRAINT role_audit_log_role_id_fkey TO audit_log_role_id_fkey;
ALTER INDEX ix_role_audit_log_created RENAME TO ix_audit_log_created;
ALTER INDEX ix_role_audit_log_org RENAME TO ix_audit_log_org;
ALTER INDEX ix_role_audit_log_target RENAME TO ix_audit_log_target;

UPDATE permissions SET description = 'Ver la auditoría de toda la plataforma' WHERE code = 'platform.audit.read';
UPDATE permissions SET description = 'Ver la auditoría de la organización' WHERE code = 'org.audit.read';

-- ---------------------------------------------------------------------------
-- Permisos nuevos. Los de organización se le dan al rol de sistema "admin" (y solo
-- a él: los personalizados los arma cada organización con los delegables). El
-- superadmin los tiene todos por platform.organizations.access_all.
-- ---------------------------------------------------------------------------

INSERT INTO permissions (code, scope, description, delegable) VALUES
    ('org.settings.read',   'organization', 'Ver los ajustes del negocio (datos de contacto, horarios, zona horaria)', true),
    ('org.settings.manage', 'organization', 'Editar el perfil y los ajustes del negocio', false),
    ('org.billing.read',    'organization', 'Ver el plan y el historial de suscripción', false);

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'admin' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code IN ('org.settings.read', 'org.settings.manage', 'org.billing.read');

-- +goose Down
-- Vuelve a la forma de 00002. Se pierde el historial de planes, las invitaciones, las
-- excepciones y los ajustes; un estado "archived" vuelve como no activa.

DELETE FROM role_permissions WHERE permission_code IN ('org.settings.read', 'org.settings.manage', 'org.billing.read');
DELETE FROM permissions WHERE code IN ('org.settings.read', 'org.settings.manage', 'org.billing.read');
UPDATE permissions SET description = 'Ver la auditoría de roles de toda la plataforma' WHERE code = 'platform.audit.read';
UPDATE permissions SET description = 'Ver la auditoría de roles de la organización' WHERE code = 'org.audit.read';

ALTER INDEX ix_audit_log_target RENAME TO ix_role_audit_log_target;
ALTER INDEX ix_audit_log_org RENAME TO ix_role_audit_log_org;
ALTER INDEX ix_audit_log_created RENAME TO ix_role_audit_log_created;
ALTER TABLE audit_log RENAME CONSTRAINT audit_log_role_id_fkey TO role_audit_log_role_id_fkey;
ALTER TABLE audit_log RENAME CONSTRAINT audit_log_organization_id_fkey TO role_audit_log_organization_id_fkey;
ALTER TABLE audit_log RENAME CONSTRAINT audit_log_target_user_id_fkey TO role_audit_log_target_user_id_fkey;
ALTER TABLE audit_log RENAME CONSTRAINT audit_log_actor_id_fkey TO role_audit_log_actor_id_fkey;
ALTER TABLE audit_log RENAME CONSTRAINT audit_log_pkey TO role_audit_log_pkey;
ALTER TABLE audit_log RENAME TO role_audit_log;

DROP TABLE organization_invitations;
DROP TABLE organization_feature_overrides;
DROP TABLE organization_subscriptions;
DROP TABLE organization_settings;

ALTER TABLE organizations
    DROP CONSTRAINT organizations_plan_tier_check,
    ALTER COLUMN plan_tier DROP DEFAULT;
DROP INDEX ix_organizations_status;
ALTER TABLE organizations ADD COLUMN is_active boolean;
UPDATE organizations SET is_active = (status = 'active');
ALTER TABLE organizations
    ALTER COLUMN is_active SET NOT NULL,
    DROP CONSTRAINT organizations_status_check,
    DROP COLUMN status,
    DROP COLUMN status_reason,
    DROP COLUMN status_changed_at;
