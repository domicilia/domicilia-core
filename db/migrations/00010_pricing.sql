-- Módulo de pagos v2.1 (docs/pagos.md): comisiones configurables, planes de tarifa de la pasarela,
-- precio local vs. publicado, promoción de producto y el desglose del costo de transacción según el
-- medio de pago que elige el cliente. Nada de esto queda "quemado" en el código: lo edita el
-- superadmin y cada cambio se audita.
--
-- Unidades: centavos de peso (COP × 100) y puntos básicos (10000 = 100 %), como el resto del core.

-- +goose Up

-- Planes de tarifa de la pasarela. Valores iniciales tomados de epayco.com/tarifas (consultado el
-- 2026-10-08): "Plan Davivienda" 2,64 % + $690 (promocional por tiempo limitado) y "Plan otros
-- bancos" 3,29 % + $700, más IVA; tarjetas internacionales +0,8 %; PSE por debajo de $60.000
-- cuesta $2.200. Nequi/Daviplata y PSE de $60.000 o más NO aparecen en esa página: se siembran con
-- la tarifa de tarjeta y quedan marcados como pendientes de confirmar con ePayco (docs/pagos.md).
CREATE TABLE gateway_fee_plans (
    code                      text        NOT NULL,
    gateway                   text        NOT NULL DEFAULT 'epayco',
    name                      text        NOT NULL,
    card_percent_bps          integer     NOT NULL,
    card_fixed_cents          integer     NOT NULL,
    international_extra_bps   integer     NOT NULL DEFAULT 0,
    wallet_percent_bps        integer     NOT NULL,
    wallet_fixed_cents        integer     NOT NULL,
    pse_percent_bps           integer     NOT NULL,
    pse_fixed_cents           integer     NOT NULL,
    pse_small_threshold_cents integer     NOT NULL DEFAULT 0,
    pse_small_fixed_cents     integer     NOT NULL DEFAULT 0,
    vat_bps                   integer     NOT NULL DEFAULT 1900,
    -- Texto libre para el superadmin: de dónde salió la tarifa, si es promocional, hasta cuándo.
    notes                     text,
    updated_by                uuid,
    updated_at                timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT gateway_fee_plans_pkey PRIMARY KEY (code),
    CONSTRAINT gateway_fee_plans_gateway_check CHECK (gateway IN ('epayco')),
    CONSTRAINT gateway_fee_plans_bps_check CHECK (
        card_percent_bps BETWEEN 0 AND 5000 AND international_extra_bps BETWEEN 0 AND 5000 AND
        wallet_percent_bps BETWEEN 0 AND 5000 AND pse_percent_bps BETWEEN 0 AND 5000 AND
        vat_bps BETWEEN 0 AND 5000
    ),
    CONSTRAINT gateway_fee_plans_cents_check CHECK (
        card_fixed_cents >= 0 AND wallet_fixed_cents >= 0 AND pse_fixed_cents >= 0 AND
        pse_small_threshold_cents >= 0 AND pse_small_fixed_cents >= 0
    )
);

INSERT INTO gateway_fee_plans (code, name, card_percent_bps, card_fixed_cents, international_extra_bps,
    wallet_percent_bps, wallet_fixed_cents, pse_percent_bps, pse_fixed_cents,
    pse_small_threshold_cents, pse_small_fixed_cents, vat_bps, notes) VALUES
    ('epayco_davivienda', 'ePayco · Plan Davivienda', 264, 69000, 80, 264, 69000, 264, 69000, 6000000, 220000, 1900,
     'epayco.com/tarifas (2026-10-08): 2,64 % + $690 + IVA, tarifa promocional por tiempo limitado. Nequi/Daviplata y PSE ≥ $60.000: POR CONFIRMAR con ePayco (se usa la de tarjeta).'),
    ('epayco_otros_bancos', 'ePayco · Plan otros bancos', 329, 70000, 80, 329, 70000, 329, 70000, 6000000, 220000, 1900,
     'epayco.com/tarifas (2026-10-08): 3,29 % + $700 + IVA. Nequi/Daviplata y PSE ≥ $60.000: POR CONFIRMAR con ePayco (se usa la de tarjeta).');

