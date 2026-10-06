-- Catálogo: categorías, productos, variantes y modificadores. Diseño en docs/ecommerce.md
-- (sección 1). Primera pieza del módulo de e-commerce (catalog → orders → payments →
-- promotions); sin esto no hay nada que poner en un carrito.
--
-- Mismo aislamiento entre inquilinos que el resto: cada tabla hija lleva organization_id y su
-- clave foránea hacia el padre es COMPUESTA (id, organization_id).
--
-- El precio SIEMPRE vive en la variante, nunca en el producto — un solo camino para calcularlo.
-- Un producto sin tamaños tiene una única variante ("Regular") marcada is_default.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Categorías
-- ---------------------------------------------------------------------------

CREATE TABLE categories (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL,
    name            text        NOT NULL,
    position        integer     NOT NULL DEFAULT 0,
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT categories_pkey PRIMARY KEY (id),
    CONSTRAINT categories_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT categories_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT categories_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 100)
);
-- Solo entre las activas: renombrar una y reactivar una vieja con el mismo nombre no debe chocar.
CREATE UNIQUE INDEX categories_organization_name_key ON categories (organization_id, lower(name)) WHERE is_active;
CREATE INDEX ix_categories_organization_position ON categories (organization_id, position, id);

-- ---------------------------------------------------------------------------
-- Productos y variantes
-- ---------------------------------------------------------------------------

CREATE TABLE products (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL,
    category_id     uuid,
    name            text        NOT NULL,
    description     text,
    image_url       text,
    position        integer     NOT NULL DEFAULT 0,
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT products_pkey PRIMARY KEY (id),
    CONSTRAINT products_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT products_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    -- category_id NULL no se valida contra categories (MATCH SIMPLE): un producto sin
    -- categoría es válido.
    CONSTRAINT products_category_fkey FOREIGN KEY (category_id, organization_id) REFERENCES categories (id, organization_id) ON DELETE SET NULL,
    CONSTRAINT products_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 150),
    CONSTRAINT products_description_check CHECK (description IS NULL OR char_length(description) <= 1000)
);
CREATE INDEX ix_products_organization_category ON products (organization_id, category_id, position, id);

-- Nunca se borra una variante de verdad una vez que existió (docs/ecommerce.md §1.1.6): un
-- pedido futuro la referenciará por id y necesita que exista, aunque esté is_active = false.
CREATE TABLE product_variants (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    product_id      uuid        NOT NULL,
    organization_id uuid        NOT NULL,
    name            text        NOT NULL,
    price_cents     integer     NOT NULL,
    is_default      boolean     NOT NULL DEFAULT false,
    is_active       boolean     NOT NULL DEFAULT true,
    position        integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT product_variants_pkey PRIMARY KEY (id),
    CONSTRAINT product_variants_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT product_variants_product_fkey FOREIGN KEY (product_id, organization_id) REFERENCES products (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT product_variants_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT product_variants_price_check CHECK (price_cents >= 0)
);
-- A lo sumo una variante activa por defecto por producto (la que se preselecciona al pedir).
CREATE UNIQUE INDEX product_variants_one_default_key ON product_variants (product_id) WHERE is_default AND is_active;
CREATE INDEX ix_product_variants_product ON product_variants (product_id, position, id);

-- ---------------------------------------------------------------------------
-- Modificadores (extras, tamaños de bebida, salsas...) — se reutilizan entre productos.
-- ---------------------------------------------------------------------------

CREATE TABLE modifier_groups (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL,
    name            text        NOT NULL,
    selection_type  text        NOT NULL DEFAULT 'single',
    -- "required" no es una columna aparte: min_select >= 1 YA significa obligatorio. Un solo
    -- lugar para esa regla, no dos campos que puedan contradecirse.
    min_select      integer     NOT NULL DEFAULT 0,
    max_select      integer     NOT NULL DEFAULT 1,
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT modifier_groups_pkey PRIMARY KEY (id),
    CONSTRAINT modifier_groups_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT modifier_groups_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT modifier_groups_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT modifier_groups_selection_type_check CHECK (selection_type IN ('single', 'multiple')),
    CONSTRAINT modifier_groups_select_check CHECK (min_select >= 0 AND max_select > 0 AND max_select >= min_select)
);

-- Mismo trato que las variantes: nunca se borra de verdad, se desactiva.
CREATE TABLE modifier_options (
    id                uuid        NOT NULL DEFAULT gen_random_uuid(),
    modifier_group_id uuid        NOT NULL,
    organization_id   uuid        NOT NULL,
    name              text        NOT NULL,
    price_delta_cents integer     NOT NULL DEFAULT 0,
    is_active         boolean     NOT NULL DEFAULT true,
    position          integer     NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT modifier_options_pkey PRIMARY KEY (id),
    CONSTRAINT modifier_options_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT modifier_options_group_fkey FOREIGN KEY (modifier_group_id, organization_id) REFERENCES modifier_groups (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT modifier_options_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT modifier_options_price_check CHECK (price_delta_cents >= 0)
);
CREATE INDEX ix_modifier_options_group ON modifier_options (modifier_group_id, position, id);

-- Un grupo se asocia a varios productos (N:M): "Extras de pizza" se define una vez.
CREATE TABLE product_modifier_groups (
    product_id        uuid    NOT NULL,
    organization_id   uuid    NOT NULL,
    modifier_group_id uuid    NOT NULL,
    position          integer NOT NULL DEFAULT 0,
    CONSTRAINT product_modifier_groups_pkey PRIMARY KEY (product_id, modifier_group_id),
    CONSTRAINT product_modifier_groups_product_fkey FOREIGN KEY (product_id, organization_id) REFERENCES products (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT product_modifier_groups_group_fkey FOREIGN KEY (modifier_group_id, organization_id) REFERENCES modifier_groups (id, organization_id) ON DELETE CASCADE
);

-- ---------------------------------------------------------------------------
-- Permisos. El admin administra el catálogo; el empleado (call center) solo lo consulta
-- (para ayudar a un cliente a pedir), igual que ya pasa con contactos/bandejas.
-- ---------------------------------------------------------------------------

INSERT INTO permissions (code, scope, description, delegable) VALUES
    ('org.catalog.read',   'organization', 'Ver categorías, productos, variantes y modificadores', true),
    ('org.catalog.manage', 'organization', 'Crear y editar categorías, productos, variantes y modificadores', true);

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'admin' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code IN ('org.catalog.read', 'org.catalog.manage');

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'employee' AND r.scope = 'organization' AND r.organization_id IS NULL
  AND p.code = 'org.catalog.read';

-- +goose Down

DELETE FROM role_permissions WHERE permission_code IN ('org.catalog.read', 'org.catalog.manage');
DELETE FROM permissions WHERE code IN ('org.catalog.read', 'org.catalog.manage');

DROP TABLE product_modifier_groups;
DROP TABLE modifier_options;
DROP TABLE modifier_groups;
DROP TABLE product_variants;
DROP TABLE products;
DROP TABLE categories;
