package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Paginación por cursor: para listados que crecen sin parar y cambian mientras se leen
// (conversaciones, mensajes, contactos). Un offset se corre cuando llega un mensaje nuevo y
// repite o se salta filas; el cursor apunta a la ÚLTIMA fila vista y no se mueve.
//
// El cursor es opaco para el cliente. No es un secreto ni una credencial: solo una posición;
// uno alterado como mucho cambia el punto de partida de un listado que el usuario ya puede ver,
// y uno ilegible es un 422.

// DefaultCursorLimit y MaxCursorLimit acotan una página.
const (
	DefaultCursorLimit = 30
	MaxCursorLimit     = 100
)

// Cursor es la posición de la última fila devuelta: su instante y su id (desempata filas del
// mismo instante).
type Cursor struct {
	At time.Time `json:"a"`
	ID uuid.UUID `json:"i"`
}

// Encode devuelve el cursor como texto opaco.
func (c Cursor) Encode() string {
	b, _ := json.Marshal(c) // una estructura de tipos simples no puede fallar
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor lee un cursor. Un texto vacío no es un error: significa "desde el principio".
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil //nolint:nilnil // "sin cursor" es un resultado válido
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, apperr.Invalid("cursor no es válido")
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.At.IsZero() || c.ID == uuid.Nil {
		return nil, apperr.Invalid("cursor no es válido")
	}
	return &c, nil
}

// CursorPage es la respuesta de un listado por cursor. NextCursor es nil en la última página.
type CursorPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// NewCursorPage arma la respuesta. Se pide UNA fila de más al listar para saber si hay otra
// página sin contar: si llegaron más de limit, se recorta y se calcula el cursor con la última
// que sí se devuelve (position dice su cursor).
func NewCursorPage[T any](items []T, limit int, position func(T) Cursor) CursorPage[T] {
	page := CursorPage[T]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		next := position(page.Items[limit-1]).Encode()
		page.NextCursor = &next
	}
	if page.Items == nil {
		page.Items = []T{}
	}
	return page
}

// CursorParams lee `limit` y `cursor` de la consulta. Sin limit usa DefaultCursorLimit; uno fuera
// de 1..MaxCursorLimit es un 422.
func CursorParams(c *echo.Context) (limit int, cur *Cursor, err error) {
	limit = DefaultCursorLimit
	if s := c.QueryParam("limit"); s != "" {
		n, perr := strconv.Atoi(s)
		if perr != nil || n < 1 || n > MaxCursorLimit {
			return 0, nil, apperr.Invalid("limit debe estar entre 1 y " + strconv.Itoa(MaxCursorLimit))
		}
		limit = n
	}
	cur, err = DecodeCursor(c.QueryParam("cursor"))
	return limit, cur, err
}
