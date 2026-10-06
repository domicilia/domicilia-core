-- Pagos: el cobro de un pedido y el registro de lo que dice la pasarela. Diseño en
-- docs/ecommerce.md §3. Tercera pieza del módulo de e-commerce (catalog → orders → payments →
-- promotions).
--
-- Modelo v2 (ver §0): el cliente paga en la pasarela; la pasarela liquida a Domicilia (una sola
-- cuenta comerciante). Un pedido puede tener MÁS de un intento de pago (el primero falla, el
-- cliente reintenta): por eso "payments" es su propia tabla, no columnas sueltas en "orders".

-- +goose Up

CREATE TABLE payments (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    order_id        uuid        NOT NULL,
    organization_id uuid        NOT NULL,
    status          text        NOT NULL DEFAULT 'pending',
    amount_cents    integer     NOT NULL,
    currency        char(3)     NOT NULL DEFAULT 'COP',
    gateway         text        NOT NULL DEFAULT 'epayco',
    -- checkout_url: adonde se mandó al cliente a pagar. failure_reason: solo si status = failed.
    checkout_url    text,
    failure_reason  text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payments_pkey PRIMARY KEY (id),
    CONSTRAINT payments_id_organization_key UNIQUE (id, organization_id),
    -- RESTRICT a propósito, igual que orders → organizations: un registro de pago es un
    -- documento contable, no algo que deba desaparecer en cascada.
    CONSTRAINT payments_order_fkey FOREIGN KEY (order_id, organization_id) REFERENCES orders (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT payments_status_check CHECK (status IN ('pending', 'succeeded', 'failed')),
    CONSTRAINT payments_amount_check CHECK (amount_cents > 0),
    CONSTRAINT payments_gateway_check CHECK (gateway IN ('epayco'))
);
CREATE INDEX ix_payments_order ON payments (order_id, created_at DESC);

-- Idempotencia del webhook — mismo patrón que webhook_events de WhatsApp (00004): un evento que
-- ya se aplicó no se vuelve a aplicar aunque la pasarela lo reintente (los webhooks de pago,
-- igual que los de Meta, prometen "al menos una vez", nunca "una sola vez").
CREATE TABLE payment_events (
    id          uuid        NOT NULL DEFAULT gen_random_uuid(),
    source      text        NOT NULL,
    event_key   text        NOT NULL,
    payload     jsonb       NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_events_pkey PRIMARY KEY (id),
    CONSTRAINT payment_events_source_key_key UNIQUE (source, event_key)
);

-- ---------------------------------------------------------------------------
-- Permisos. Solo lectura: nadie crea o edita un pago a mano, eso lo hace el webhook de la
-- pasarela. Es información contable — no se delega al empleado del día a día, a diferencia del
-- catálogo o los pedidos.
-- ---------------------------------------------------------------------------

INSERT INTO permissions (code, scope, description, delegable) VALUES
    ('org.payments.read', 'organization', 'Ver los pagos de los pedidos de la organización', false);

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'admin' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code = 'org.payments.read';

-- +goose Down

DELETE FROM role_permissions WHERE permission_code = 'org.payments.read';
DELETE FROM permissions WHERE code = 'org.payments.read';

DROP TABLE payment_events;
DROP TABLE payments;
