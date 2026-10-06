-- name: InsertCustomer :one
INSERT INTO customers (user_id, phone, default_address, created_at, updated_at)
VALUES (@user_id, sqlc.narg('phone'), sqlc.narg('default_address'),
        timezone('utc', now()), timezone('utc', now()))
RETURNING *;

-- name: GetCustomer :one
SELECT * FROM customers WHERE user_id = @user_id;

-- name: UpdateCustomer :execrows
UPDATE customers
SET phone = COALESCE(sqlc.narg('phone'), phone),
    default_address = COALESCE(sqlc.narg('default_address'), default_address),
    updated_at = timezone('utc', now())
WHERE user_id = @user_id;
