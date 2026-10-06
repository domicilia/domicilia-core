-- Solo para sqlc: la tabla auth.users es de auth-domicilia (GoTrue) y no está en
-- nuestras migraciones, pero public.users tiene una FK hacia ella. Este archivo
-- NO se ejecuta contra ninguna base.
CREATE SCHEMA auth;
CREATE TABLE auth.users (id uuid PRIMARY KEY);
