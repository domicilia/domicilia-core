// Package drivers son las postulaciones de domiciliarios ("domis"). Nunca hay
// signup directo: la postulación es la única puerta de entrada, y por sí misma
// no crea ninguna identidad. Solo al aprobarla el superadmin se crea la cuenta en
// auth-domicilia y el perfil de negocio con is_delivery.
//
// Los domis no pertenecen a una organización (reparten para cualquiera), así que
// solo el superadmin de plataforma revisa, no un admin de organización.
package drivers

import (
	"errors"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound   = errors.New("drivers: solicitud no encontrada")
	ErrNotPending = errors.New("drivers: la solicitud ya fue revisada")
	ErrEmailTaken = errors.New("drivers: correo ya registrado")
)

// Status es el estado de una postulación.
type Status string

// Estados posibles: pending -> approved | rejected. Es una vía de un solo
// sentido: una solicitud revisada no se vuelve a revisar.
const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

// ParseStatus valida un estado recibido del cliente (filtro del listado).
func ParseStatus(s string) (Status, error) {
	switch st := Status(s); st {
	case StatusPending, StatusApproved, StatusRejected:
		return st, nil
	default:
		return "", apperr.Invalid("status_filter debe ser pending, approved o rejected")
	}
}

// Application es una postulación de domiciliario.
type Application struct {
	ID          uuid.UUID `json:"id"`
	FullName    string    `json:"full_name"`
	Email       string    `json:"email"`
	Phone       string    `json:"phone"`
	VehicleType string    `json:"vehicle_type"`
	Status      Status    `json:"status"`
}

// ApprovedApplication es la respuesta de aprobar: la solicitud más la
// contraseña temporal de la cuenta recién creada.
//
// La contraseña temporal solo es aceptable mientras no haya proveedor de correo
// en GoTrue: el flujo real es la invitación por correo (POST /invite de GoTrue).
type ApprovedApplication struct {
	Application
	TemporaryPassword string `json:"temporary_password"`
}

// ApplyInput son los datos que aporta quien se postula.
type ApplyInput struct {
	FullName    string `json:"full_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	VehicleType string `json:"vehicle_type"`
}

// Límites de las columnas y reglas de entrada.
const (
	maxFullName    = 255
	minPhone       = 5
	maxPhone       = 50
	maxVehicleType = 50
)
