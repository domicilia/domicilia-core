-- ---------------------------------------------------------------------------
-- Pedidos
-- ---------------------------------------------------------------------------

-- Ver el carrito y crearlo si no existe son la misma operación: el índice único parcial
-- (organization_id, customer_id) WHERE status = 'draft' hace que un segundo intento devuelva el
-- MISMO carrito en vez de chocar. En cuanto el pedido deja 'draft', el índice deja de aplicarle:
-- la siguiente vez que este cliente compre en esta organización, se abre un carrito nuevo.
-- name: GetOrCreateDraftOrder :one
INSERT INTO orders (organization_id, customer_id)
VALUES (@organization_id, @customer_id)
ON CONFLICT (organization_id, customer_id) WHERE status = 'draft'
DO UPDATE SET updated_at = orders.updated_at
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = @id AND organization_id = @organization_id;

-- Sin organization_id: la usa el cliente para ver/cancelar UNO de sus propios pedidos por
-- /v1/orders/{id}, sin tener que conocer (ni declarar) de qué organización es. Segura porque el
-- id es un UUID no adivinable y el servicio comprueba dueño (customer_id) justo después.
-- name: GetOrderByID :one
SELECT * FROM orders WHERE id = @id;

-- name: ListOrdersByOrganization :many
SELECT * FROM orders
WHERE organization_id = @organization_id
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC, id DESC
LIMIT @page_size OFFSET @page_offset;

-- name: CountOrdersByOrganization :one
SELECT count(*) FROM orders
WHERE organization_id = @organization_id
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'));

-- Cruza organizaciones a propósito: es "mis pedidos" del cliente, no el de un negocio.
-- name: ListOrdersByCustomer :many
SELECT * FROM orders WHERE customer_id = @customer_id AND status <> 'draft'
ORDER BY created_at DESC, id DESC
LIMIT @page_size OFFSET @page_offset;

-- name: CountOrdersByCustomer :one
SELECT count(*) FROM orders WHERE customer_id = @customer_id AND status <> 'draft';

-- "Mis carritos": los borradores abiertos del cliente, cruzando organizaciones a propósito —
-- mismo motivo que ListOrdersByCustomer, pero al revés (status = 'draft'). Sin paginar: un
-- cliente tiene a lo sumo un puñado de carritos abiertos a la vez (uno por restaurante). Excluye
-- los vacíos (EXISTS): abrir el menú de un restaurante crea un borrador aunque todavía no se
-- agregue nada — eso no es un carrito que mostrar en "todas las rutas".
-- name: ListMyDraftOrders :many
SELECT o.id, o.organization_id, org.name AS organization_name, org.slug AS organization_slug,
       o.total_cents, o.updated_at,
       (SELECT count(*) FROM order_items oi WHERE oi.order_id = o.id)::int AS item_count
FROM orders o
JOIN organizations org ON org.id = o.organization_id
WHERE o.customer_id = @customer_id AND o.status = 'draft'
  AND EXISTS (SELECT 1 FROM order_items oi WHERE oi.order_id = o.id)
ORDER BY o.updated_at DESC, o.id;

-- Recalcula subtotal_cents desde las líneas reales — nunca se confía en un total que mandó el
-- cliente — y BORRA cualquier promoción aplicada: un cambio en el carrito invalida un descuento
-- calculado contra el subtotal anterior (ver internal/promotions). La llaman las mutaciones del
-- carrito (agregar/cambiar/quitar una línea), nunca place().
-- name: RecalcCartAmounts :one
UPDATE orders
SET subtotal_cents = @subtotal_cents, discount_cents = 0, promotion_id = NULL,
    total_cents = @total_cents, updated_at = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;

-- Recalcula subtotal_cents y total_cents SIN tocar discount_cents/promotion_id (a diferencia de
-- RecalcCartAmounts) — la llama place(): las líneas no debieron cambiar desde que se aplicó una
-- promoción (eso ya la habría borrado), así que esto es sobre todo una confirmación. total_cents
-- ya viene calculado desde Go (subtotal - descuento + envío, nunca negativo) — la aritmética de
-- negocio vive ahí, no en SQL.
-- name: SetOrderAmounts :one
UPDATE orders
SET subtotal_cents = @subtotal_cents, total_cents = @total_cents, updated_at = now()
WHERE id = @id AND organization_id = @organization_id
RETURNING *;

-- Solo mientras el pedido sigue en 'draft' (lo exige el servicio, no esta consulta). total_cents
-- ya viene calculado desde Go, mismo motivo que SetOrderAmounts.
-- name: ApplyPromotionToOrder :one
UPDATE orders
SET promotion_id = @promotion_id, discount_cents = @discount_cents, total_cents = @total_cents, updated_at = now()
WHERE id = @id AND organization_id = @organization_id AND status = 'draft'
RETURNING *;

-- Sin parámetro para total_cents: es pura aritmética de columnas (sin descuento, es
-- subtotal + envío), no hace falta calcularla en Go.
-- name: RemovePromotionFromOrder :one
UPDATE orders
SET promotion_id = NULL, discount_cents = 0, total_cents = subtotal_cents + delivery_fee_cents, updated_at = now()
WHERE id = @id AND organization_id = @organization_id AND status = 'draft'
RETURNING *;

-- Una transición de estado. placed_at solo se toca al confirmar el carrito (@set_placed_at); los
-- montos van aparte, por SetOrderAmounts — el resto de transiciones (aceptar, despachar...) no
-- cambian lo que el cliente va a pagar.
-- name: TransitionOrder :one
UPDATE orders
SET status = @status,
    status_changed_at = now(),
    placed_at = CASE WHEN @set_placed_at::boolean THEN now() ELSE placed_at END,
    updated_at = now()
WHERE id = @id AND organization_id = @organization_id AND status = ANY(@from_statuses::text[])
RETURNING *;

-- ---------------------------------------------------------------------------
-- Líneas del pedido (carrito)
-- ---------------------------------------------------------------------------

-- name: InsertOrderItem :one
INSERT INTO order_items (
    order_id, organization_id, product_variant_id, name_snapshot,
    unit_price_cents_snapshot, modifiers_snapshot, unit_total_cents, quantity, line_total_cents
) VALUES (
    @order_id, @organization_id, @product_variant_id, @name_snapshot,
    @unit_price_cents_snapshot, @modifiers_snapshot, @unit_total_cents, @quantity, @line_total_cents
)
RETURNING *;

-- name: GetOrderItem :one
SELECT * FROM order_items WHERE id = @id AND order_id = @order_id AND organization_id = @organization_id;

-- name: ListOrderItemsByOrder :many
SELECT * FROM order_items WHERE order_id = @order_id ORDER BY created_at, id;

-- name: UpdateOrderItemQuantity :one
UPDATE order_items
SET quantity = @quantity, line_total_cents = unit_total_cents * @quantity::int, updated_at = now()
WHERE id = @id AND order_id = @order_id AND organization_id = @organization_id
RETURNING *;

-- Solo se llama mientras el pedido sigue en 'draft' (lo exige el servicio, no esta consulta).
-- name: DeleteOrderItem :execrows
DELETE FROM order_items WHERE id = @id AND order_id = @order_id AND organization_id = @organization_id;

-- Recalcula subtotal_cents desde las líneas reales — nunca se confía en un total que el cliente
-- mandó por su cuenta.
-- name: SumOrderItems :one
SELECT COALESCE(sum(line_total_cents), 0)::bigint FROM order_items WHERE order_id = @order_id;
