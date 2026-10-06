-- Promociones: cupones/descuentos aplicables al carrito. Diseño en docs/ecommerce.md §4. Cuarta y
-- última pieza planeada del módulo de e-commerce (catalog → orders → payments → promotions).
--
-- El código es único por organización entre las activas (mismo patrón que categories.name).
-- max_uses y per_customer_limit se comprueban contra CANJES REALES, no contra "se aplicó al
-- carrito alguna vez": una promoción solo se consume cuando el pedido deja de ser un borrador
-- (status <> 'draft') Y sigue siendo la promoción vigente en ese pedido (orders.promotion_id
-- coincide). Así, aplicar un código y luego seguir editando el carrito (lo que borra
-- orders.promotion_id, ver internal/orders) nunca gasta un cupo — evita que alguien agote un
-- max_uses limitado sin llegar a confirmar un solo pedido.

-- +goose Up

CREATE TABLE promotions (
    id                 uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id    uuid        NOT NULL,
    code               text        NOT NULL,
    discount_type      text        NOT NULL,
    -- percent: 1-100. fixed: centavos, > 0.
    value              integer     NOT NULL,
    min_order_cents    integer     NOT NULL DEFAULT 0,
    starts_at          timestamptz,
    ends_at            timestamptz,
    max_uses           integer,
    per_customer_limit integer,
    is_active          boolean     NOT NULL DEFAULT true,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT promotions_pkey PRIMARY KEY (id),
    CONSTRAINT promotions_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT promotions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT promotions_code_check CHECK (char_length(btrim(code)) BETWEEN 1 AND 40),
    CONSTRAINT promotions_discount_type_check CHECK (discount_type IN ('percent', 'fixed')),
    CONSTRAINT promotions_value_check CHECK (value > 0 AND (discount_type <> 'percent' OR value <= 100)),
    CONSTRAINT promotions_min_order_check CHECK (min_order_cents >= 0),
    CONSTRAINT promotions_dates_check CHECK (starts_at IS NULL OR ends_at IS NULL OR starts_at < ends_at),
    CONSTRAINT promotions_max_uses_check CHECK (max_uses IS NULL OR max_uses > 0),
    CONSTRAINT promotions_per_customer_limit_check CHECK (per_customer_limit IS NULL OR per_customer_limit > 0)
);
-- Sin distinguir mayúsculas: "BIENVENIDA10" y "bienvenida10" son el mismo código. Solo entre las
-- activas — mismo motivo que categories: se puede reciclar el código de una desactivada.
CREATE UNIQUE INDEX promotions_organization_code_key ON promotions (organization_id, upper(code)) WHERE is_active;

-- Un canje por pedido (UNIQUE en order_id): aplicar OTRO código reemplaza el canje, nunca lo
-- acumula. organization_id repetido a propósito (mismo patrón que el resto): aislamiento en la
-- base, no solo en el código.
CREATE TABLE promotion_redemptions (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    promotion_id    uuid        NOT NULL,
    organization_id uuid        NOT NULL,
    order_id        uuid        NOT NULL,
    customer_id     uuid        NOT NULL,
    -- Congelado en el momento de aplicar — igual que el precio de una línea: si la promoción
    -- cambia después, este canje ya hecho no se mueve.
    discount_cents  integer     NOT NULL,
    applied_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT promotion_redemptions_pkey PRIMARY KEY (id),
    CONSTRAINT promotion_redemptions_order_id_key UNIQUE (order_id),
    CONSTRAINT promotion_redemptions_promotion_fkey FOREIGN KEY (promotion_id, organization_id) REFERENCES promotions (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT promotion_redemptions_order_fkey FOREIGN KEY (order_id, organization_id) REFERENCES orders (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT promotion_redemptions_discount_check CHECK (discount_cents > 0)
);
CREATE INDEX ix_promotion_redemptions_promotion ON promotion_redemptions (promotion_id);
CREATE INDEX ix_promotion_redemptions_customer ON promotion_redemptions (promotion_id, customer_id);

-- El pedido conoce la promoción que tiene aplicada. SET NULL si la promoción se borrara de
-- verdad (no debería: aquí también se desactiva, nunca se borra) — un pedido no debe romperse por
-- eso.
ALTER TABLE orders ADD COLUMN promotion_id uuid;
ALTER TABLE orders ADD CONSTRAINT orders_promotion_fkey
    FOREIGN KEY (promotion_id, organization_id) REFERENCES promotions (id, organization_id) ON DELETE SET NULL;

-- ---------------------------------------------------------------------------
-- Permisos. Administrar promociones es una decisión de mercadeo, no rutina de atención — a
-- diferencia del catálogo o los pedidos, no se le da a employee.
-- ---------------------------------------------------------------------------

INSERT INTO permissions (code, scope, description, delegable) VALUES
    ('org.promotions.read',   'organization', 'Ver las promociones de la organización', true),
    ('org.promotions.manage', 'organization', 'Crear y editar promociones', true);

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'admin' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code IN ('org.promotions.read', 'org.promotions.manage');

-- +goose Down

DELETE FROM role_permissions WHERE permission_code IN ('org.promotions.read', 'org.promotions.manage');
DELETE FROM permissions WHERE code IN ('org.promotions.read', 'org.promotions.manage');

ALTER TABLE orders DROP CONSTRAINT orders_promotion_fkey;
ALTER TABLE orders DROP COLUMN promotion_id;

DROP TABLE promotion_redemptions;
DROP TABLE promotions;
