package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

const readyTimeout = 2 * time.Second

// healthz: ¿el proceso está vivo? No toca dependencias — si esto falla,
// reiniciar el contenedor es lo correcto.
func healthz(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// readyz: ¿puede atender tráfico ahora? Comprueba las dependencias (base de
// datos). Si falla NO se reinicia nada: solo se deja de mandarle tráfico.
func readyz(ready func(context.Context) error) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if ready != nil {
			ctx, cancel := context.WithTimeout(c.Request().Context(), readyTimeout)
			defer cancel()
			if err := ready(ctx); err != nil {
				c.Logger().Warn("readyz: dependencia no disponible", "error", err)
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			}
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	}
}
