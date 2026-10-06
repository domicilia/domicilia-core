-- Dónde se publica un producto, y sus ingredientes. Diseño en docs/ecommerce.md §7.
--
-- Hasta ahora is_active era el único filtro del feed público (GET /v1/public/products): un
-- producto activo aparecía ahí sin que la organización decidiera nada. channels hace esa
-- publicación explícita — 'ecommerce' habilita el feed del cliente; 'whatsapp' queda reservado
-- para cuando el agente de WhatsApp venda productos (hoy no existe ese lado, ver
-- docs/ecommerce.md §7 — el campo nace ya pensando en los dos canales para no repetir esta
-- migración). Vacío por omisión: un producto nuevo NO se publica solo, la organización tiene que
-- elegirlo a propósito.

-- +goose Up

ALTER TABLE products ADD COLUMN ingredients text[] NOT NULL DEFAULT '{}';
ALTER TABLE products ADD COLUMN channels text[] NOT NULL DEFAULT '{}';
ALTER TABLE products ADD CONSTRAINT products_channels_check
    CHECK (channels <@ ARRAY['ecommerce', 'whatsapp']::text[]);

-- +goose Down

ALTER TABLE products DROP CONSTRAINT products_channels_check;
ALTER TABLE products DROP COLUMN channels;
ALTER TABLE products DROP COLUMN ingredients;
