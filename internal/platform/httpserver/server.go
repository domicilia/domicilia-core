// Package httpserver arma el servidor Echo: middleware, errores y rutas base.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// Options desacopla el servidor de la configuración y de la base de datos:
// las pruebas lo construyen sin ninguna de las dos.
type Options struct {
	Logger         *slog.Logger
	CORSOrigins    []string
	RequestTimeout time.Duration
	MaxBodyBytes   int64
	// Ready comprueba las dependencias para /readyz. nil = siempre listo.
	Ready func(context.Context) error
}

// New devuelve el Echo con el middleware común y las rutas de salud. Los
// módulos de negocio cuelgan de la rama /v1 (ver V1), con timeout por petición.
func New(opt Options) *echo.Echo {
	e := echo.New()
	// Sin esto, c.Logger() (Recover, readyz, ...) usa el logger por defecto de
	// Echo: otro formato y sin nuestro nivel/handler.
	e.Logger = opt.Logger
	e.HTTPErrorHandler = errorHandler(opt.Logger)

	// Orden: el request ID primero, para que lo vea todo lo demás (logs, errores).
	e.Use(middleware.RequestID())
	e.Use(middleware.Recover())
	e.Use(requestLogger(opt.Logger))
	e.Use(middleware.BodyLimit(opt.MaxBodyBytes))
	e.Use(middleware.Secure())
	if len(opt.CORSOrigins) > 0 {
		e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
			AllowOrigins:     opt.CORSOrigins,
			AllowCredentials: true,
		}))
	}

	e.GET("/healthz", healthz)
	e.GET("/readyz", readyz(opt.Ready))

	return e
}

// V1 devuelve el grupo /v1 con timeout por petición y, después, el middleware
// que se le pase (autenticación). Las rutas de salud quedan fuera a propósito:
// no deben cortarse por timeout de negocio ni exigir sesión.
func V1(e *echo.Echo, timeout time.Duration, mw ...echo.MiddlewareFunc) *echo.Group {
	return e.Group("/v1", append([]echo.MiddlewareFunc{middleware.ContextTimeout(timeout)}, mw...)...)
}

func requestLogger(log *slog.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		// Las sondas de salud llegan cada pocos segundos: sin esto el log se llena de ruido.
		Skipper: func(c *echo.Context) bool {
			p := c.Request().URL.Path
			return p == "/healthz" || p == "/readyz"
		},
		LogStatus:    true,
		LogMethod:    true,
		LogURIPath:   true,
		LogLatency:   true,
		LogRemoteIP:  true,
		LogRequestID: true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			level := slog.LevelInfo
			if v.Status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			log.LogAttrs(c.Request().Context(), level, "request",
				slog.String("method", v.Method),
				slog.String("path", v.URIPath),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
				slog.String("request_id", v.RequestID),
				slog.String("remote_ip", v.RemoteIP),
			)
			return nil
		},
	})
}