-- Configuración general (una sola fila). Cada organización la hereda salvo lo que el superadmin le
-- sobrescriba en organization_pricing.
CREATE TABLE pricing_settings (
    id                     boolean     NOT NULL DEFAULT true,
    platform_fee_bps       integer     NOT NULL DEFAULT 1000,  -- 10 % sobre el precio local
    promo_platform_fee_bps integer     NOT NULL DEFAULT 500,   -- 5 % si el producto está en promoción
    courier_fee_bps        integer     NOT NULL DEFAULT 0,     -- lo que Domicilia cobra al domiciliario (⏳ abogado)
    -- Tarifa plana de domicilio mientras no exista el algoritmo de ruta (docs/domicilios.md).
    delivery_fee_cents     integer     NOT NULL DEFAULT 0,
    gateway_plan_code      text        NOT NULL DEFAULT 'epayco_davivienda',
    -- Reparto automático en ePayco: apagado hasta que ePayco lo active en la cuenta (ticket de
    -- soporte) y se pruebe en sandbox. Apagado = todo el cobro llega a la cuenta de la plataforma.
    split_enabled          boolean     NOT NULL DEFAULT false,
    updated_by             uuid,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pricing_settings_pkey PRIMARY KEY (id),
    CONSTRAINT pricing_settings_singleton CHECK (id),
    CONSTRAINT pricing_settings_plan_fkey FOREIGN KEY (gateway_plan_code) REFERENCES gateway_fee_plans (code),
    CONSTRAINT pricing_settings_bps_check CHECK (
        platform_fee_bps BETWEEN 0 AND 10000 AND promo_platform_fee_bps BETWEEN 0 AND 10000 AND
        courier_fee_bps BETWEEN 0 AND 10000
    ),
    CONSTRAINT pricing_settings_delivery_check CHECK (delivery_fee_cents >= 0)
);
INSERT INTO pricing_settings (id) VALUES (true);

-- Lo que el superadmin le sobrescribe a una organización. NULL = hereda de pricing_settings.
CREATE TABLE organization_pricing (
    organization_id        uuid        NOT NULL,
    platform_fee_bps       integer,
    promo_platform_fee_bps integer,
    courier_fee_bps        integer,
    delivery_fee_cents     integer,
    -- El plan depende del banco de la cuenta que liquida (Davivienda u otro). ⏳ Confirmar con
    -- ePayco si en un split aplica el plan de la cuenta principal (Domicilia) o el del receptor.
    gateway_plan_code      text,
    -- Id del restaurante en ePayco como RECEPTOR del split (P_CUST_ID_CLIENTE). No es un secreto.
    epayco_merchant_id     text,
    updated_by             uuid        NOT NULL,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT organization_pricing_pkey PRIMARY KEY (organization_id),
    CONSTRAINT organization_pricing_org_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT organization_pricing_plan_fkey FOREIGN KEY (gateway_plan_code) REFERENCES gateway_fee_plans (code),
    CONSTRAINT organization_pricing_bps_check CHECK (
        (platform_fee_bps IS NULL OR platform_fee_bps BETWEEN 0 AND 10000) AND
        (promo_platform_fee_bps IS NULL OR promo_platform_fee_bps BETWEEN 0 AND 10000) AND
        (courier_fee_bps IS NULL OR courier_fee_bps BETWEEN 0 AND 10000)
    ),
    CONSTRAINT organization_pricing_delivery_check CHECK (delivery_fee_cents IS NULL OR delivery_fee_cents >= 0),
    CONSTRAINT organization_pricing_merchant_check CHECK (epayco_merchant_id IS NULL OR char_length(btrim(epayco_merchant_id)) BETWEEN 1 AND 40)
);

-- Promoción de producto: el restaurante da un descuento sobre SU precio local y la comisión de la
-- plataforma baja a promo_platform_fee_bps (decisión del usuario, 2026-10-08). 0 = sin promoción.
-- price_cents de las variantes (y price_delta_cents de los modificadores) siguen siendo el PRECIO
-- LOCAL: el publicado se calcula al leer (internal/pricing), nunca se guarda en el catálogo.
ALTER TABLE products ADD COLUMN promo_discount_bps integer NOT NULL DEFAULT 0;
ALTER TABLE products ADD CONSTRAINT products_promo_discount_check CHECK (promo_discount_bps BETWEEN 0 AND 9000);

