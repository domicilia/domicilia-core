package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

const problemContentType = "application/problem+json"

// Problem sigue RFC 9457 (problem details): el mismo formato de error para
// toda la API, así el frontend y el servicio de IA lo tratan igual.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// errorHandler convierte cualquier error en un Problem. Los 5xx nunca exponen
// el detalle al cliente (puede traer rutas, SQL o nombres internos): se
// registra en el log con el request_id para poder correlacionarlo.
func errorHandler(log *slog.Logger) echo.HTTPErrorHandler {
	return func(c *echo.Context, err error) {
		if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
			return
		}

		code := http.StatusInternalServerError
		var sc echo.HTTPStatusCoder
		if errors.As(err, &sc) {
			if v := sc.StatusCode(); v != 0 {
				code = v
			}
		}

		requestID := c.Response().Header().Get(echo.HeaderXRequestID)

		detail := ""
		if code >= http.StatusInternalServerError {
			log.Error("error interno", "error", err, "request_id", requestID, "path", c.Request().URL.Path)
		} else if he, ok := errors.AsType[*echo.HTTPError](err); ok {
			detail = he.Message
		} else if ae, ok := errors.AsType[*apperr.Error](err); ok {
			detail = ae.Message
		}

		p := Problem{
			Type:      "about:blank",
			Title:     http.StatusText(code),
			Status:    code,
			Detail:    detail,
			Instance:  c.Request().URL.Path,
			RequestID: requestID,
		}

		if c.Request().Method == http.MethodHead {
			_ = c.NoContent(code)
			return
		}
		body, mErr := json.Marshal(p)
		if mErr != nil {
			_ = c.NoContent(code)
			return
		}
		if bErr := c.Blob(code, problemContentType, body); bErr != nil {
			log.Warn("no se pudo enviar el error al cliente", "error", bErr, "request_id", requestID)
		}
	}
}
