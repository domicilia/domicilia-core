package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/db"
)

const migrateUsage = "uso: api migrate <up|down|status|adopt>"

// runMigrate atiende `api migrate <acción>`. Las migraciones van embebidas en
// el binario, así que sirve igual en la imagen distroless (sin shell).
func runMigrate(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("%s", migrateUsage)
	}
	cfg, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return db.Migrate(ctx, cfg.URL, args[0], cfg.IsProduction(), os.Stdout)
}
