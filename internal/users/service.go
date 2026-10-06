package users

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/customers"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
	"github.com/domicilia/domicilia-core/internal/roles"
)

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	Get(ctx context.Context, id uuid.UUID) (User, error)
	Memberships(ctx context.Context, id uuid.UUID) ([]MembershipBrief, error)
	UpdateName(ctx context.Context, id uuid.UUID, fullName *string) (User, error)
	List(ctx context.Context, f ListFilter) ([]User, int64, error)
	// SetActive activa o desactiva la cuenta y lo audita. Devuelve ErrNotFound, o
	// ErrLastSuperadmin si desactivarla dejaría la plataforma sin superadmin activo.
	SetActive(ctx context.Context, id uuid.UUID, active bool, actor uuid.UUID) (User, error)
	Counts(ctx context.Context) (total, delivery int64, err error)
}

// Customers es lo que necesita del dominio de clientes.
type Customers interface {
	SignUp(ctx context.Context, id uuid.UUID, email string, in customers.SignUpInput) (customers.Registration, error)
	Profile(ctx context.Context, userID uuid.UUID) (*customers.Profile, error)
	UpdateProfile(ctx context.Context, userID uuid.UUID, in customers.ProfileInput) (*customers.Profile, error)
}

// Roles es lo que necesita de la plataforma de roles.
type Roles interface {
	GrantPlatformRole(ctx context.Context, actor identity.Principal, userID uuid.UUID, ref string) (roles.PlatformRoles, error)
	RevokePlatformRole(ctx context.Context, actor identity.Principal, userID uuid.UUID, ref string) (roles.PlatformRoles, error)
}

// Service reúne las reglas de negocio de los usuarios.
type Service struct {
	repo      Repository
	customers Customers
	roles     Roles
}

// NewService crea el servicio.
func NewService(repo Repository, c Customers, r Roles) *Service {
	return &Service{repo: repo, customers: c, roles: r}
}

// SignUp crea el perfil de negocio de una identidad que ya se registró en GoTrue.
// Es el signup de clientes: crea también su perfil de cliente y le da el rol
// customer. id y email salen del token, nunca del cuerpo.
func (s *Service) SignUp(ctx context.Context, id uuid.UUID, email string, in ProfileInput) (User, error) {
	reg, err := s.customers.SignUp(ctx, id, email, customers.SignUpInput{
		FullName: in.FullName, Phone: in.Phone, DefaultAddress: in.DefaultAddress,
	})
	if err != nil {
		return User{}, err
	}
	roleRefs, err := s.userRoles(ctx, reg.UserID)
	if err != nil {
		return User{}, err
	}
	return NewUser(reg.UserID, reg.Email, reg.FullName, true, roleRefs), nil
}

func (s *Service) userRoles(ctx context.Context, id uuid.UUID) ([]access.RoleRef, error) {
	u, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return u.Roles, nil
}

// Me arma la sesión del usuario a partir de su acceso ya cargado (identity) y de lo
// que solo la base sabe: los nombres de sus organizaciones y su perfil de cliente.
func (s *Service) Me(ctx context.Context, p identity.Principal) (Me, error) {
	ms, err := s.repo.Memberships(ctx, p.ID)
	if err != nil {
		return Me{}, err
	}
	// Una organización archivada "no existe" para sus miembros: no entra en la sesión.
	// (El operador la ve en el detalle de usuario de la plataforma, con su estado.)
	kept := ms[:0]
	for _, m := range ms {
		if m.OrganizationStatus == "archived" {
			continue
		}
		m.Permissions = p.Memberships[m.OrganizationID].Permissions.Sorted()
		kept = append(kept, m)
	}
	ms = kept
	prof, err := s.customers.Profile(ctx, p.ID)
	if err != nil {
		return Me{}, err
	}
	if ms == nil {
		ms = []MembershipBrief{}
	}
	return Me{
		User:            NewUser(p.ID, p.Email, p.FullName, p.IsActive, p.PlatformRoles),
		Permissions:     p.Permissions.Sorted(),
		Memberships:     ms,
		CustomerProfile: prof,
	}, nil
}

