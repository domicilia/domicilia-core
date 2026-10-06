// Package contacts son las personas con las que una organización habla por WhatsApp. Son POR
// organización: el mismo teléfono puede ser contacto de varios negocios sin que uno vea los
// datos del otro (a diferencia del cliente final de la plataforma, que es una identidad global).
package contacts

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound  = errors.New("contacts: no encontrado")
	ErrDuplicate = errors.New("contacts: teléfono repetido")
)

// Origen del contacto.
const (
	SourceInbound = "inbound" // escribió primero
	SourceManual  = "manual"  // lo creó alguien del equipo
)

// Contact es una persona. No trae más datos que los del negocio: nada de la cuenta de la
// plataforma de esa persona.
type Contact struct {
	ID               uuid.UUID      `json:"id"`
	OrganizationID   uuid.UUID      `json:"organization_id"`
	Phone            string         `json:"phone"`
	Name             *string        `json:"name"`
	Email            *string        `json:"email"`
	CustomAttributes map[string]any `json:"custom_attributes"`
	Blocked          bool           `json:"blocked"`
	Source           string         `json:"source"`
	LastActivityAt   *time.Time     `json:"last_activity_at"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// Límites de los campos.
const (
	maxName          = 255
	maxAttributes    = 50
	maxAttributeKey  = 64
	maxAttributeText = 500
)

var (
	phoneSeparators = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "")
	e164Pattern     = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
)

// NormalizePhone deja un teléfono en E.164 ("+573001234567"). Exige el código de país: adivinarlo
// mandaría mensajes a un número equivocado. Acepta espacios, guiones, puntos y paréntesis.
func NormalizePhone(raw string) (string, error) {
	p := phoneSeparators.Replace(strings.TrimSpace(raw))
	if !e164Pattern.MatchString(p) {
		return "", apperr.Invalid("phone debe estar en formato internacional, con el código de país (p. ej. +573001234567)")
	}
	return p, nil
}

// validateAttributes comprueba los atributos a medida: un objeto plano de valores simples.
func validateAttributes(attrs map[string]any) error {
	if len(attrs) > maxAttributes {
		return apperr.Invalid("custom_attributes admite hasta 50 atributos")
	}
	for k, v := range attrs {
		if k == "" || len(k) > maxAttributeKey {
			return apperr.Invalid("los nombres de custom_attributes deben tener entre 1 y 64 caracteres")
		}
		switch x := v.(type) {
		case nil, bool, float64:
		case string:
			if len(x) > maxAttributeText {
				return apperr.Invalid("los valores de texto de custom_attributes admiten hasta 500 caracteres")
			}
		default:
			return apperr.Invalid("custom_attributes solo admite texto, números y booleanos")
		}
	}
	return nil
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	Insert(ctx context.Context, c NewContact) (Contact, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Contact, error)
	List(ctx context.Context, orgID uuid.UUID, search string, before *Cursor, limit int) ([]Contact, error)
	Update(ctx context.Context, orgID, id uuid.UUID, p Patch) (Contact, error)
}

// NewContact son los datos para crear un contacto a mano.
type NewContact struct {
	OrganizationID uuid.UUID
	Phone          string
	Name           *string
	Email          *string
	Attributes     map[string]any
}

// Patch es un cambio parcial: nil no toca el campo.
type Patch struct {
	SetName    bool
	Name       *string
	SetEmail   bool
	Email      *string
	Attributes map[string]any
	Blocked    *bool
}

// Cursor es la posición de la última fila vista (ver httpserver.Cursor).
type Cursor struct {
	At time.Time
	ID uuid.UUID
}
