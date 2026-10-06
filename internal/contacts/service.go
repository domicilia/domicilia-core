package contacts

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// Service reúne las reglas de los contactos.
type Service struct {
	repo Repository
	gate *tenant.Gate
}

// NewService crea el servicio.
func NewService(repo Repository, gate *tenant.Gate) *Service { return &Service{repo: repo, gate: gate} }

// CreateInput son los datos para crear un contacto.
type CreateInput struct {
	Phone            string
	Name             *string
	Email            *string
	CustomAttributes map[string]any
}

func optionalName(in *string) (*string, error) {
	if in == nil {
		return nil, nil //nolint:nilnil // ausente es un valor válido
	}
	v := strings.TrimSpace(*in)
	if v == "" {
		return nil, nil //nolint:nilnil // vacío deja el campo en blanco
	}
	if err := validate.MaxLen("name", v, maxName); err != nil {
		return nil, err
	}
	return &v, nil
}

func optionalEmail(in *string) (*string, error) {
	if in == nil || strings.TrimSpace(*in) == "" {
		return nil, nil //nolint:nilnil // ausente o vacío: sin correo
	}
	e, err := validate.Email("email", *in)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Create crea un contacto a mano. El teléfono es único por organización.
func (s *Service) Create(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in CreateInput) (Contact, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgContactsManage, true); err != nil {
		return Contact{}, err
	}
	phone, err := NormalizePhone(in.Phone)
	if err != nil {
		return Contact{}, err
	}
	name, err := optionalName(in.Name)
	if err != nil {
		return Contact{}, err
	}
	email, err := optionalEmail(in.Email)
	if err != nil {
		return Contact{}, err
	}
	if err := validateAttributes(in.CustomAttributes); err != nil {
		return Contact{}, err
	}
	c, err := s.repo.Insert(ctx, NewContact{OrganizationID: orgID, Phone: phone, Name: name, Email: email, Attributes: in.CustomAttributes})
	if errors.Is(err, ErrDuplicate) {
		return Contact{}, apperr.Conflict("ya existe un contacto con ese teléfono")
	}
	return c, err
}

// Get devuelve un contacto de la organización.
func (s *Service) Get(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Contact, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgContactsRead, false); err != nil {
		return Contact{}, err
	}
	c, err := s.repo.Get(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return Contact{}, apperr.NotFound("contacto no encontrado")
	}
	return c, err
}

// ListInput son los filtros del listado.
type ListInput struct {
	Search string
	Limit  int
	Before *Cursor
}

// List devuelve una página de contactos (los más nuevos primero), con búsqueda por nombre, teléfono o
// correo. Devuelve UNA fila más que Limit si hay otra página: el llamador arma el cursor.
func (s *Service) List(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in ListInput) ([]Contact, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgContactsRead, false); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, orgID, strings.TrimSpace(in.Search), in.Before, in.Limit)
}

// UpdateInput es un cambio parcial: nil no toca el campo; en name y email, una cadena vacía lo deja
// en blanco.
type UpdateInput struct {
	Name             *string
	Email            *string
	CustomAttributes map[string]any
	Blocked          *bool
}

// Update cambia un contacto.
func (s *Service) Update(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, in UpdateInput) (Contact, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgContactsManage, true); err != nil {
		return Contact{}, err
	}
	p := Patch{Attributes: in.CustomAttributes, Blocked: in.Blocked}
	var err error
	if in.Name != nil {
		p.SetName = true
		if p.Name, err = optionalName(in.Name); err != nil {
			return Contact{}, err
		}
	}
	if in.Email != nil {
		p.SetEmail = true
		if p.Email, err = optionalEmail(in.Email); err != nil {
			return Contact{}, err
		}
	}
	if in.CustomAttributes != nil {
		if err := validateAttributes(in.CustomAttributes); err != nil {
			return Contact{}, err
		}
	}
	if !p.SetName && !p.SetEmail && p.Attributes == nil && p.Blocked == nil {
		return Contact{}, apperr.Invalid("envía name, email, custom_attributes o blocked")
	}
	c, err := s.repo.Update(ctx, orgID, id, p)
	if errors.Is(err, ErrNotFound) {
		return Contact{}, apperr.NotFound("contacto no encontrado")
	}
	return c, err
}