// UpdateMe edita el perfil propio. Un campo ausente o null no se toca. El teléfono
// y la dirección solo aplican si el usuario es cliente; para el resto se ignoran.
func (s *Service) UpdateMe(ctx context.Context, p identity.Principal, in ProfileInput) (User, error) {
	if err := validate.OptionalMaxLen("full_name", in.FullName, maxFullName); err != nil {
		return User{}, err
	}
	// Se valida todo antes de escribir nada: el nombre y el perfil de cliente son
	// dos escrituras, y una entrada inválida no debe dejar la primera hecha.
	if err := customers.ValidateProfile(in.Phone, in.DefaultAddress); err != nil {
		return User{}, err
	}
	u, err := s.repo.UpdateName(ctx, p.ID, in.FullName)
	if errors.Is(err, ErrNotFound) {
		return User{}, apperr.Unauthorized("credenciales requeridas o inválidas")
	}
	if err != nil {
		return User{}, err
	}
	if in.Phone != nil || in.DefaultAddress != nil {
		if _, err := s.customers.UpdateProfile(ctx, p.ID, customers.ProfileInput{Phone: in.Phone, DefaultAddress: in.DefaultAddress}); err != nil {
			return User{}, err
		}
	}
	return u, nil
}

// PromoteToGeneralAdmin da el rol de superadmin. Quien llama debe poder asignarlo
// (lo exige la ruta y roles lo comprueba).
func (s *Service) PromoteToGeneralAdmin(ctx context.Context, actor identity.Principal, id uuid.UUID) (User, error) {
	if _, err := s.roles.GrantPlatformRole(ctx, actor, id, access.RoleSuperadmin); err != nil {
		return User{}, err
	}
	return s.Get(ctx, id)
}

// SetDelivery marca o desmarca a un usuario como domiciliario independiente (da o
// quita el rol delivery).
func (s *Service) SetDelivery(ctx context.Context, actor identity.Principal, id uuid.UUID, isDelivery bool) (User, error) {
	var err error
	if isDelivery {
		_, err = s.roles.GrantPlatformRole(ctx, actor, id, access.RoleDelivery)
	} else {
		_, err = s.roles.RevokePlatformRole(ctx, actor, id, access.RoleDelivery)
	}
	if err != nil {
		return User{}, err
	}
	return s.Get(ctx, id)
}

// Get devuelve un usuario con sus roles de plataforma.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (User, error) {
	u, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return User{}, apperr.NotFound("usuario no encontrado")
	}
	return u, err
}

// Detail devuelve un usuario con sus organizaciones y su perfil de cliente. Quien
// llama debe tener PlatformUsersRead (lo exige la ruta).
func (s *Service) Detail(ctx context.Context, id uuid.UUID) (Detail, error) {
	u, err := s.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	ms, err := s.repo.Memberships(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if ms == nil {
		ms = []MembershipBrief{}
	}
	prof, err := s.customers.Profile(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	return Detail{User: u, Memberships: ms, CustomerProfile: prof}, nil
}

// List devuelve una página de usuarios y el total. Quien llama debe tener
// PlatformUsersRead (lo exige la ruta).
func (s *Service) List(ctx context.Context, f ListFilter) ([]User, int64, error) {
	f.Search = strings.TrimSpace(f.Search)
	f.Role = strings.TrimSpace(f.Role)
	return s.repo.List(ctx, f)
}

// SetActive activa o desactiva una cuenta. Quien llama debe tener
// PlatformUsersManage (lo exige la ruta). Una cuenta desactivada recibe 403 en cada
// petición. No se puede desactivar la propia ni la del último superadmin activo.
func (s *Service) SetActive(ctx context.Context, actor identity.Principal, id uuid.UUID, active bool) (User, error) {
	if actor.ID == id && !active {
		return User{}, apperr.Conflict("no puedes desactivar tu propia cuenta")
	}
	u, err := s.repo.SetActive(ctx, id, active, actor.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, apperr.NotFound("usuario no encontrado")
	case errors.Is(err, ErrLastSuperadmin):
		return User{}, apperr.Conflict("es el último superadmin activo: la plataforma no puede quedarse sin uno")
	}
	return u, err
}

// Counts devuelve el total de usuarios y cuántos son domiciliarios.
func (s *Service) Counts(ctx context.Context) (total, delivery int64, err error) {
	return s.repo.Counts(ctx)
}
