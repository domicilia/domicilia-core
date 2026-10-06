package httpserver

import (
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Bind decodifica el cuerpo JSON en dst. Un cuerpo vacío deja dst intacto (los
// campos opcionales quedan en su valor cero); uno mal formado es un 400.
func Bind(c *echo.Context, dst any) error {
	return echo.BindBody(c, dst)
}

// UUIDParam lee un parámetro de ruta que debe ser un UUID. Uno mal formado es un
// 422, no un 404: el identificador no es de un recurso que "no existe", es
// inválido.
func UUIDParam(c *echo.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		return uuid.Nil, apperr.Invalid(name + " no es un identificador válido")
	}
	return id, nil
}
