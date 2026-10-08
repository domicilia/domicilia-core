-- name: InsertPayment :one
INSERT INTO payments (order_id, organization_id, amount_cents, currency, gateway,
    method, base_cents, transaction_fee_cents, gateway_plan_code, breakdown)
VALUES (@order_id, @organization_id, @amount_cents, @currency, @gateway,
    sqlc.narg('method'), sqlc.narg('base_cents'), @transaction_fee_cents, sqlc.narg('gateway_plan_code'), @breakdown)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = @id AND organization_id = @organization_id;

-- Sin organization_id: la usa el webhook de la pasarela, que solo conoce el id que le mandamos
-- como referencia (p_id_invoice en ePayco) — igual motivo que orders.GetOrderByID.
-- name: GetPaymentByID :one
SELECT * FROM payments WHERE id = @id;

-- Aislamiento en la base, no solo en el código: aunque el servicio ya comprobó que el pedido es
-- de esta organización, la consulta lo vuelve a exigir.
-- name: ListPaymentsByOrder :many
SELECT * FROM payments WHERE order_id = @order_id AND organization_id = @organization_id ORDER BY created_at DESC, id DESC;

-- name: SetPaymentCheckoutURL :one
UPDATE payments SET checkout_url = @checkout_url, updated_at = now()
WHERE id = @id
RETURNING *;

-- Solo avanza desde 'pending': un evento repetido de la pasarela (reintento de webhook) no debe
-- poder pisar un estado final ya aplicado. Si no coincide ninguna fila, el servicio lo trata como
-- "ya se había procesado" — no como un error.
-- name: SettlePayment :one
UPDATE payments
SET status = @status, failure_reason = sqlc.narg('failure_reason'), updated_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- Idempotencia: 0 filas afectadas significa que este evento ya se había registrado antes (mismo
-- source + event_key) — el servicio lo trata como "ya se aplicó", no como un error.
-- name: InsertPaymentEvent :execrows
INSERT INTO payment_events (source, event_key, payload)
VALUES (@source, @event_key, @payload)
ON CONFLICT (source, event_key) DO NOTHING;

-- name: SetPaymentSession :one
UPDATE payments SET session_id = @session_id, updated_at = now()
WHERE id = @id
RETURNING *;

-- Lo que la pasarela dice que se usó para pagar (x_franchise en ePayco) y si coincide con lo que
-- el cliente declaró. Se registra aunque el pago ya estuviera resuelto: es información de
-- conciliación, no cambia el estado.
-- name: SetPaymentMethodUsed :one
UPDATE payments SET method_used = @method_used, method_mismatch = @method_mismatch, updated_at = now()
WHERE id = @id
RETURNING *;
