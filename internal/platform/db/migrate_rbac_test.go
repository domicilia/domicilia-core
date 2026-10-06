package db_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/domicilia/domicilia-core/internal/platform/db"
)

// 00002_rbac: el traspaso de los booleanos y el enum a roles. Es la migración más
// delicada: convierte datos reales, y un error aquí cambia quién puede qué.

func queryStrings(t *testing.T, conn *pgx.Conn, sql string, args ...any) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// legacyDB deja una base en la versión 1 (el esquema de Alembic) con datos como los
// de staging/producción: cada tipo de usuario que existía antes de los roles.
func legacyDB(t *testing.T) (string, *pgx.Conn) {
	t.Helper()
	url, conn := blank(t)
	if err := db.MigrateUpTo(context.Background(), url, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	const ts = `timezone('utc', now())`
	exec(t, conn, `INSERT INTO auth.users (id) VALUES
		('00000000-0000-4000-8000-00000000000a'), ('00000000-0000-4000-8000-00000000000b'),
		('00000000-0000-4000-8000-00000000000c'), ('00000000-0000-4000-8000-00000000000d'),
		('00000000-0000-4000-8000-00000000000e')`)
	// a: superadmin y admin de org · b: domiciliario · c: cliente · d: empleado · e: inactivo y sin nada
	exec(t, conn, `INSERT INTO users (id, email, is_active, is_general_admin, is_delivery, created_at, updated_at) VALUES
		('00000000-0000-4000-8000-00000000000a', 'a@ejemplo.com', true,  true,  false, `+ts+`, `+ts+`),
		('00000000-0000-4000-8000-00000000000b', 'b@ejemplo.com', true,  false, true,  `+ts+`, `+ts+`),
		('00000000-0000-4000-8000-00000000000c', 'c@ejemplo.com', true,  false, false, `+ts+`, `+ts+`),
		('00000000-0000-4000-8000-00000000000d', 'd@ejemplo.com', true,  false, false, `+ts+`, `+ts+`),
		('00000000-0000-4000-8000-00000000000e', 'e@ejemplo.com', false, false, false, `+ts+`, `+ts+`)`)
	exec(t, conn, `INSERT INTO customers (user_id, phone, created_at, updated_at) VALUES
		('00000000-0000-4000-8000-00000000000c', '300', `+ts+`, `+ts+`)`)
	exec(t, conn, `INSERT INTO organizations (id, name, slug, is_active, plan_tier, created_at, updated_at) VALUES
		('00000000-0000-4000-8000-0000000000f1', 'Org Uno', 'org-uno', true, 'basic', `+ts+`, `+ts+`)`)
	exec(t, conn, `INSERT INTO user_organizations (user_id, organization_id, role, created_at, updated_at) VALUES
		('00000000-0000-4000-8000-00000000000a', '00000000-0000-4000-8000-0000000000f1', 'admin', `+ts+`, `+ts+`),
		('00000000-0000-4000-8000-00000000000d', '00000000-0000-4000-8000-0000000000f1', 'employee', `+ts+`, `+ts+`)`)
	return url, conn
}

func TestRBACTraspasaLosDatosExistentes(t *testing.T) {
	url, conn := legacyDB(t)
	if err := db.MigrateUpTo(context.Background(), url, 2, io.Discard); err != nil {
		t.Fatalf("aplicar 00002_rbac sobre datos existentes: %v", err)
	}

	roles := func(email string) string {
		return strings.Join(queryStrings(t, conn, `SELECT r.code FROM user_platform_roles upr
			JOIN roles r ON r.id = upr.role_id JOIN users u ON u.id = upr.user_id WHERE u.email = $1 ORDER BY r.code`, email), ",")
	}
	for email, want := range map[string]string{
		"a@ejemplo.com": "superadmin", // era is_general_admin
		"b@ejemplo.com": "delivery",   // era is_delivery
		"c@ejemplo.com": "customer",   // tenía fila en customers
		"d@ejemplo.com": "",           // solo era empleado de una organización
		"e@ejemplo.com": "",
	} {
		if got := roles(email); got != want {
			t.Errorf("roles de plataforma de %s = %q, quería %q", email, got, want)
		}
	}

	memberRole := func(email string) string {
		return strings.Join(queryStrings(t, conn, `SELECT r.code FROM user_organizations uo JOIN roles r ON r.id = uo.role_id
			JOIN users u ON u.id = uo.user_id WHERE u.email = $1`, email), ",")
	}
	if got := memberRole("a@ejemplo.com"); got != "admin" {
		t.Errorf("rol de a en su organización = %q, quería admin (el valor del enum)", got)
	}
	if got := memberRole("d@ejemplo.com"); got != "employee" {
		t.Errorf("rol de d en su organización = %q, quería employee", got)
	}

	// Las columnas y el enum viejos ya no existen: no queda una segunda fuente de verdad.
	for _, col := range []struct{ table, column string }{
		{"users", "is_general_admin"}, {"users", "is_delivery"}, {"user_organizations", "role"},
	} {
		if n := queryStrings(t, conn, `SELECT column_name FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2`, col.table, col.column); len(n) != 0 {
			t.Errorf("la columna %s.%s debía haberse eliminado", col.table, col.column)
		}
	}
	if n := queryStrings(t, conn, `SELECT typname FROM pg_type WHERE typname = 'orgrole'`); len(n) != 0 {
		t.Error("el tipo orgrole debía haberse eliminado")
	}
	// Ningún dato se perdió.
	if got := queryStrings(t, conn, `SELECT count(*)::text FROM users`); got[0] != "5" {
		t.Errorf("usuarios = %s, quería 5", got[0])
	}
	if got := queryStrings(t, conn, `SELECT count(*)::text FROM user_organizations`); got[0] != "2" {
		t.Errorf("membresías = %s, quería 2", got[0])
	}
}

// La reversa devuelve la forma de Alembic (para desarrollo): los datos representables
// vuelven a sus columnas.
func TestRBACSeRevierteAlEsquemaDeAlembic(t *testing.T) {
	url, conn := legacyDB(t)
	if err := db.MigrateUpTo(context.Background(), url, 2, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00002_rbac: %v", err)
	}

	if got := queryStrings(t, conn, `SELECT email FROM users WHERE is_general_admin ORDER BY email`); strings.Join(got, ",") != "a@ejemplo.com" {
		t.Errorf("superadmins tras la reversa = %v, quería [a@ejemplo.com]", got)
	}
	if got := queryStrings(t, conn, `SELECT email FROM users WHERE is_delivery ORDER BY email`); strings.Join(got, ",") != "b@ejemplo.com" {
		t.Errorf("domiciliarios tras la reversa = %v, quería [b@ejemplo.com]", got)
	}
	if got := queryStrings(t, conn, `SELECT u.email || ':' || uo.role::text FROM user_organizations uo JOIN users u ON u.id = uo.user_id ORDER BY 1`); strings.Join(got, ",") != "a@ejemplo.com:admin,d@ejemplo.com:employee" {
		t.Errorf("roles tras la reversa = %v", got)
	}
	// Y quedó exactamente en la versión 1: un `up` la vuelve a llevar a la 2.
	if out, err := migrate(url, "up", false); err != nil || !strings.Contains(out, "00002_rbac.sql") {
		t.Fatalf("up tras la reversa = %q, %v", out, err)
	}
}
