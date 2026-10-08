package organizations

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Panaderia Del Centro", "panaderia-del-centro"},
		{"ACME", "acme"},
		{"Mi  Tienda", "mi-tienda"},
		{"Panadería Sol", "panaderia-sol"},
		{"Ñandú & Cía.", "nandu-cia"},
		{"  --Hola--Mundo--  ", "hola-mundo"},
		{"Café 24/7", "cafe-24-7"},
		{"ya-esta-en-slug", "ya-esta-en-slug"},
		{"!!!", ""},
		{"日本語", ""},
	}
	for _, tc := range tests {
		if got := Slugify(tc.in); got != tc.want {
			t.Errorf("Slugify(%q) = %q, quería %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugifyCortaAlMaximoSinDejarUnGuionAlFinal(t *testing.T) {
	// El corte cae justo después de un guion: no debe quedar "aaa...-".
	in := strings.Repeat("a", maxSlug-1) + " b"
	got := Slugify(in)
	if len(got) > maxSlug || strings.HasSuffix(got, "-") {
		t.Fatalf("Slugify = %q (%d), debía caber en %d sin guion final", got, len(got), maxSlug)
	}
	if err := ValidateSlug(got); err != nil {
		t.Fatalf("el slug derivado no es válido: %v", err)
	}
}

func TestValidateSlug(t *testing.T) {
	valid := []string{"acme", "mi-tienda", "cafe-24-7", "abc", strings.Repeat("a", maxSlug)}
	for _, s := range valid {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, debía ser válido", s, err)
		}
	}
	invalid := map[string]string{
		"corto":                  "ab",
		"largo":                  strings.Repeat("a", maxSlug+1),
		"mayúsculas":             "Acme",
		"espacio":                "mi tienda",
		"acento":                 "café",
		"guion inicial":          "-acme",
		"guion final":            "acme-",
		"guiones repetidos":      "mi--tienda",
		"guion bajo":             "mi_tienda",
		"reservado":              "platform",
		"reservado (api)":        "api",
		"reservado (v1)":         "v1",
		"reservado (privacidad)": "privacidad",
		"reservado (terminos)":   "terminos",
		"reservado (pagar)":      "pagar",
	}
	for name, s := range invalid {
		if err := ValidateSlug(s); err == nil {
			t.Errorf("%s: ValidateSlug(%q) debía fallar", name, s)
		}
	}
}

func TestOperacionesDelCicloDeVida(t *testing.T) {
	all := []Status{StatusActive, StatusSuspended, StatusArchived, "otro"}
	tests := []struct {
		op   Operation
		from map[Status]bool // desde qué estados se puede
		to   Status
	}{
		{OpSuspend, map[Status]bool{StatusActive: true}, StatusSuspended},
		{OpReactivate, map[Status]bool{StatusSuspended: true}, StatusActive},
		{OpArchive, map[Status]bool{StatusActive: true, StatusSuspended: true}, StatusArchived},
		{OpRestore, map[Status]bool{StatusArchived: true}, StatusSuspended},
	}
	for _, tc := range tests {
		for _, from := range all {
			if got := tc.op.CanApply(from); got != tc.from[from] {
				t.Errorf("%s desde %s = %v, quería %v", tc.op.Label, from, got, tc.from[from])
			}
		}
		if tc.op.To != tc.to {
			t.Errorf("%s lleva a %s, quería %s", tc.op.Label, tc.op.To, tc.to)
		}
		if tc.op.Action == "" {
			t.Errorf("%s no tiene acción de auditoría", tc.op.Label)
		}
	}
	// Los dos errores que un chequeo solo del destino dejaría pasar.
	if OpSuspend.CanApply(StatusArchived) {
		t.Error("suspender una archivada la sacaría del archivo sin que nadie lo decida")
	}
	if OpRestore.CanApply(StatusActive) {
		t.Error("restaurar una activa la dejaría suspendida")
	}
	// Restaurar NO reactiva: es una decisión aparte.
	if OpRestore.To == StatusActive {
		t.Error("restaurar debe dejar la organización suspendida, no activa")
	}
}

func TestBusinessHoursValidate(t *testing.T) {
	t.Run("acepta y normaliza", func(t *testing.T) {
		got, err := BusinessHours{
			"mon": {{"14:00", "20:00"}, {"08:00", "12:00"}}, // desordenadas
			"tue": {},                                       // día cerrado: se omite
			"sun": {{"22:00", "24:00"}},
		}.Validate()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got["tue"]; ok {
			t.Error("un día sin franjas debe omitirse")
		}
		if got["mon"][0].Open != "08:00" || got["mon"][1].Open != "14:00" {
			t.Errorf("franjas sin ordenar: %v", got["mon"])
		}
	})

	invalid := map[string]BusinessHours{
		"día desconocido":       {"lunes": {{"08:00", "12:00"}}},
		"hora mal escrita":      {"mon": {{"8:00", "12:00"}}},
		"hora fuera de rango":   {"mon": {{"08:00", "25:00"}}},
		"minutos fuera":         {"mon": {{"08:60", "12:00"}}},
		"24:00 con minutos":     {"mon": {{"08:00", "24:30"}}},
		"apertura a las 24:00":  {"mon": {{"24:00", "24:00"}}},
		"apertura tras cierre":  {"mon": {{"20:00", "08:00"}}},
		"franja vacía":          {"mon": {{"08:00", "08:00"}}},
		"franjas traslapadas":   {"mon": {{"08:00", "12:00"}, {"11:00", "15:00"}}},
		"demasiadas franjas":    {"mon": {{"01:00", "02:00"}, {"03:00", "04:00"}, {"05:00", "06:00"}, {"07:00", "08:00"}}},
		"texto en vez de hora":  {"mon": {{"abierto", "cerrado"}}},
		"separador equivocado":  {"mon": {{"08.00", "12.00"}}},
		"franjas que se tocan?": {"mon": {{"08:00", "12:00"}, {"11:59", "15:00"}}},
	}
	for name, h := range invalid {
		if _, err := h.Validate(); err == nil {
			t.Errorf("%s: debía fallar", name)
		}
	}

	t.Run("dos franjas contiguas no se traslapan", func(t *testing.T) {
		if _, err := (BusinessHours{"mon": {{"08:00", "12:00"}, {"12:00", "15:00"}}}).Validate(); err != nil {
			t.Fatalf("12:00 cierra una y abre la otra: %v", err)
		}
	})
}

func TestBusinessHoursOpenAt(t *testing.T) {
	bogota, err := time.LoadLocation("America/Bogota")
	if err != nil {
		t.Fatal(err)
	}
	h := BusinessHours{
		"mon": {{"08:00", "12:00"}, {"14:00", "20:00"}},
		"sun": {{"22:00", "24:00"}},
	}
	// 2026-09-28 es lunes; 2026-09-27 es domingo. Bogotá es UTC-5 sin horario de verano.
	at := func(day string, hour, minute int) time.Time {
		d, perr := time.ParseInLocation("2006-01-02", day, bogota)
		if perr != nil {
			t.Fatal(perr)
		}
		return d.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}
	tests := []struct {
		name string
		when time.Time
		want bool
	}{
		{"lunes dentro de la mañana", at("2026-09-28", 9, 30), true},
		{"lunes justo a la apertura", at("2026-09-28", 8, 0), true},
		{"lunes justo al cierre: cerrado", at("2026-09-28", 12, 0), false},
		{"lunes en el descanso", at("2026-09-28", 13, 0), false},
		{"lunes por la tarde", at("2026-09-28", 19, 59), true},
		{"martes sin franjas: cerrado", at("2026-09-29", 10, 0), false},
		{"domingo hasta la medianoche", at("2026-09-27", 23, 59), true},
	}
	for _, tc := range tests {
		got := h.OpenAt(tc.when, bogota)
		if got == nil || *got != tc.want {
			t.Errorf("%s: OpenAt = %v, quería %v", tc.name, got, tc.want)
		}
	}

	t.Run("la zona horaria manda: el mismo instante se lee distinto", func(t *testing.T) {
		// Lunes 19:30 en Bogotá (abierto) es martes 00:30 UTC (martes no tiene franjas).
		inst := at("2026-09-28", 19, 30)
		if got := h.OpenAt(inst, bogota); got == nil || !*got {
			t.Error("lunes 19:30 en Bogotá debía estar abierto")
		}
		if got := h.OpenAt(inst, time.UTC); got == nil || *got {
			t.Error("leído en UTC es martes 00:30 y debía estar cerrado")
		}
	})

	t.Run("sin horario configurado no se sabe: nil, no cerrado", func(t *testing.T) {
		if got := (BusinessHours{}).OpenAt(time.Now(), bogota); got != nil {
			t.Fatalf("OpenAt sin horario = %v, quería nil", *got)
		}
		if got := BusinessHours(nil).OpenAt(time.Now(), bogota); got != nil {
			t.Fatal("OpenAt con horario nil debía ser nil")
		}
	})
}

func ptr[T any](v T) *T { return &v }

func TestSettingsPatchApply(t *testing.T) {
	cur := DefaultSettings()

	t.Run("un cambio parcial solo toca lo enviado y lo reporta", func(t *testing.T) {
		got, changed, err := SettingsPatch{City: ptr("Bogotá"), ContactPhone: ptr("+57 (300) 123-4567")}.Apply(cur)
		if err != nil {
			t.Fatal(err)
		}
		if got.City == nil || *got.City != "Bogotá" {
			t.Errorf("city = %v", got.City)
		}
		if got.ContactPhone == nil || *got.ContactPhone != "+573001234567" {
			t.Errorf("el teléfono debía normalizarse: %v", got.ContactPhone)
		}
		if got.Timezone != DefaultTimezone || got.Currency != DefaultCurrency {
			t.Error("lo no enviado debía quedar igual")
		}
		if strings.Join(changed, ",") != "contact_phone,city" {
			t.Errorf("changed = %v", changed)
		}
	})

	t.Run("no reporta un campo que no cambió", func(t *testing.T) {
		withCity := cur
		withCity.City = ptr("Cali")
		_, changed, err := SettingsPatch{City: ptr("Cali"), Currency: ptr("cop")}.Apply(withCity)
		if err != nil {
			t.Fatal(err)
		}
		if len(changed) != 0 {
			t.Errorf("no cambió nada y reporta %v", changed)
		}
	})

	t.Run("una cadena vacía deja el campo en blanco", func(t *testing.T) {
		withCity := cur
		withCity.City = ptr("Cali")
		got, changed, err := SettingsPatch{City: ptr("  ")}.Apply(withCity)
		if err != nil {
			t.Fatal(err)
		}
		if got.City != nil || len(changed) != 1 {
			t.Errorf("city = %v, changed = %v", got.City, changed)
		}
	})

	t.Run("normaliza correo, moneda y horario", func(t *testing.T) {
		hours := BusinessHours{"mon": {{"14:00", "20:00"}, {"08:00", "12:00"}}}
		got, _, err := SettingsPatch{ContactEmail: ptr("  Ventas@Ejemplo.COM "), Currency: ptr("usd"), BusinessHours: &hours}.Apply(cur)
		if err != nil {
			t.Fatal(err)
		}
		if *got.ContactEmail != "ventas@ejemplo.com" || got.Currency != "USD" || got.BusinessHours["mon"][0].Open != "08:00" {
			t.Errorf("got = %+v", got)
		}
	})

	invalid := map[string]SettingsPatch{
		"correo inválido":       {ContactEmail: ptr("no-es-correo")},
		"teléfono corto":        {ContactPhone: ptr("123")},
		"teléfono con letras":   {ContactPhone: ptr("300abc4567")},
		"logo http":             {LogoURL: ptr("http://ejemplo.com/logo.png")},
		"logo sin host":         {LogoURL: ptr("https:///logo.png")},
		"logo no es url":        {LogoURL: ptr("logo.png")},
		"zona horaria inválida": {Timezone: ptr("Marte/Olimpo")},
		"zona horaria Local":    {Timezone: ptr("Local")},
		"zona horaria vacía":    {Timezone: ptr("")},
		"locale inválido":       {Locale: ptr("español")},
		"moneda inválida":       {Currency: ptr("PESOS")},
		"nit demasiado largo":   {TaxID: ptr(strings.Repeat("9", maxTaxID+1))},
		"dirección muy larga":   {Address: ptr(strings.Repeat("x", maxAddress+1))},
		"horario inválido":      {BusinessHours: &BusinessHours{"mon": {{"20:00", "08:00"}}}},
	}
	for name, p := range invalid {
		if _, _, err := p.Apply(cur); err == nil {
			t.Errorf("%s: debía fallar", name)
		}
	}
}

func TestTokens(t *testing.T) {
	a, hashA, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("dos tokens iguales")
	}
	if len(a) < 43 { // 32 bytes en base64 sin relleno
		t.Errorf("token demasiado corto: %d caracteres", len(a))
	}
	if !bytes.Equal(hashA, hashToken(a)) {
		t.Error("el hash del token debe ser determinista")
	}
	if strings.Contains(string(hashA), a) {
		t.Error("el hash no debe contener el token")
	}
}
