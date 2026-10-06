-- name: InsertDriverApplication :one
INSERT INTO driver_applications (id, full_name, email, phone, vehicle_type, status, created_at, updated_at)
VALUES (@id, @full_name, @email, @phone, @vehicle_type, 'pending',
        timezone('utc', now()), timezone('utc', now()))
RETURNING *;

-- name: GetDriverApplication :one
SELECT * FROM driver_applications WHERE id = @id;

-- name: ListDriverApplications :many
SELECT * FROM driver_applications
WHERE sqlc.narg('status')::application_status IS NULL OR status = sqlc.narg('status')::application_status
ORDER BY created_at, id;

-- Solo revisa una solicitud pendiente: si otra petición ya la revisó, no devuelve
-- filas. Es lo que evita aprobar dos veces en paralelo.
-- name: ReviewDriverApplication :one
UPDATE driver_applications
SET status = @status, updated_at = timezone('utc', now())
WHERE id = @id AND status = 'pending'
RETURNING *;
