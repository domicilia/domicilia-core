package organizations

import (
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

// Límites de los ajustes.
const (
	maxLegalName = 255
	maxTaxID     = 30
	maxAddress   = 255
	maxCity      = 100
	maxLogoURL   = 500
)

var (
	phonePattern  = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
	localePattern = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	currencyRegex = regexp.MustCompile(`^[A-Z]{3}$`)
	phoneNoise    = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "")
)

// Valores por omisión de una organización nueva (Colombia).
const (
	DefaultTimezone = "America/Bogota"
	DefaultLocale   = "es-CO"
	DefaultCurrency = "COP"
)

// Settings son los ajustes del negocio: datos de contacto, cómo se ve y cuándo atiende.
type Settings struct {
	LegalName     *string       `json:"legal_name"`
	TaxID         *string       `json:"tax_id"`
	ContactEmail  *string       `json:"contact_email"`
	ContactPhone  *string       `json:"contact_phone"`
	Address       *string       `json:"address"`
	City          *string       `json:"city"`
	LogoURL       *string       `json:"logo_url"`
	Timezone      string        `json:"timezone"`
	Locale        string        `json:"locale"`
	Currency      string        `json:"currency"`
	BusinessHours BusinessHours `json:"business_hours"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// DefaultSettings son los ajustes de una organización recién creada.
func DefaultSettings() Settings {
	return Settings{Timezone: DefaultTimezone, Locale: DefaultLocale, Currency: DefaultCurrency, BusinessHours: BusinessHours{}}
}

// SettingsPatch es un cambio parcial. nil no toca el campo; en los textos opcionales,
// una cadena vacía lo deja en blanco.
type SettingsPatch struct {
	LegalName     *string        `json:"legal_name"`
	TaxID         *string        `json:"tax_id"`
	ContactEmail  *string        `json:"contact_email"`
	ContactPhone  *string        `json:"contact_phone"`
	Address       *string        `json:"address"`
	City          *string        `json:"city"`
	LogoURL       *string        `json:"logo_url"`
	Timezone      *string        `json:"timezone"`
	Locale        *string        `json:"locale"`
	Currency      *string        `json:"currency"`
	BusinessHours *BusinessHours `json:"business_hours"`
}

// optionalText aplica un campo de texto opcional: nil no toca, vacío borra, y lo demás
// pasa por check (que devuelve el valor normalizado).
func optionalText(cur *string, in *string, name string, check func(string) (string, error), changed *[]string) (*string, error) {
	if in == nil {
		return cur, nil
	}
	v := strings.TrimSpace(*in)
	if v == "" {
		if cur != nil {
			*changed = append(*changed, name)
		}
		return nil, nil
	}
	v, err := check(v)
	if err != nil {
		return nil, err
	}
	if cur == nil || *cur != v {
		*changed = append(*changed, name)
	}
	return &v, nil
}

func maxRunes(field string, limit int) func(string) (string, error) {
	return func(s string) (string, error) { return s, validate.MaxLen(field, s, limit) }
}

func checkEmail(s string) (string, error) { return validate.Email("contact_email", s) }

func checkPhone(s string) (string, error) {
	p := phoneNoise.Replace(s)
	if !phonePattern.MatchString(p) {
		return "", apperr.Invalid("contact_phone debe tener entre 7 y 15 dígitos, con + opcional al inicio")
	}
	return p, nil
}

func checkLogoURL(s string) (string, error) {
	if utf8.RuneCountInString(s) > maxLogoURL {
		return "", apperr.Invalid("logo_url es demasiado largo")
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", apperr.Invalid("logo_url debe ser una URL https")
	}
	return s, nil
}

// Apply fusiona el cambio con los ajustes actuales, validando cada campo. Devuelve los
// ajustes resultantes y los nombres de los campos que de verdad cambiaron (para la
// auditoría; no se guardan los valores).
func (p SettingsPatch) Apply(cur Settings) (Settings, []string, error) {
	out := cur
	var changed []string
	var err error

	if out.LegalName, err = optionalText(cur.LegalName, p.LegalName, "legal_name", maxRunes("legal_name", maxLegalName), &changed); err != nil {
		return Settings{}, nil, err
	}
	if out.TaxID, err = optionalText(cur.TaxID, p.TaxID, "tax_id", maxRunes("tax_id", maxTaxID), &changed); err != nil {
		return Settings{}, nil, err
	}
	if out.ContactEmail, err = optionalText(cur.ContactEmail, p.ContactEmail, "contact_email", checkEmail, &changed); err != nil {
		return Settings{}, nil, err
	}
	if out.ContactPhone, err = optionalText(cur.ContactPhone, p.ContactPhone, "contact_phone", checkPhone, &changed); err != nil {
		return Settings{}, nil, err
	}
	if out.Address, err = optionalText(cur.Address, p.Address, "address", maxRunes("address", maxAddress), &changed); err != nil {
		return Settings{}, nil, err
	}
	if out.City, err = optionalText(cur.City, p.City, "city", maxRunes("city", maxCity), &changed); err != nil {
		return Settings{}, nil, err
	}
	if out.LogoURL, err = optionalText(cur.LogoURL, p.LogoURL, "logo_url", checkLogoURL, &changed); err != nil {
		return Settings{}, nil, err
	}

	if p.Timezone != nil {
		tz := strings.TrimSpace(*p.Timezone)
		if _, lerr := time.LoadLocation(tz); tz == "" || tz == "Local" || lerr != nil {
			return Settings{}, nil, apperr.Invalid("timezone no es una zona horaria IANA válida (p. ej. America/Bogota)")
		}
		if tz != cur.Timezone {
			changed = append(changed, "timezone")
		}
		out.Timezone = tz
	}
	if p.Locale != nil {
		l := strings.TrimSpace(*p.Locale)
		if !localePattern.MatchString(l) {
			return Settings{}, nil, apperr.Invalid("locale debe tener la forma es-CO")
		}
		if l != cur.Locale {
			changed = append(changed, "locale")
		}
		out.Locale = l
	}
	if p.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*p.Currency))
		if !currencyRegex.MatchString(c) {
			return Settings{}, nil, apperr.Invalid("currency debe ser un código de tres letras (p. ej. COP)")
		}
		if c != cur.Currency {
			changed = append(changed, "currency")
		}
		out.Currency = c
	}
	if p.BusinessHours != nil {
		h, herr := p.BusinessHours.Validate()
		if herr != nil {
			return Settings{}, nil, apperr.Invalid(herr.Error())
		}
		out.BusinessHours = h
		changed = append(changed, "business_hours")
	}
	return out, changed, nil
}