-- Cada línea guarda, además de su precio publicado, cuánto de ella es del restaurante. Las líneas
-- que ya existían no tenían comisión: su parte local es su precio entero.
ALTER TABLE order_items ADD COLUMN unit_local_total_cents integer;
UPDATE order_items SET unit_local_total_cents = unit_total_cents;
ALTER TABLE order_items ALTER COLUMN unit_local_total_cents SET NOT NULL;
ALTER TABLE order_items ADD COLUMN platform_fee_bps integer NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD CONSTRAINT order_items_local_check CHECK (unit_local_total_cents >= 0 AND unit_local_total_cents <= unit_total_cents);

-- Lo que se congela al confirmar el pedido (place): subtotal local, comisión, reparto del
-- domicilio y las tarifas con las que se calculó (pricing_snapshot). Un cambio posterior de
-- tarifas nunca altera un pedido ya hecho.
ALTER TABLE orders ADD COLUMN subtotal_local_cents integer NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN platform_fee_cents integer NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN courier_fee_cents integer NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN pricing_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;
UPDATE orders SET subtotal_local_cents = subtotal_cents;
ALTER TABLE orders ADD CONSTRAINT orders_pricing_check CHECK (
    subtotal_local_cents >= 0 AND platform_fee_cents >= 0 AND courier_fee_cents >= 0
);

-- El pago guarda el medio que declaró el cliente, el costo de transacción que aceptó, el plan con
-- que se calculó y el desglose completo. method_used lo informa la pasarela en el webhook: si no
-- coincide con method, method_mismatch se marca para revisarlo (docs/pagos.md §7.6).
ALTER TABLE payments ADD COLUMN method text;
ALTER TABLE payments ADD COLUMN base_cents integer;
ALTER TABLE payments ADD COLUMN transaction_fee_cents integer NOT NULL DEFAULT 0;
ALTER TABLE payments ADD COLUMN gateway_plan_code text;
ALTER TABLE payments ADD COLUMN breakdown jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE payments ADD COLUMN session_id text;
ALTER TABLE payments ADD COLUMN method_used text;
ALTER TABLE payments ADD COLUMN method_mismatch boolean NOT NULL DEFAULT false;
ALTER TABLE payments ADD CONSTRAINT payments_method_check CHECK (
    method IS NULL OR method IN ('card', 'card_international', 'pse', 'wallet')
);
ALTER TABLE payments ADD CONSTRAINT payments_fee_check CHECK (transaction_fee_cents >= 0);

-- +goose Down

ALTER TABLE payments DROP CONSTRAINT payments_fee_check;
ALTER TABLE payments DROP CONSTRAINT payments_method_check;
ALTER TABLE payments DROP COLUMN method_mismatch;
ALTER TABLE payments DROP COLUMN method_used;
ALTER TABLE payments DROP COLUMN session_id;
ALTER TABLE payments DROP COLUMN breakdown;
ALTER TABLE payments DROP COLUMN gateway_plan_code;
ALTER TABLE payments DROP COLUMN transaction_fee_cents;
ALTER TABLE payments DROP COLUMN base_cents;
ALTER TABLE payments DROP COLUMN method;

ALTER TABLE orders DROP CONSTRAINT orders_pricing_check;
ALTER TABLE orders DROP COLUMN pricing_snapshot;
ALTER TABLE orders DROP COLUMN courier_fee_cents;
ALTER TABLE orders DROP COLUMN platform_fee_cents;
ALTER TABLE orders DROP COLUMN subtotal_local_cents;

ALTER TABLE order_items DROP CONSTRAINT order_items_local_check;
ALTER TABLE order_items DROP COLUMN platform_fee_bps;
ALTER TABLE order_items DROP COLUMN unit_local_total_cents;

ALTER TABLE products DROP CONSTRAINT products_promo_discount_check;
ALTER TABLE products DROP COLUMN promo_discount_bps;

DROP TABLE organization_pricing;
DROP TABLE pricing_settings;
DROP TABLE gateway_fee_plans;
