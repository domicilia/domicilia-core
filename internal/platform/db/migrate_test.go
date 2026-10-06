package db_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/testutil/pgtest"
)

var (
	server   *pgtest.Server
	startErr error
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	srv, err := pgtest.Start(context.Background())
	if err != nil {
		startErr = err
		return m.Run()
	}
	defer srv.Close()
	server = srv
	return m.Run()
}

// blank devuelve una base vacía (solo con el doble de auth.users) y una conexión.
func blank(t *testing.T) (url string, conn *pgx.Conn) {
	t.Helper()
	pgtest.Skip(t, startErr)
	ctx := context.Background()
	url, err := server.NewDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return url, conn
}

func migrate(url, action string, production bool) (string, error) {
	var out bytes.Buffer
	err := db.Migrate(context.Background(), url, action, production, &out)
	return out.String(), err
}

func tableExists(t *testing.T, conn *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := conn.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, "public."+name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// columnExists es para migraciones que solo agregan/quitan columnas de una tabla que ya existe
// (00009: no crea tabla, tableExists no sirve para verificar su down).
func columnExists(t *testing.T, conn *pgx.Conn, table, column string) bool {
	t.Helper()
	var ok bool
	err := conn.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
		)`, table, column).Scan(&ok)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func exec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

var baseTables = []string{"organizations", "users", "user_organizations", "customers", "driver_applications"}

func TestUpCreaElEsquemaBase(t *testing.T) {
	url, conn := blank(t)

	out, err := migrate(url, "up", false)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "00001_baseline.sql") {
		t.Fatalf("la salida debía mencionar la migración aplicada: %q", out)
	}
	for _, name := range baseTables {
		if !tableExists(t, conn, name) {
			t.Errorf("falta la tabla %s", name)
		}
	}

	st, err := migrate(url, "status", false)
	if err != nil || !strings.Contains(st, "applied") {
		t.Fatalf("status = %q, %v", st, err)
	}
}

func TestUpEsIdempotente(t *testing.T) {
	url, _ := blank(t)
	if _, err := migrate(url, "up", false); err != nil {
		t.Fatal(err)
	}
	out, err := migrate(url, "up", false)
	if err != nil || !strings.Contains(out, "sin migraciones pendientes") {
		t.Fatalf("segundo up = %q, %v; quería \"sin migraciones pendientes\"", out, err)
	}
}

func TestUpSinAuthUsersDiceQueFalta(t *testing.T) {
	pgtest.Skip(t, startErr)
	// Una base sin el esquema auth: GoTrue todavía no arrancó.
	ctx := context.Background()
	url, err := server.NewDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	exec(t, conn, `DROP SCHEMA auth CASCADE`)

	_, err = migrate(url, "up", false)
	if err == nil || !strings.Contains(err.Error(), "auth.users no existe") {
		t.Fatalf("up sin auth.users debía explicar qué falta, err = %v", err)
	}
	if tableExists(t, conn, "users") {
		t.Fatal("la migración fallida dejó tablas a medias")
	}
}

func TestDownRevierteEnDesarrolloYSeNiegaEnProduccion(t *testing.T) {
	url, conn := blank(t)
	if _, err := migrate(url, "up", false); err != nil {
		t.Fatal(err)
	}

	// Revertir la base borra todas las tablas: en staging/producción no se permite.
	if _, err := migrate(url, "down", true); !errors.Is(err, db.ErrMigrationRefused) {
		t.Fatalf("down en producción debía rechazarse, err = %v", err)
	}
	if !tableExists(t, conn, "users") {
		t.Fatal("el down rechazado borró tablas de todos modos")
	}

	// down revierte UNA migración: la más reciente (00009). Las anteriores siguen.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down en desarrollo: %v", err)
	}
	if columnExists(t, conn, "products", "channels") || !tableExists(t, conn, "promotions") {
		t.Fatal("el primer down debía revertir solo 00009_product_channels")
	}
	// El segundo revierte 00008_promotions.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00008: %v", err)
	}
	if tableExists(t, conn, "promotions") || !tableExists(t, conn, "payments") {
		t.Fatal("el segundo down debía revertir solo 00008_promotions")
	}
	// El tercero revierte 00007_payments.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00007: %v", err)
	}
	if tableExists(t, conn, "payments") || !tableExists(t, conn, "orders") {
		t.Fatal("el tercero down debía revertir solo 00007_payments")
	}
	// El cuarto revierte 00006_orders.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00006: %v", err)
	}
	if tableExists(t, conn, "orders") || !tableExists(t, conn, "categories") {
		t.Fatal("el cuarto down debía revertir solo 00006_orders")
	}
	// El quinto revierte 00005_catalog.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00005: %v", err)
	}
	if tableExists(t, conn, "categories") || !tableExists(t, conn, "conversations") {
		t.Fatal("el quinto down debía revertir solo 00005_catalog")
	}
	// El sexto revierte 00004_whatsapp_inbox.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00004: %v", err)
	}
	if tableExists(t, conn, "conversations") || !tableExists(t, conn, "organization_invitations") {
		t.Fatal("el sexto down debía revertir solo 00004_whatsapp_inbox")
	}
	// El séptimo revierte 00003_organizations_lifecycle.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00003: %v", err)
	}
	if tableExists(t, conn, "organization_invitations") || !tableExists(t, conn, "roles") {
		t.Fatal("el séptimo down debía revertir solo 00003_organizations_lifecycle")
	}
	// El octavo revierte 00002_rbac; el esquema base sigue.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00002: %v", err)
	}
	if tableExists(t, conn, "roles") {
		t.Error("la tabla roles sigue existiendo tras revertir 00002_rbac")
	}
	for _, name := range baseTables {
		if !tableExists(t, conn, name) {
			t.Errorf("la tabla %s desapareció al revertir solo 00002_rbac", name)
		}
	}
	// El último down revierte la base y borra todo.
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("último down: %v", err)
	}
	for _, name := range baseTables {
		if tableExists(t, conn, name) {
			t.Errorf("la tabla %s sigue existiendo tras revertir la base", name)
		}
	}
}

func TestAccionDesconocida(t *testing.T) {
	url, _ := blank(t)
	if _, err := migrate(url, "borrar-todo", false); err == nil || !strings.Contains(err.Error(), "desconocida") {
		t.Fatalf("una acción desconocida debía rechazarse: %v", err)
	}
}

// alembicDB deja una base como la de staging/producción: el esquema ya creado
// (por Alembic) y sin tabla de control de goose.
func alembicDB(t *testing.T, revision string) (string, *pgx.Conn) {
	t.Helper()
	url, conn := blank(t)
	// Solo la base (versión 1): es el esquema que dejó Alembic, sin los roles.
	if err := db.MigrateUpTo(context.Background(), url, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	exec(t, conn, `DROP TABLE goose_db_version`)
	if revision != "" {
		exec(t, conn, `CREATE TABLE alembic_version (version_num varchar(32) NOT NULL)`)
		exec(t, conn, `INSERT INTO alembic_version VALUES ($1)`, revision)
	}
	return url, conn
}

const alembicFinal = "b9f4efb43ad7"

func TestAdoptaLaBaseDeAlembicSinEjecutarSuSQL(t *testing.T) {
	url, conn := alembicDB(t, alembicFinal)
	// Un dato de "producción": si adopt ejecutara el SQL de la migración (que
	// crea las tablas) fallaría, y si las recreara lo perdería.
	exec(t, conn, `INSERT INTO auth.users (id) VALUES ('00000000-0000-4000-8000-000000000001')`)
	exec(t, conn, `INSERT INTO organizations (id, name, slug, is_active, plan_tier, created_at, updated_at)
		VALUES ('00000000-0000-4000-8000-000000000002', 'Existente', 'existente', true, 'basic', now(), now())`)

	out, err := migrate(url, "adopt", true) // en producción, que es donde se usa
	if err != nil || !strings.Contains(out, "adoptada") {
		t.Fatalf("adopt = %q, %v", out, err)
	}

	var n int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM organizations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("organizaciones = %d, %v: adopt no debe tocar los datos", n, err)
	}
	// Tras adoptar la base queda pendiente lo que vino después (00002 a 00009), y NO la base.
	out, err = migrate(url, "up", true)
	if err != nil || !strings.Contains(out, "00002_rbac.sql") || !strings.Contains(out, "00003_organizations_lifecycle.sql") ||
		!strings.Contains(out, "00004_whatsapp_inbox.sql") || !strings.Contains(out, "00005_catalog.sql") ||
		!strings.Contains(out, "00006_orders.sql") || !strings.Contains(out, "00007_payments.sql") ||
		!strings.Contains(out, "00008_promotions.sql") || !strings.Contains(out, "00009_product_channels.sql") ||
		strings.Contains(out, "00001_baseline.sql") {
		t.Fatalf("tras adoptar debía aplicarse 00002 a 00009, no la base: %q, %v", out, err)
	}
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM organizations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("organizaciones = %d, %v: los datos deben sobrevivir a la migración", n, err)
	}
}

func TestAdoptSeNiegaSiLaBaseNoEsLaDeAlembic(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (string, *pgx.Conn)
		want  string
	}{
		{
			name:  "no existe alembic_version",
			setup: func(t *testing.T) (string, *pgx.Conn) { return alembicDB(t, "") },
			want:  "alembic_version",
		},
		{
			name:  "Alembic está en otra revisión",
			setup: func(t *testing.T) (string, *pgx.Conn) { return alembicDB(t, "a8f3c2d91e04") },
			want:  "debe estar en " + alembicFinal,
		},
		{
			name: "alembic_version tiene más de una fila",
			setup: func(t *testing.T) (string, *pgx.Conn) {
				url, conn := alembicDB(t, alembicFinal)
				exec(t, conn, `INSERT INTO alembic_version VALUES ('otra')`)
				return url, conn
			},
			want: "debe estar en " + alembicFinal,
		},
		{
			name: "falta una tabla del esquema",
			setup: func(t *testing.T) (string, *pgx.Conn) {
				url, conn := alembicDB(t, alembicFinal)
				exec(t, conn, `DROP TABLE customers`)
				return url, conn
			},
			want: "falta la tabla customers",
		},
		{
			name: "la base ya está adoptada o migrada",
			setup: func(t *testing.T) (string, *pgx.Conn) {
				url, conn := blank(t)
				if _, err := migrate(url, "up", false); err != nil {
					t.Fatal(err)
				}
				return url, conn
			},
			want: "ya está en la versión",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url, _ := tc.setup(t)
			_, err := migrate(url, "adopt", true)
			if !errors.Is(err, db.ErrMigrationRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("adopt debía rechazarse con %q, err = %v", tc.want, err)
			}
		})
	}
}
