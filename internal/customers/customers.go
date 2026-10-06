// Package customers es el cliente B2C: quien pide domicilios. Extiende a un usuario
// (la identidad y la autenticación son de `users` y de GoTrue) con lo que es propio
// de ser cliente: hoy teléfono y dirección; después direcciones, historial de
// pedidos y el resto que nazca aquí.
//
// Es un paquete hoja: no importa a ningún otro dominio, para que `users` pueda
// componer el perfil de sesión (/users/me) sin ciclos.
package customers

import (
	"errors"

	"github.com/google/uuid"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound      = errors.New("customers: sin perfil de cliente")
	ErrProfileExists = errors.New("customers: el perfil ya existe")
	ErrEmailTaken    = errors.New("customers: correo ya registrado")
)

// Profile es lo específico de ser cliente.
type Profile struct {
	Phone          *string `json:"phone"`
	DefaultAddress *string `json:"default_address"`
}

// ProfileInput son los datos que el cliente edita. Un campo ausente (o null) no se
// toca: no se puede vaciar un dato mandándolo en null.
type ProfileInput struct {
	Phone          *string `json:"phone"`
	DefaultAddress *string `json:"default_address"`
}

// SignUpInput son los datos opcionales del alta de un cliente.
type SignUpInput struct {
	FullName       *string `json:"full_name"`
	Phone          *string `json:"phone"`
	DefaultAddress *string `json:"default_address"`
}

// Registration es el resultado del alta: el usuario creado.
type Registration struct {
	UserID   uuid.UUID
	Email    string
	FullName *string
}

// Límites de las columnas.
const (
	maxFullName = 255
	maxPhone    = 50
	maxAddress  = 500
)
