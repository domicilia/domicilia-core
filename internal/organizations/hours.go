package organizations

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	// Embebe la base de zonas horarias: la imagen de producción (distroless) no
	// garantiza tener /usr/share/zoneinfo, y sin ella LoadLocation falla.
	_ "time/tzdata"
)

// Días de la semana, en el orden en que se ordenan las claves y con el que las
// escribe el frontend.
var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

const (
	maxIntervalsPerDay = 3
	minutesPerDay      = 24 * 60
)

// Interval es una franja de atención en hora local del negocio, "HH:MM".
type Interval struct {
	Open  string `json:"open"`
	Close string `json:"close"`
}

// BusinessHours son las franjas de cada día: {"mon": [{"open":"08:00","close":"20:00"}]}.
// Un día ausente o vacío es un día cerrado. Una franja no cruza la medianoche: para
// atender de 22:00 a 02:00 se escribe "22:00"-"24:00" un día y "00:00"-"02:00" el
// siguiente.
type BusinessHours map[string][]Interval

// parseClock convierte "HH:MM" en minutos desde la medianoche. "24:00" es válido y
// solo sirve como cierre.
func parseClock(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, fmt.Errorf("hora %q no tiene la forma HH:MM", s)
	}
	hh, errH := strconv.Atoi(h)
	mm, errM := strconv.Atoi(m)
	if errH != nil || errM != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 || (hh == 24 && mm != 0) {
		return 0, fmt.Errorf("hora %q fuera de rango", s)
	}
	return hh*60 + mm, nil
}

// Validate comprueba las franjas y devuelve la forma canónica: solo los días con
// franjas, cada día ordenado. Rechaza días desconocidos, horas mal escritas, franjas
// vacías o invertidas, traslapadas, y más de tres por día.
func (h BusinessHours) Validate() (BusinessHours, error) {
	out := make(BusinessHours, len(h))
	for day, intervals := range h {
		if !slices.Contains(weekdays, day) {
			return nil, fmt.Errorf("business_hours: día desconocido %q (usa %s)", day, strings.Join(weekdays, ", "))
		}
		if len(intervals) == 0 {
			continue // día cerrado
		}
		if len(intervals) > maxIntervalsPerDay {
			return nil, fmt.Errorf("business_hours: %s admite hasta %d franjas", day, maxIntervalsPerDay)
		}
		type span struct{ open, close int }
		spans := make([]span, 0, len(intervals))
		for _, iv := range intervals {
			o, err := parseClock(iv.Open)
			if err != nil {
				return nil, fmt.Errorf("business_hours: %s: %w", day, err)
			}
			c, err := parseClock(iv.Close)
			if err != nil {
				return nil, fmt.Errorf("business_hours: %s: %w", day, err)
			}
			if o >= minutesPerDay {
				return nil, fmt.Errorf("business_hours: %s: 24:00 solo puede ser el cierre", day)
			}
			if o >= c {
				return nil, fmt.Errorf("business_hours: %s: la apertura %s debe ser anterior al cierre %s", day, iv.Open, iv.Close)
			}
			spans = append(spans, span{o, c})
		}
		slices.SortFunc(spans, func(a, b span) int { return a.open - b.open })
		for i := 1; i < len(spans); i++ {
			if spans[i].open < spans[i-1].close {
				return nil, fmt.Errorf("business_hours: %s: las franjas se traslapan", day)
			}
		}
		canon := make([]Interval, 0, len(spans))
		for _, s := range spans {
			canon = append(canon, Interval{Open: formatClock(s.open), Close: formatClock(s.close)})
		}
		out[day] = canon
	}
	return out, nil
}

func formatClock(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// OpenAt dice si el negocio está abierto en el instante t, leído en la zona horaria
// loc. Devuelve nil si no hay ningún horario configurado: "no sé" no es "cerrado".
func (h BusinessHours) OpenAt(t time.Time, loc *time.Location) *bool {
	if len(h) == 0 {
		return nil
	}
	local := t.In(loc)
	day := weekdays[(int(local.Weekday())+6)%7] // time.Sunday = 0; mon es el primero
	now := local.Hour()*60 + local.Minute()
	open := false
	for _, iv := range h[day] {
		// Las franjas ya pasaron Validate; un error aquí solo puede ser un dato viejo.
		o, errO := parseClock(iv.Open)
		c, errC := parseClock(iv.Close)
		if errO == nil && errC == nil && now >= o && now < c {
			open = true
			break
		}
	}
	return &open
}
