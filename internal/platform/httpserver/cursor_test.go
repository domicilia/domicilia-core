package httpserver_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

func TestElCursorDaLaVueltaCompleta(t *testing.T) {
	want := httpserver.Cursor{At: time.Date(2026, 9, 26, 15, 4, 5, 123456000, time.UTC), ID: uuid.New()}
	got, err := httpserver.DecodeCursor(want.Encode())
	if err != nil || got == nil || !got.At.Equal(want.At) || got.ID != want.ID {
		t.Fatalf("cursor = %+v, %v", got, err)
	}
}

func TestSinCursorEsElPrincipio(t *testing.T) {
	got, err := httpserver.DecodeCursor("")
	if err != nil || got != nil {
		t.Fatalf("cursor vacío = %+v, %v", got, err)
	}
}

func TestUnCursorMalformadoEsUn422(t *testing.T) {
	for name, in := range map[string]string{
		"no es base64": "!!!", "no es JSON": "aG9sYQ", "sin id": "eyJhIjoiMjAyNi0wMS0wMVQwMDowMDowMFoifQ", "vacío en JSON": "e30",
	} {
		_, err := httpserver.DecodeCursor(in)
		if !apperr.Is(err, apperr.KindInvalid) {
			t.Errorf("%s: err = %v, quería un error de entrada inválida", name, err)
		}
	}
}

func TestPaginaConCursor(t *testing.T) {
	type row struct {
		At time.Time
		ID uuid.UUID
	}
	pos := func(r row) httpserver.Cursor { return httpserver.Cursor{At: r.At, ID: r.ID} }
	mk := func(n int) []row {
		out := make([]row, n)
		for i := range out {
			out[i] = row{At: time.Unix(int64(1000-i), 0).UTC(), ID: uuid.New()}
		}
		return out
	}

	t.Run("hay más: se recorta a limit y el cursor apunta a la última devuelta", func(t *testing.T) {
		rows := mk(4) // se pidieron limit+1
		p := httpserver.NewCursorPage(rows, 3, pos)
		if len(p.Items) != 3 || p.NextCursor == nil {
			t.Fatalf("página = %d filas, cursor %v", len(p.Items), p.NextCursor)
		}
		c, err := httpserver.DecodeCursor(*p.NextCursor)
		if err != nil || c.ID != rows[2].ID {
			t.Fatalf("el cursor debía apuntar a la 3.ª fila: %+v, %v", c, err)
		}
	})

	t.Run("última página: sin cursor", func(t *testing.T) {
		p := httpserver.NewCursorPage(mk(3), 3, pos)
		if len(p.Items) != 3 || p.NextCursor != nil {
			t.Fatalf("página = %d filas, cursor %v", len(p.Items), p.NextCursor)
		}
	})

	t.Run("vacía: lista vacía, no null", func(t *testing.T) {
		p := httpserver.NewCursorPage[row](nil, 3, pos)
		if p.Items == nil || len(p.Items) != 0 || p.NextCursor != nil {
			t.Fatalf("página = %+v", p)
		}
	})
}
