// Package logging construye el slog.Logger del servicio.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New devuelve un logger JSON en staging/producción (para que lo lea una
// herramienta) y de texto en desarrollo (para que lo lea una persona).
func New(level string, production bool, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	if production {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
