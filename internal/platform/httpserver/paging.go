package httpserver

import (
	"strconv"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Límites de paginación: una página nunca pide más de MaxPageSize filas.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Page es una ventana de resultados.
type Page struct {
	Limit  int
	Offset int
}

// Paged es la respuesta de un listado paginado.
type Paged[T any] struct {
	Items  []T   `json:"items"`
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// NewPaged arma la respuesta. Un items nil sale como [] y no como null.
func NewPaged[T any](items []T, total int64, p Page) Paged[T] {
	if items == nil {
		items = []T{}
	}
	return Paged[T]{Items: items, Total: total, Limit: p.Limit, Offset: p.Offset}
}

// PageParams lee `limit` y `offset` de la consulta. Sin `limit` usa
// DefaultPageSize; uno fuera de 1..MaxPageSize o un offset negativo es un 422.
func PageParams(c *echo.Context) (Page, error) {
	p := Page{Limit: DefaultPageSize}
	if s := c.QueryParam("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > MaxPageSize {
			return Page{}, apperr.Invalid("limit debe estar entre 1 y " + strconv.Itoa(MaxPageSize))
		}
		p.Limit = n
	}
	if s := c.QueryParam("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return Page{}, apperr.Invalid("offset debe ser un número mayor o igual a 0")
		}
		p.Offset = n
	}
	return p, nil
}
