-- name: InsertOrganizationSettings :one
INSERT INTO organization_settings (organization_id) VALUES (@organization_id)
ON CONFLICT (organization_id) DO UPDATE SET organization_id = EXCLUDED.organization_id
RETURNING *;

-- name: GetOrganizationSettings :one
SELECT * FROM organization_settings WHERE organization_id = @organization_id;

-- name: GetOrganizationSettingsForUpdate :one
SELECT * FROM organization_settings WHERE organization_id = @organization_id FOR UPDATE;

-- El servicio fusiona el cambio parcial con la fila leída y escribe la fila completa.
-- name: UpdateOrganizationSettings :one
UPDATE organization_settings
SET legal_name = @legal_name, tax_id = @tax_id, contact_email = @contact_email,
    contact_phone = @contact_phone, address = @address, city = @city, logo_url = @logo_url,
    timezone = @timezone, locale = @locale, currency = @currency,
    business_hours = @business_hours, updated_at = now()
WHERE organization_id = @organization_id
RETURNING *;
