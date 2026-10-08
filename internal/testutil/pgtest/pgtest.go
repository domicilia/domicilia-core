// Package pgtest levanta un Postgres real y desechable para las pruebas, con el
// esquema aplicado por las migraciones REALES de goose. Nada de SQLite ni de
// crear las tablas desde los modelos: SQLite no tiene UUID ni ENUM nativos y
// pasaría pruebas que fallarían en producción, y crear el esquema aparte no
// detecta una migración mal escrita.
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/domicilia/domicilia-core/internal/platform/db"
)

const (
	image = "postgres:16-alpine"

	// RequiredEnv convierte el "no hay Docker" de un skip silencioso en un fallo
	// ruidoso. En el CI vale "1": una suite que se salta sus pruebas de base de
	// datos sin decirlo es peor que no tenerlas.
	RequiredEnv = "TESTS_DB_REQUIRED"

	startTimeout = 3 * time.Minute
)

// Server es un Postgres desechable. Sobre él se crean bases independientes.
type Server struct {
	ctr     *postgres.PostgresContainer
	baseURL string
	seq     atomic.Int64
}

// Start levanta el contenedor. Si Docker no está disponible devuelve
// ErrUnavailable: quien llama decide entre omitir y fallar (ver Required).
func Start(ctx context.Context) (*Server, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()

	ctr, err := postgres.Run(ctx, image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		if ctr != nil {
			_ = testcontainers.TerminateContainer(ctr)
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return nil, fmt.Errorf("pgtest: url: %w", err)
	}
	return &Server{ctr: ctr, baseURL: strings.TrimSuffix(strings.SplitN(url, "?", 2)[0], "/postgres")}, nil
}

// ErrUnavailable indica que no se pudo levantar Postgres (típicamente, sin Docker).
var ErrUnavailable = errors.New("pgtest: Postgres de pruebas no disponible")

// Required dice si la ausencia de Docker debe hacer fallar las pruebas.
func Required() bool { return os.Getenv(RequiredEnv) == "1" }

// Close detiene y borra el contenedor.
func (s *Server) Close() {
	if s == nil {
		return
	}
	_ = testcontainers.TerminateContainer(s.ctr)
}

// NewDatabase crea una base vacía con solo el doble mínimo de auth.users (la
// tabla de GoTrue, de la que public.users depende por FK) y devuelve su URL.
// No aplica migraciones: ver Migrated.
func (s *Server) NewDatabase(ctx context.Context) (string, error) {
	name := fmt.Sprintf("t%d", s.seq.Add(1))

	admin, err := pgx.Connect(ctx, s.baseURL+"/postgres?sslmode=disable")
	if err != nil {
		return "", fmt.Errorf("pgtest: conectar: %w", err)
	}
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	_ = admin.Close(ctx)
	if err != nil {
		return "", fmt.Errorf("pgtest: crear base: %w", err)
	}

	url := s.baseURL + "/" + name + "?sslmode=disable"
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return "", fmt.Errorf("pgtest: conectar a %s: %w", name, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	// En un entorno real esta tabla la crean las migraciones de auth-domicilia;
	// levantar GoTrue entero solo para tener una tabla con la que validar una FK
	// sería desproporcionado: basta con la misma forma que le importa a la FK.
	if _, err := conn.Exec(ctx, `CREATE SCHEMA auth; CREATE TABLE auth.users (id uuid PRIMARY KEY)`); err != nil {
		return "", fmt.Errorf("pgtest: auth.users: %w", err)
	}
	return url, nil
}

// Migrated crea una base nueva con el esquema aplicado por goose y devuelve su
// pool y su URL.
func (s *Server) Migrated(ctx context.Context) (*pgxpool.Pool, string, error) {
	url, err := s.NewDatabase(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := db.Migrate(ctx, url, "up", false, io.Discard); err != nil {
		return nil, "", fmt.Errorf("pgtest: migrar: %w", err)
	}
	pool, err := db.NewPool(ctx, url, 10)
	if err != nil {
		return nil, "", err
	}
	return pool, url, nil
}

// Reset deja la base como la dejan las migraciones: sin datos de negocio, pero CON
// los datos semilla (permisos y roles de sistema). Por eso no usa TRUNCATE ...
// CASCADE: `roles` tiene una FK a `organizations`, y truncar organizaciones en
// cascada borraría también los roles de sistema. Se borra en orden de dependencia.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		DELETE FROM audit_log;
		DELETE FROM unmatched_statuses;
		DELETE FROM webhook_events;
		DELETE FROM payment_events;
		DELETE FROM payments;
		DELETE FROM order_items;
		DELETE FROM orders;
		DELETE FROM user_platform_roles;
		DELETE FROM user_organizations;
		DELETE FROM customers;
		DELETE FROM driver_applications;
		DELETE FROM users;
		DELETE FROM roles WHERE NOT is_system;
		DELETE FROM organizations;
		DELETE FROM auth.users;
		-- Tarifas (00010_pricing): de vuelta a lo que siembra la migración, por si una prueba las
		-- cambió. organization_pricing ya se fue en cascada con organizations.
		UPDATE pricing_settings SET platform_fee_bps = 1000, promo_platform_fee_bps = 500, courier_fee_bps = 0,
			delivery_fee_cents = 0, gateway_plan_code = 'epayco_davivienda', split_enabled = false, updated_by = NULL;
		UPDATE gateway_fee_plans SET card_percent_bps = 264, card_fixed_cents = 69000, international_extra_bps = 80,
			wallet_percent_bps = 264, wallet_fixed_cents = 69000, pse_percent_bps = 264, pse_fixed_cents = 69000,
			pse_small_threshold_cents = 6000000, pse_small_fixed_cents = 220000, vat_bps = 1900, updated_by = NULL
			WHERE code = 'epayco_davivienda';
		UPDATE gateway_fee_plans SET card_percent_bps = 329, card_fixed_cents = 70000, international_extra_bps = 80,
			wallet_percent_bps = 329, wallet_fixed_cents = 70000, pse_percent_bps = 329, pse_fixed_cents = 70000,
			pse_small_threshold_cents = 6000000, pse_small_fixed_cents = 220000, vat_bps = 1900, updated_by = NULL
			WHERE code = 'epayco_otros_bancos';`)
	if err != nil {
		return fmt.Errorf("pgtest: vaciar tablas: %w", err)
	}
	return nil
}

// Skip omite la prueba (o la falla, si RequiredEnv=1) cuando no hay Postgres.
func Skip(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if Required() {
		t.Fatalf("%v (con %s=1 no se omite)", err, RequiredEnv)
	}
	t.Skipf("%v\nPara ejecutarlas: instala/arranca Docker.", err)
}
