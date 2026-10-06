-- RBAC: roles y permisos en tablas, en lugar de los booleanos y el enum que dejó
-- Alembic (users.is_general_admin, users.is_delivery, user_organizations.role).
--
-- Por qué: el negocio pide "poder crear otros roles" y permisos finos; con un enum
-- de Postgres cada rol nuevo es una migración, y con booleanos no hay permisos.
--
-- Modelo:
--   permissions       catálogo de permisos. Lo define el CÓDIGO (internal/access);
--                     una prueba comprueba que esta tabla y el código coinciden.
--   roles             de plataforma (superadmin, domiciliario, cliente...) o de
--                     organización (admin, empleado...). Los de sistema
--                     (organization_id NULL, is_system) no se editan; los
--                     personalizados pertenecen a una organización o a la plataforma.
--   role_permissions  qué permisos da cada rol.
--   user_platform_roles  roles de plataforma de un usuario (varios).
--   user_organizations.role_id  rol del usuario en cada organización (uno).
--   role_audit_log    quién cambió qué rol y cuándo.
--
-- ATENCIÓN — CONVIVENCIA CON domicilia-api (FastAPI):
--   Esta migración ELIMINA las columnas que domicilia-api todavía lee y escribe. Se
--   debe aplicar JUNTO con el desvío del tráfico al core, no antes: con Python
--   sirviendo las rutas de usuarios/organizaciones, dejaría de funcionar.
--
-- Las tablas nuevas usan timestamptz con DEFAULT now() (las de Alembic guardan
-- `timestamp` sin zona y sin DEFAULT; no se tocan).

-- +goose Up

CREATE TABLE permissions (
    code        text    NOT NULL,
    scope       text    NOT NULL,
    description text    NOT NULL,
    -- delegable: puede ir en un rol personalizado creado por un admin de
    -- organización. Los permisos de administración no lo son: si lo fueran, un
    -- admin podría fabricarse un rol equivalente a "admin" y saltarse la regla de
    -- que solo el superadmin asigna ese rol.
    delegable   boolean NOT NULL DEFAULT false,
    CONSTRAINT permissions_pkey PRIMARY KEY (code),
    CONSTRAINT permissions_scope_check CHECK (scope IN ('platform', 'organization'))
);

INSERT INTO permissions (code, scope, description, delegable) VALUES
    ('platform.overview.read',                'platform',     'Ver el panorama de la plataforma', false),
    ('platform.organizations.create',         'platform',     'Crear organizaciones', false),
    ('platform.organizations.manage',         'platform',     'Suspender/reactivar organizaciones y cambiar su plan', false),
    ('platform.organizations.access_all',     'platform',     'Actuar como miembro con todos los permisos en cualquier organización, incluso suspendida', false),
    ('platform.users.read',                   'platform',     'Listar y ver usuarios de la plataforma', false),
    ('platform.users.manage',                 'platform',     'Activar y desactivar cuentas', false),
    ('platform.roles.read',                   'platform',     'Ver roles y permisos', false),
    ('platform.roles.manage',                 'platform',     'Asignar roles de plataforma, crear roles y asignar cualquier rol de organización', false),
    ('platform.drivers.review',               'platform',     'Revisar las postulaciones de domiciliarios', false),
    ('platform.audit.read',                   'platform',     'Ver la auditoría de roles de toda la plataforma', false),
    ('org.members.read',                      'organization', 'Ver los miembros de la organización', true),
    ('org.members.manage',                    'organization', 'Sumar, invitar, cambiar de rol y sacar miembros', false),
    ('org.roles.read',                        'organization', 'Ver los roles de la organización', true),
    ('org.roles.manage',                      'organization', 'Crear, editar y borrar roles de la organización', false),
    ('org.audit.read',                        'organization', 'Ver la auditoría de roles de la organización', false);

CREATE TABLE roles (
    id             uuid        NOT NULL DEFAULT gen_random_uuid(),
    code           text        NOT NULL,
    name           text        NOT NULL,
    description    text,
    scope          text        NOT NULL,
    -- NULL: rol de sistema (o de plataforma). No NULL: rol personalizado de esa organización.
    organization_id uuid,
    is_system      boolean     NOT NULL DEFAULT false,
    -- org_assignable: un admin de organización puede asignarlo. Falso para "admin":
    -- solo quien tiene platform.roles.manage nombra administradores.
    org_assignable boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT roles_pkey PRIMARY KEY (id),
    CONSTRAINT roles_scope_check CHECK (scope IN ('platform', 'organization')),
    CONSTRAINT roles_org_only_in_org_scope CHECK (organization_id IS NULL OR scope = 'organization'),
    CONSTRAINT roles_system_has_no_org CHECK (NOT is_system OR organization_id IS NULL),
    CONSTRAINT roles_code_format CHECK (code ~ '^[a-z][a-z0-9_-]{1,49}$'),
    CONSTRAINT roles_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE
);
-- El código es único entre los roles sin organización (por alcance) y dentro de cada organización.
CREATE UNIQUE INDEX roles_global_code_key ON roles (scope, code) WHERE organization_id IS NULL;
CREATE UNIQUE INDEX roles_org_code_key ON roles (organization_id, code) WHERE organization_id IS NOT NULL;

CREATE TABLE role_permissions (
    role_id         uuid NOT NULL,
    permission_code text NOT NULL,
    CONSTRAINT role_permissions_pkey PRIMARY KEY (role_id, permission_code),
    CONSTRAINT role_permissions_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE CASCADE,
    CONSTRAINT role_permissions_permission_code_fkey FOREIGN KEY (permission_code) REFERENCES permissions (code) ON DELETE RESTRICT
);

