package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	_ "github.com/jackc/pgx/v5/stdlib" // registra el driver "pgx" para database/sql
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"

	"github.com/domicilia/domicilia-core/db/migrations"
)

// alembicFinalRevision es la última revisión de Alembic, que domicilia-api dejó
// congelada. La migración base de goose equivale exactamente a ese esquema.
const alembicFinalRevision = "b9f4efb43ad7"

// baselineVersion es la migración base (00001_baseline.sql).
const baselineVersion = 1

// tablasBase son las tablas que debe tener una base para poder adoptar la base.
var tablasBase = []string{
	"organizations", "users", "user_organizations", "customers", "driver_applications",
}

// ErrMigrationRefused se devuelve cuando la acción no se permite en este entorno.
var ErrMigrationRefused = errors.New("migración rechazada")

// Migrate ejecuta una acción de migración sobre la base indicada: up, down,
// status o adopt. Escribe un resumen legible en out.
//
// down solo se permite fuera de staging/producción: revertir la migración base
// borra todas las tablas. Dos procesos migrando a la vez se serializan con un
// lock de sesión de Postgres (p. ej. dos réplicas arrancando juntas).
func Migrate(ctx context.Context, url, action string, production bool, out io.Writer) error {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("migrar: abrir la base: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migrar: lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("migrar: proveedor: %w", err)
	}

	switch action {
	case "up":
		return up(ctx, p, out)
	case "down":
		if production {
			return fmt.Errorf("%w: down no se permite en staging/producción", ErrMigrationRefused)
		}
		return down(ctx, p, out)
	case "status":
		return status(ctx, p, out)
	case "adopt":
		return adopt(ctx, sqlDB, p, out)
	default:
		return fmt.Errorf("acción desconocida %q (usa: up, down, status, adopt)", action)
	}
}

// MigrateUpTo aplica las migraciones hasta la versión indicada (inclusive). Existe
// para probar los traspasos de datos: dejar la base en una versión intermedia,
// cargarle datos "como los de producción" y aplicar la siguiente migración.
func MigrateUpTo(ctx context.Context, url string, version int64, out io.Writer) error {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("migrar: abrir la base: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return fmt.Errorf("migrar: proveedor: %w", err)
	}
	results, err := p.UpTo(ctx, version)
	if err != nil {
		return fmt.Errorf("migrar: up-to %d: %w", version, err)
	}
	for _, r := range results {
		_, _ = fmt.Fprintf(out, "aplicada %s (%s)\n", r.Source.Path, r.Duration)
	}
	return nil
}

func up(ctx context.Context, p *goose.Provider, out io.Writer) error {
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrar: up: %w", err)
	}
	if len(results) == 0 {
		_, _ = fmt.Fprintln(out, "sin migraciones pendientes")
		return nil
	}
	for _, r := range results {
		_, _ = fmt.Fprintf(out, "aplicada %s (%s)\n", r.Source.Path, r.Duration)
	}
	return nil
}

func down(ctx context.Context, p *goose.Provider, out io.Writer) error {
	r, err := p.Down(ctx)
	if err != nil {
		return fmt.Errorf("migrar: down: %w", err)
	}
	_, _ = fmt.Fprintf(out, "revertida %s (%s)\n", r.Source.Path, r.Duration)
	return nil
}

func status(ctx context.Context, p *goose.Provider, out io.Writer) error {
	rows, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("migrar: status: %w", err)
	}
	for _, r := range rows {
		_, _ = fmt.Fprintf(out, "%-10s %s\n", r.State, r.Source.Path)
	}
	return nil
}

// adopt registra la migración base como aplicada SIN ejecutar su SQL. Es el paso
// de una sola vez para staging y producción, donde el esquema ya lo creó
// Alembic. Se niega si Alembic no está exactamente en su revisión final o si
// falta alguna tabla: en esos casos el esquema no coincide con la base y
// marcarla como aplicada escondería la diferencia.
func adopt(ctx context.Context, sqlDB *sql.DB, p *goose.Provider, out io.Writer) error {
	// Crea goose_db_version si no existe y devuelve la versión actual.
	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("migrar: adopt: leer versión: %w", err)
	}
	if current >= baselineVersion {
		return fmt.Errorf("%w: la base ya está en la versión %d", ErrMigrationRefused, current)
	}

	if err := requireAlembicAtFinal(ctx, sqlDB); err != nil {
		return err
	}
	for _, t := range tablasBase {
		var ok bool
		if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+t).Scan(&ok); err != nil {
			return fmt.Errorf("migrar: adopt: comprobar %s: %w", t, err)
		}
		if !ok {
			return fmt.Errorf("%w: falta la tabla %s; no es el esquema de Alembic", ErrMigrationRefused, t)
		}
	}

	store, err := database.NewStore(database.DialectPostgres, goose.DefaultTablename)
	if err != nil {
		return fmt.Errorf("migrar: adopt: %w", err)
	}
	if err := store.Insert(ctx, sqlDB, database.InsertRequest{Version: baselineVersion}); err != nil {
		return fmt.Errorf("migrar: adopt: registrar la base: %w", err)
	}
	_, _ = fmt.Fprintf(out, "adoptada la migración %d sin ejecutar su SQL\n", baselineVersion)
	return nil
}

func requireAlembicAtFinal(ctx context.Context, sqlDB *sql.DB) error {
	rows, err := sqlDB.QueryContext(ctx, `SELECT version_num FROM alembic_version`)
	if err != nil {
		return fmt.Errorf("%w: no se pudo leer alembic_version (¿es la base de Alembic?): %w", ErrMigrationRefused, err)
	}
	defer func() { _ = rows.Close() }()

	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("migrar: adopt: leer alembic_version: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrar: adopt: leer alembic_version: %w", err)
	}
	if len(versions) != 1 || versions[0] != alembicFinalRevision {
		return fmt.Errorf("%w: Alembic está en %v y debe estar en %s; aplica `alembic upgrade head` antes",
			ErrMigrationRefused, versions, alembicFinalRevision)
	}
	return nil
}
