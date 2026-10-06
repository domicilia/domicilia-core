package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/seed"
)

// firstEnv devuelve la primera variable definida. El seed de Python leía
// EMPLOYEE_* mientras su .env.example documentaba ORG_EMPLOYEE_*: se aceptan ambos.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// runSeed atiende `api seed`: crea los datos de demo del entorno de desarrollo (una
// organización y un usuario por rol). Lee las mismas variables que el antiguo
// scripts/seed.py: ADMIN_EMAIL/ADMIN_PASSWORD (superadmin), ORG_ADMIN_EMAIL/
// ORG_ADMIN_PASSWORD, EMPLOYEE_EMAIL/EMPLOYEE_PASSWORD (o ORG_EMPLOYEE_*) y ADMIN_ORG (nombre de la
// organización). Necesita auth-domicilia levantado. Se niega en staging/producción.
func runSeed() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.IsProduction() {
		return errors.New("seed es solo para desarrollo: usa create-superadmin y las invitaciones en staging/producción")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	return seed.Run(ctx, pool, gotrue.New(cfg.AuthURL, cfg.JWTSecret, nil), seed.Options{
		OrgName:  os.Getenv("ADMIN_ORG"),
		Admin:    seed.Account{Email: os.Getenv("ADMIN_EMAIL"), Password: os.Getenv("ADMIN_PASSWORD")},
		OrgAdmin: seed.Account{Email: os.Getenv("ORG_ADMIN_EMAIL"), Password: os.Getenv("ORG_ADMIN_PASSWORD")},
		Employee: seed.Account{Email: firstEnv("EMPLOYEE_EMAIL", "ORG_EMPLOYEE_EMAIL"), Password: firstEnv("EMPLOYEE_PASSWORD", "ORG_EMPLOYEE_PASSWORD")},
	}, os.Stdout)
}
