// Command api es el punto de entrada del core API de Domicilia.
// Solo cablea dependencias: la lógica vive en internal/.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/app"
	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/platform/logging"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(runHealthcheck())
		case "migrate":
			exitOnError(runMigrate(os.Args[2:]))
			return
		case "create-superadmin":
			exitOnError(runCreateSuperadmin())
			return
		case "seed":
			exitOnError(runSeed())
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg.LogLevel, cfg.IsProduction(), os.Stdout)
	slog.SetDefault(log)

	// SIGTERM es lo que manda Docker al parar el contenedor.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	mailer, err := newMailer(cfg, log)
	if err != nil {
		return err
	}

	deps := app.Deps{
		Log:      log,
		Config:   cfg,
		Pool:     pool,
		Identity: gotrue.New(cfg.AuthURL, cfg.JWTSecret, nil),
		Mailer:   mailer,
	}
	// Los trabajadores (entrega de mensajes) corren en este mismo proceso y se detienen con él.
	workers := app.NewWorkers(deps)
	deps.Workers = workers
	e := app.New(deps)

	// Si el servidor termina por un error (y no por una señal), los trabajadores también deben parar: sin
	// cancelar su contexto, wg.Wait esperaría para siempre.
	workCtx, stopWorkers := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		workers.Run(workCtx)
	}()
	defer wg.Wait()
	defer stopWorkers()

	sc := echo.StartConfig{
		Address:         cfg.HTTPAddr,
		HideBanner:      true,
		HidePort:        true,
		GracefulTimeout: cfg.ShutdownTimeout,
		// Timeouts del servidor: sin ellos un cliente lento (slowloris) retiene conexiones.
		BeforeServeFunc: func(s *http.Server) error {
			s.ReadHeaderTimeout = 5 * time.Second
			s.ReadTimeout = 15 * time.Second
			s.WriteTimeout = cfg.RequestTimeout + 5*time.Second
			s.IdleTimeout = 60 * time.Second
			return nil
		},
	}

	log.Info("core api iniciando", "addr", cfg.HTTPAddr, "env", cfg.Env)
	if err := sc.Start(ctx, e); err != nil {
		return fmt.Errorf("servidor: %w", err)
	}
	log.Info("core api detenido")
	return nil
}
