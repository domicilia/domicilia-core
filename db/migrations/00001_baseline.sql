-- Migración base: el esquema `public` tal como lo dejó la última revisión de
-- Alembic (b9f4efb43ad7), que queda congelado en domicilia-api. A partir de aquí
-- el esquema lo gobierna solo goose.
--
-- En una base NUEVA se ejecuta normalmente (`api migrate up`). En staging y
-- producción el esquema ya existe: se adopta sin ejecutar este SQL
-- (`api migrate adopt`), que antes comprueba que Alembic esté en esa revisión.
--
-- Las columnas no llevan DEFAULT a propósito: es lo que Alembic creó, y las
-- fechas las pone la aplicación. Añadirlos aquí haría que el esquema de una base
-- nueva difiriera del de staging/producción.
--
-- El esquema `auth` no está aquí: es de auth-domicilia (GoTrue), que debe haber
-- arrancado antes para que exista `auth.users`.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('auth.users') IS NULL THEN
        RAISE EXCEPTION 'auth.users no existe: arranca auth-domicilia (GoTrue) antes de migrar';
    END IF;
END
$$;
-- +goose StatementEnd

CREATE TYPE orgrole AS ENUM ('admin', 'employee');
CREATE TYPE application_status AS ENUM ('pending', 'approved', 'rejected');

CREATE TABLE organizations (
    id          uuid                        NOT NULL,
    name        character varying(255)      NOT NULL,
    description character varying(500),
    created_at  timestamp without time zone NOT NULL,
    updated_at  timestamp without time zone NOT NULL,
    slug        character varying(100)      NOT NULL,
    is_active   boolean                     NOT NULL,
    plan_tier   character varying(50)       NOT NULL,
    CONSTRAINT organizations_pkey PRIMARY KEY (id),
    CONSTRAINT organizations_name_key UNIQUE (name),
    CONSTRAINT organizations_slug_key UNIQUE (slug)
);

CREATE TABLE users (
    id               uuid                        NOT NULL,
    email            character varying(255)      NOT NULL,
    full_name        character varying(255),
    is_active        boolean                     NOT NULL,
    is_general_admin boolean                     NOT NULL,
    created_at       timestamp without time zone NOT NULL,
    updated_at       timestamp without time zone NOT NULL,
    is_delivery      boolean                     NOT NULL,
    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT fk_users_auth_users FOREIGN KEY (id) REFERENCES auth.users (id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX ix_users_email ON users USING btree (email);

CREATE TABLE user_organizations (
    user_id         uuid                        NOT NULL,
    organization_id uuid                        NOT NULL,
    role            orgrole                     NOT NULL,
    created_at      timestamp without time zone NOT NULL,
    updated_at      timestamp without time zone NOT NULL,
    CONSTRAINT user_organizations_pkey PRIMARY KEY (user_id, organization_id),
    CONSTRAINT user_organizations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT user_organizations_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

CREATE TABLE customers (
    user_id         uuid                        NOT NULL,
    phone           character varying(50),
    default_address character varying(500),
    created_at      timestamp without time zone NOT NULL,
    updated_at      timestamp without time zone NOT NULL,
    CONSTRAINT customers_pkey PRIMARY KEY (user_id),
    CONSTRAINT customers_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

CREATE TABLE driver_applications (
    id           uuid                        NOT NULL,
    full_name    character varying(255)      NOT NULL,
    email        character varying(255)      NOT NULL,
    phone        character varying(50)       NOT NULL,
    vehicle_type character varying(50)       NOT NULL,
    status       application_status          NOT NULL,
    created_at   timestamp without time zone NOT NULL,
    updated_at   timestamp without time zone NOT NULL,
    CONSTRAINT driver_applications_pkey PRIMARY KEY (id)
);
CREATE INDEX ix_driver_applications_email ON driver_applications USING btree (email);

-- +goose Down
-- Destruye todos los datos del esquema. `api migrate down` se niega a correr en
-- staging/producción.
DROP TABLE driver_applications;
DROP TABLE customers;
DROP TABLE user_organizations;
DROP TABLE users;
DROP TABLE organizations;
DROP TYPE application_status;
DROP TYPE orgrole;
