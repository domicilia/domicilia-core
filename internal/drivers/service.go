package drivers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
)

// compensateTimeout acota el intento de deshacer un alta en GoTrue.
const compensateTimeout = 5 * time.Second

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	Create(ctx context.Context, in ApplyInput) (Application, error)
	// List filtra por estado si status no es nil; ordena por antigüedad.
	List(ctx context.Context, status *Status) ([]Application, error)
	Get(ctx context.Context, id uuid.UUID) (Application, error)
	EmailRegistered(ctx context.Context, email string) (bool, error)
	// Reject marca como rechazada una solicitud pendiente. Devuelve ErrNotFound o
	// ErrNotPending.
	Reject(ctx context.Context, id uuid.UUID) (Application, error)
	// Approve marca como aprobada una solicitud pendiente y crea el usuario de
	// negocio con el rol delivery y el id de la cuenta de GoTrue, todo en una
	// transacción, y lo audita. Devuelve ErrNotFound, ErrNotPending o ErrEmailTaken.
	Approve(ctx context.Context, id, userID, actor uuid.UUID) (Application, error)
}

// IdentityProvider crea y borra cuentas en auth-domicilia (GoTrue).
type IdentityProvider interface {
	CreateUser(ctx context.Context, email, password string) (gotrue.User, error)
	DeleteUser(ctx context.Context, id uuid.UUID) error
}

// Service reúne las reglas de negocio de las postulaciones.
type Service struct {
	repo Repository
	idp  IdentityProvider
	log  *slog.Logger
}

// NewService crea el servicio.
func NewService(repo Repository, idp IdentityProvider, log *slog.Logger) *Service {
	return &Service{repo: repo, idp: idp, log: log}
}

// Apply registra una postulación pendiente. Es pública y no crea ninguna cuenta.
func (s *Service) Apply(ctx context.Context, in ApplyInput) (Application, error) {
	var err error
	if in.FullName, err = validate.Required("full_name", in.FullName, maxFullName); err != nil {
		return Application{}, err
	}
	if in.Email, err = validate.Email("email", in.Email); err != nil {
		return Application{}, err
	}
	if in.Phone, err = validate.RequiredMin("phone", in.Phone, minPhone, maxPhone); err != nil {
		return Application{}, err
	}
	if in.VehicleType, err = validate.Required("vehicle_type", in.VehicleType, maxVehicleType); err != nil {
		return Application{}, err
	}
	return s.repo.Create(ctx, in)
}

// List devuelve las postulaciones, opcionalmente filtradas por estado. Quien
// llama debe ser superadmin (lo exige la ruta).
func (s *Service) List(ctx context.Context, statusFilter string) ([]Application, error) {
	if statusFilter == "" {
		return s.repo.List(ctx, nil)
	}
	st, err := ParseStatus(statusFilter)
	if err != nil {
		return nil, err
	}
	return s.repo.List(ctx, &st)
}

// Approve crea la cuenta del domiciliario en GoTrue y su perfil con el rol
// delivery, y marca la solicitud como aprobada. Quien llama debe tener
// PlatformDriversReview (lo exige la ruta).
//
// Si guardar en la base falla después de crear la cuenta (o si otra petición
// aprobó la misma solicitud al mismo tiempo), la cuenta se borra de GoTrue: sin
// eso el correo quedaría ocupado allí y no se podría reintentar.
func (s *Service) Approve(ctx context.Context, actor identity.Principal, id uuid.UUID) (ApprovedApplication, error) {
	app, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ApprovedApplication{}, apperr.NotFound("solicitud no encontrada")
	}
	if err != nil {
		return ApprovedApplication{}, err
	}
	if app.Status != StatusPending {
		return ApprovedApplication{}, apperr.Conflict("la solicitud ya fue revisada")
	}
	if taken, err := s.repo.EmailRegistered(ctx, app.Email); err != nil {
		return ApprovedApplication{}, err
	} else if taken {
		return ApprovedApplication{}, apperr.Conflict("el correo ya está registrado")
	}

	password, err := gotrue.NewTemporaryPassword()
	if err != nil {
		return ApprovedApplication{}, err
	}
	created, err := s.idp.CreateUser(ctx, app.Email, password)
	if err != nil {
		return ApprovedApplication{}, apperr.Upstream("no se pudo crear la cuenta en auth-domicilia", err)
	}

	approved, err := s.repo.Approve(ctx, id, created.ID, actor.ID)
	if err != nil {
		s.undoIdentity(ctx, created.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			return ApprovedApplication{}, apperr.NotFound("solicitud no encontrada")
		case errors.Is(err, ErrNotPending):
			return ApprovedApplication{}, apperr.Conflict("la solicitud ya fue revisada")
		case errors.Is(err, ErrEmailTaken):
			return ApprovedApplication{}, apperr.Conflict("el correo ya está registrado")
		}
		return ApprovedApplication{}, err
	}
	return ApprovedApplication{Application: approved, TemporaryPassword: password}, nil
}

// Reject rechaza una postulación pendiente. Quien llama debe ser superadmin (lo
// exige la ruta).
func (s *Service) Reject(ctx context.Context, id uuid.UUID) (Application, error) {
	app, err := s.repo.Reject(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return Application{}, apperr.NotFound("solicitud no encontrada")
	case errors.Is(err, ErrNotPending):
		return Application{}, apperr.Conflict("la solicitud ya fue revisada")
	}
	return app, err
}

// undoIdentity borra una cuenta recién creada en GoTrue. Usa un contexto propio:
// si la petición se canceló, la limpieza debe correr igual.
func (s *Service) undoIdentity(ctx context.Context, id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensateTimeout)
	defer cancel()
	if err := s.idp.DeleteUser(ctx, id); err != nil {
		s.log.Error("no se pudo deshacer el alta en auth-domicilia; queda una cuenta huérfana",
			"user_id", id, "error", err)
	}
}
