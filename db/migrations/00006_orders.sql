-- Pedidos: el carrito y su máquina de estados. Diseño en docs/ecommerce.md (sección 2). Segunda
-- pieza del módulo de e-commerce (catalog → orders → payments → promotions).
--
-- El carrito ES un pedido en estado 'draft' — no hay una tabla de carritos aparte (mismo
-- principio que "las conversaciones no tienen una tabla de borradores"). El nombre y el precio de
-- cada línea se CONGELAN al confirmar (columnas *_snapshot en order_items): un pedido ya hecho no
-- cambia si el negocio edita su menú después.
--
-- Quien pide un pedido no es necesariamente miembro de la organización — un cliente comprando no
-- pertenece al negocio al que le compra. Por eso organization_id aquí NO implica membresía; el
-- aislamiento sigue siendo el mismo (FK compuesta hacia products/product_variants), pero el
-- permiso de negocio (org.orders.*) solo aplica al lado del NEGOCIO atendiendo el pedido, nunca
-- al cliente que lo hizo (ver tenant.Gate.OpenForCustomer).

-- +goose Up

CREATE TABLE orders (
    id                 uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id    uuid        NOT NULL,
    customer_id        uuid        NOT NULL,
    status             text        NOT NULL DEFAULT 'draft',
    subtotal_cents     integer     NOT NULL DEFAULT 0,
    discount_cents     integer     NOT NULL DEFAULT 0,
    delivery_fee_cents integer     NOT NULL DEFAULT 0,
    total_cents        integer     NOT NULL DEFAULT 0,
    placed_at          timestamptz,
    status_changed_at  timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT orders_pkey PRIMARY KEY (id),
    CONSTRAINT orders_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT orders_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
    CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT orders_status_check CHECK (status IN (
        'draft', 'placed', 'confirmed', 'payment_failed', 'accepted', 'rejected',
        'preparing', 'out_for_delivery', 'delivered', 'cancelled'
    )),
    CONSTRAINT orders_amounts_check CHECK (
        subtotal_cents >= 0 AND discount_cents >= 0 AND delivery_fee_cents >= 0 AND total_cents >= 0
    )
);
-- A lo sumo un carrito abierto por cliente por organización: evita carritos duplicados sueltos
-- que nadie va a confirmar nunca.
CREATE UNIQUE INDEX orders_one_draft_per_customer_key ON orders (organization_id, customer_id) WHERE status = 'draft';
CREATE INDEX ix_orders_customer ON orders (customer_id, created_at DESC, id);
CREATE INDEX ix_orders_organization_status ON orders (organization_id, status, created_at DESC, id);

-- Mientras el pedido está en 'draft' (el carrito) las líneas se agregan, cambian de cantidad y
-- se quitan libremente. En cuanto se confirma (place) quedan congeladas: ninguna operación de
-- negocio las edita ni las borra a partir de ahí — solo desaparecen en cascada si se borra el
-- pedido entero (limpieza de un draft abandonado, nunca de uno ya confirmado).
CREATE TABLE order_items (
    id                        uuid        NOT NULL DEFAULT gen_random_uuid(),
    order_id                  uuid        NOT NULL,
    organization_id           uuid        NOT NULL,
    -- Se conserva para reordenar fácil y para analítica; NUNCA se usa para mostrar precio o
    -- nombre una vez congelado (eso son las columnas *_snapshot). SET NULL si la variante
    -- desaparece de verdad (no debería, pero un pedido no debe romperse por eso).
    product_variant_id        uuid,
    name_snapshot             text        NOT NULL,
    unit_price_cents_snapshot integer     NOT NULL,
    -- [{modifier_group_id, modifier_group_name, option_id, option_name, price_delta_cents}, ...]
    modifiers_snapshot        jsonb       NOT NULL DEFAULT '[]'::jsonb,
    -- unit_price_cents_snapshot + la suma de los price_delta_cents de modifiers_snapshot.
    -- Guardado aparte para que cambiar la cantidad recalcule line_total_cents sin tener que
    -- releer ni sumar el jsonb — ese solo es para mostrar el detalle, no para el cálculo.
    unit_total_cents          integer     NOT NULL,
    quantity                  integer     NOT NULL DEFAULT 1,
    line_total_cents          integer     NOT NULL,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT order_items_pkey PRIMARY KEY (id),
    CONSTRAINT order_items_order_fkey FOREIGN KEY (order_id, organization_id) REFERENCES orders (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT order_items_variant_fkey FOREIGN KEY (product_variant_id, organization_id) REFERENCES product_variants (id, organization_id) ON DELETE SET NULL,
    CONSTRAINT order_items_name_check CHECK (char_length(btrim(name_snapshot)) BETWEEN 1 AND 150),
    CONSTRAINT order_items_quantity_check CHECK (quantity BETWEEN 1 AND 50),
    CONSTRAINT order_items_price_check CHECK (unit_price_cents_snapshot >= 0 AND unit_total_cents >= 0 AND line_total_cents >= 0),
    CONSTRAINT order_items_line_total_check CHECK (line_total_cents = unit_total_cents * quantity),
    CONSTRAINT order_items_modifiers_check CHECK (jsonb_typeof(modifiers_snapshot) = 'array')
);
CREATE INDEX ix_order_items_order ON order_items (order_id, created_at, id);

-- ---------------------------------------------------------------------------
-- Permisos. Solo el lado del NEGOCIO: gestionar sus pedidos (aceptar, rechazar, despachar...) es
-- administración rutinaria, igual que el catálogo — delegable. El cliente que hace el pedido no
-- pasa por aquí (ver el comentario de arriba de la migración).
-- ---------------------------------------------------------------------------

INSERT INTO permissions (code, scope, description, delegable) VALUES
    ('org.orders.read',   'organization', 'Ver los pedidos de la organización', true),
    ('org.orders.manage', 'organization', 'Aceptar, rechazar y cambiar el estado de los pedidos', true);

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'admin' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code IN ('org.orders.read', 'org.orders.manage');

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'employee' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code IN ('org.orders.read', 'org.orders.manage');

-- +goose Down

DELETE FROM role_permissions WHERE permission_code IN ('org.orders.read', 'org.orders.manage');
DELETE FROM permissions WHERE code IN ('org.orders.read', 'org.orders.manage');

DROP TABLE order_items;
DROP TABLE orders;