-- Roles de sistema. Los códigos admin/employee coinciden con los valores del enum
-- que reemplazan: así el contrato con el frontend (`role: "admin"`) no cambia.
INSERT INTO roles (code, name, description, scope, is_system, org_assignable) VALUES
    ('superadmin', 'Superadmin',   'Operador de la plataforma: todos los permisos', 'platform', true, false),
    ('delivery',   'Domiciliario', 'Reparte para cualquier organización; no pertenece a una', 'platform', true, false),
    ('customer',   'Cliente',      'Quien pide domicilios', 'platform', true, false),
    ('admin',      'Administrador','Administra su organización', 'organization', true, false),
    ('employee',   'Empleado',     'Atiende (call center); no administra', 'organization', true, true);

-- El superadmin tiene todos los permisos de PLATAFORMA. Los de organización no se
-- le listan: platform.organizations.access_all le da todos en cualquier
-- organización, y así un rol de plataforma nunca mezcla alcances. Toda migración
-- que agregue un permiso de plataforma debe otorgárselo (una prueba lo verifica).
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'superadmin' AND r.scope = 'platform' AND p.scope = 'platform';

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code FROM roles r CROSS JOIN permissions p
WHERE r.code = 'admin' AND r.scope = 'organization' AND p.scope = 'organization';

CREATE TABLE user_platform_roles (
    user_id    uuid        NOT NULL,
    role_id    uuid        NOT NULL,
    granted_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_platform_roles_pkey PRIMARY KEY (user_id, role_id),
    CONSTRAINT user_platform_roles_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT user_platform_roles_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE RESTRICT,
    CONSTRAINT user_platform_roles_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX ix_user_platform_roles_role_id ON user_platform_roles (role_id);

-- Traspaso de los booleanos al modelo nuevo.
INSERT INTO user_platform_roles (user_id, role_id)
SELECT u.id, r.id FROM users u JOIN roles r ON r.code = 'superadmin' AND r.scope = 'platform'
WHERE u.is_general_admin;

INSERT INTO user_platform_roles (user_id, role_id)
SELECT u.id, r.id FROM users u JOIN roles r ON r.code = 'delivery' AND r.scope = 'platform'
WHERE u.is_delivery;

INSERT INTO user_platform_roles (user_id, role_id)
SELECT c.user_id, r.id FROM customers c JOIN roles r ON r.code = 'customer' AND r.scope = 'platform';

-- Traspaso del enum de rol de organización a role_id.
ALTER TABLE user_organizations ADD COLUMN role_id uuid;
UPDATE user_organizations uo SET role_id = r.id
FROM roles r
WHERE r.organization_id IS NULL AND r.scope = 'organization' AND r.code = uo.role::text;
ALTER TABLE user_organizations
    ALTER COLUMN role_id SET NOT NULL,
    ADD CONSTRAINT user_organizations_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE RESTRICT;
CREATE INDEX ix_user_organizations_role_id ON user_organizations (role_id);
ALTER TABLE user_organizations DROP COLUMN role;
DROP TYPE orgrole;

ALTER TABLE users DROP COLUMN is_general_admin, DROP COLUMN is_delivery;

CREATE TABLE role_audit_log (
    id             uuid        NOT NULL DEFAULT gen_random_uuid(),
    actor_id       uuid,
    action         text        NOT NULL,
    target_user_id uuid,
    organization_id uuid,
    role_id        uuid,
    -- El código del rol al momento del cambio: el rol puede borrarse después.
    role_code      text,
    detail         jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT role_audit_log_pkey PRIMARY KEY (id),
    CONSTRAINT role_audit_log_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT role_audit_log_target_user_id_fkey FOREIGN KEY (target_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT role_audit_log_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE SET NULL,
    CONSTRAINT role_audit_log_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE SET NULL
);
CREATE INDEX ix_role_audit_log_created ON role_audit_log (created_at DESC, id);
CREATE INDEX ix_role_audit_log_org ON role_audit_log (organization_id, created_at DESC);
CREATE INDEX ix_role_audit_log_target ON role_audit_log (target_user_id, created_at DESC);

-- +goose Down
-- Devuelve la forma de Alembic. No es exacta: un rol personalizado de organización
-- no cabe en el enum y vuelve como `employee`, y se pierden los roles de plataforma
-- distintos de superadmin/domiciliario y la auditoría. Sirve para desarrollo, no
-- como plan de reversa de producción.

CREATE TYPE orgrole AS ENUM ('admin', 'employee');

ALTER TABLE users
    ADD COLUMN is_general_admin boolean NOT NULL DEFAULT false,
    ADD COLUMN is_delivery boolean NOT NULL DEFAULT false;
UPDATE users u SET is_general_admin = true
WHERE EXISTS (SELECT 1 FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
              WHERE upr.user_id = u.id AND r.code = 'superadmin' AND r.scope = 'platform');
UPDATE users u SET is_delivery = true
WHERE EXISTS (SELECT 1 FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
              WHERE upr.user_id = u.id AND r.code = 'delivery' AND r.scope = 'platform');
ALTER TABLE users ALTER COLUMN is_general_admin DROP DEFAULT, ALTER COLUMN is_delivery DROP DEFAULT;

ALTER TABLE user_organizations ADD COLUMN role orgrole;
UPDATE user_organizations uo SET role = CASE WHEN r.code = 'admin' AND r.organization_id IS NULL THEN 'admin' ELSE 'employee' END::orgrole
FROM roles r WHERE r.id = uo.role_id;
ALTER TABLE user_organizations ALTER COLUMN role SET NOT NULL;
ALTER TABLE user_organizations DROP COLUMN role_id;

DROP TABLE role_audit_log;
DROP TABLE user_platform_roles;
DROP TABLE role_permissions;
DROP TABLE roles;
DROP TABLE permissions;
