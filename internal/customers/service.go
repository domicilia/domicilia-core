package customers

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	// SignUp crea el usuario, su perfil de cliente y le da el rol `customer`, todo
	// en una transacción. Devuelve ErrProfileExists o ErrEmailTaken si choca.
	SignUp(ctx context.Context, id uuid.UUID, email string, in SignUpInput) (Registration, error)
	Get(ctx context.Context, userID uuid.UUID) (Profile, error)
	Update(ctx context.Context, userID uuid.UUID, in ProfileInput) (Profile, error)
}

// Service reúne las reglas de negocio de los clientes.
type Service struct{ repo Repository }

// NewService crea el servicio.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// SignUp da de alta a un cliente: es el signup de clientes (el staff entra por
// invitación y los domiciliarios por aprobación; ninguno pasa por aquí).
//
// id y email salen del token de GoTrue, nunca del cuerpo: así nadie puede crearse
// un perfil con el id de otro.
func (s *Service) SignUp(ctx context.Context, id uuid.UUID, email string, in SignUpInput) (Registration, error) {
	if email == "" {
		return Registration{}, apperr.Invalid("el token no incluye un correo")
	}
	if err := validate.OptionalMaxLen("full_name", in.FullName, maxFullName); err != nil {
		return Registration{}, err
	}
	if err := ValidateProfile(in.Phone, in.DefaultAddress); err != nil {
		return Registration{}, err
	}
	reg, err := s.repo.SignUp(ctx, id, email, in)
	switch {
	case errors.Is(err, ErrProfileExists):
		return Registration{}, apperr.Conflict("el perfil ya existe")
	case errors.Is(err, ErrEmailTaken):
		return Registration{}, apperr.Conflict("el correo ya está registrado")
	}
	return reg, err
}

// Profile devuelve el perfil de cliente, o nil si el usuario no es cliente (staff
// y domiciliarios no tienen uno).
func (s *Service) Profile(ctx context.Context, userID uuid.UUID) (*Profile, error) {
	p, err := s.repo.Get(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil //nolint:nilnil // no ser cliente no es un error
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateProfile edita el perfil de cliente. Devuelve nil, nil si el usuario no
// tiene uno: editar el propio perfil siendo staff no falla ni crea un perfil.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, in ProfileInput) (*Profile, error) {
	if err := ValidateProfile(in.Phone, in.DefaultAddress); err != nil {
		return nil, err
	}
	p, err := s.repo.Update(ctx, userID, in)
	if errors.Is(err, ErrNotFound) {
		return nil, nil //nolint:nilnil // no ser cliente no es un error
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ValidateProfile comprueba los límites del perfil. Se exporta para que quien
// edita el perfil junto con otros datos (users) valide todo antes de escribir.
func ValidateProfile(phone, address *string) error {
	if err := validate.OptionalMaxLen("phone", phone, maxPhone); err != nil {
		return err
	}
	return validate.OptionalMaxLen("default_address", address, maxAddress)
}
