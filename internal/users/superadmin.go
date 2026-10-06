package users

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
	"github.com/domicilia/domicilia-core/internal/store"
)

// MinSuperadminPasswordLength es la longitud mínima de la contraseña de un
// superadmin: es la cuenta con más poder de la plataforma.
const MinSuperadminPasswordLength = 12

// SuperadminRepository es lo que el alta de superadmin necesita de la base.
type SuperadminRepository interface {
	// ByEmail devuelve el usuario o ErrNotFound.
	ByEmail(ctx context.Context, email string) (User, error)
	// CreateSuperadmin crea el usuario de negocio ya como superadmin activo.
	CreateSuperadmin(ctx context.Context, id uuid.UUID, email string, fullName *string) (User, error)
	// PromoteToSuperadmin deja a un usuario existente como superadmin activo.
	PromoteToSuperadmin(ctx context.Context, id uuid.UUID, fullName *string) (User, error)
	// Retire quita el rol de superadmin y desactiva la cuenta. false si el correo no existe.
	Retire(ctx context.Context, email string) (bool, error)
}

// SuperadminIdentityProvider es lo que necesita de auth-domicilia (GoTrue).
type SuperadminIdentityProvider interface {
	CreateUser(ctx context.Context, email, password string) (gotrue.User, error)
	UserByEmail(ctx context.Context, email string) (*gotrue.User, error)
}

// SuperadminService crea, promueve y retira superadmins. Es el bootstrap de la
// plataforma: sin un primer superadmin nadie puede crear organizaciones. Se usa
// desde `api create-superadmin`, no desde HTTP.
type SuperadminService struct {
	repo SuperadminRepository
	idp  SuperadminIdentityProvider
}

// NewSuperadminService crea el servicio.
func NewSuperadminService(repo SuperadminRepository, idp SuperadminIdentityProvider) *SuperadminService {
	return &SuperadminService{repo: repo, idp: idp}
}

// Exists dice si el correo ya tiene perfil de negocio (y por tanto no hace falta
// contraseña para darle el rol).
func (s *SuperadminService) Exists(ctx context.Context, email string) (bool, error) {
	email, err := validate.Email("email", email)
	if err != nil {
		return false, err
	}
	if _, err := s.repo.ByEmail(ctx, email); errors.Is(err, ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

// Ensure deja a email como superadmin activo. Es idempotente:
//   - si ya tiene perfil, solo se le da el rol (y se reactiva); NUNCA se cambia
//     su contraseña, para eso está la recuperación de GoTrue;
//   - si no tiene perfil pero ya tiene identidad en GoTrue (p. ej. se registró
//     como cliente y no llegó a tener fila de negocio), se reutiliza esa
//     identidad sin tocar su contraseña;
//   - si no existe en ningún lado, se crea la cuenta con password.
//
// password solo hace falta en el último caso.
func (s *SuperadminService) Ensure(ctx context.Context, email, password string, fullName *string) (User, error) {
	email, err := validate.Email("email", email)
	if err != nil {
		return User{}, err
	}
	if err := validate.OptionalMaxLen("full_name", fullName, maxFullName); err != nil {
		return User{}, err
	}

	existing, err := s.repo.ByEmail(ctx, email)
	switch {
	case err == nil:
		return s.repo.PromoteToSuperadmin(ctx, existing.ID, fullName)
	case !errors.Is(err, ErrNotFound):
		return User{}, err
	}

	if len(password) < MinSuperadminPasswordLength {
		return User{}, apperr.Invalid(fmt.Sprintf(
			"%s no existe todavía: hace falta una contraseña de al menos %d caracteres",
			email, MinSuperadminPasswordLength))
	}
	id, err := s.identityID(ctx, email, password)
	if err != nil {
		return User{}, err
	}
	return s.repo.CreateSuperadmin(ctx, id, email, fullName)
}

// identityID crea la cuenta en GoTrue, o reutiliza la que ya tenga ese correo.
func (s *SuperadminService) identityID(ctx context.Context, email, password string) (uuid.UUID, error) {
	created, err := s.idp.CreateUser(ctx, email, password)
	if err == nil {
		return created.ID, nil
	}
	if !gotrue.IsEmailExists(err) {
		return uuid.Nil, fmt.Errorf("crear la cuenta en auth-domicilia: %w", err)
	}
	found, ferr := s.idp.UserByEmail(ctx, email)
	if ferr != nil {
		return uuid.Nil, fmt.Errorf("buscar la cuenta existente en auth-domicilia: %w", ferr)
	}
	if found == nil {
		return uuid.Nil, fmt.Errorf("%s existe en auth-domicilia pero no se pudo recuperar su id", email)
	}
	return found.ID, nil
}

// Retire quita el rol de superadmin y desactiva la cuenta (la API responde 403 a
// partir de ese momento). Devuelve false si el correo no existe. No borra nada.
func (s *SuperadminService) Retire(ctx context.Context, email string) (bool, error) {
	// Sin validar el formato: puede ser una cuenta semilla heredada con un correo
	// que hoy no pasaría la validación, y precisamente esas son las que se retiran.
	return s.repo.Retire(ctx, strings.ToLower(strings.TrimSpace(email)))
}

// NewSuperadminRepository crea el repositorio del alta de superadmin.
func NewSuperadminRepository(pool *pgxpool.Pool) SuperadminRepository {
	return &superadminRepository{pool: pool, q: store.New(pool)}
}

type superadminRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

func (r *superadminRepository) ByEmail(ctx context.Context, email string) (User, error) {
	u, err := r.q.GetUserByEmail(ctx, email)
	if db.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("users: buscar por correo: %w", err)
	}
	return withRoles(ctx, r.q, u)
}

// viaCLI marca en la auditoría que el cambio lo hizo el operador desde la línea de
// comandos, no un usuario de la API (por eso no hay actor).
var viaCLI = map[string]any{"via": "create-superadmin"}

// grantSuperadmin da el rol y lo audita.
func grantSuperadmin(ctx context.Context, q *store.Queries, userID uuid.UUID) error {
	n, err := q.InsertUserPlatformRoleByCode(ctx, store.InsertUserPlatformRoleByCodeParams{
		UserID: userID, Code: access.RoleSuperadmin,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	return audit.Entry{
		Action: audit.PlatformRoleGranted, TargetUserID: userID, RoleCode: access.RoleSuperadmin, Detail: viaCLI,
	}.Insert(ctx, q)
}

func (r *superadminRepository) CreateSuperadmin(ctx context.Context, id uuid.UUID, email string, fullName *string) (User, error) {
	var out store.User
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		u, err := q.InsertUser(ctx, store.InsertUserParams{ID: id, Email: email, FullName: fullName})
		if err != nil {
			return err
		}
		out = u
		return grantSuperadmin(ctx, q, id)
	})
	if db.IsUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("users: crear superadmin: %w", err)
	}
	return withRoles(ctx, r.q, out)
}

func (r *superadminRepository) PromoteToSuperadmin(ctx context.Context, id uuid.UUID, fullName *string) (User, error) {
	var out store.User
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		u, err := q.GetUser(ctx, id)
		if err != nil {
			return err
		}
		// El nombre solo se rellena si no tenía uno.
		if u.FullName == nil && fullName != nil {
			if u, err = q.UpdateUserFullName(ctx, store.UpdateUserFullNameParams{ID: id, FullName: fullName}); err != nil {
				return err
			}
		}
		if !u.IsActive {
			if u, err = q.SetUserActive(ctx, store.SetUserActiveParams{ID: id, IsActive: true}); err != nil {
				return err
			}
			if err := (audit.Entry{Action: audit.UserActivated, TargetUserID: id, Detail: viaCLI}).Insert(ctx, q); err != nil {
				return err
			}
		}
		out = u
		return grantSuperadmin(ctx, q, id)
	})
	if db.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("users: promover a superadmin: %w", err)
	}
	return withRoles(ctx, r.q, out)
}

func (r *superadminRepository) Retire(ctx context.Context, email string) (bool, error) {
	found := false
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		u, err := q.GetUserByEmail(ctx, email)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if err := q.LockSuperadmins(ctx); err != nil {
			return err
		}
		role, err := q.GetSystemRoleByCode(ctx, store.GetSystemRoleByCodeParams{
			Scope: string(access.ScopePlatform), Code: access.RoleSuperadmin,
		})
		if err != nil {
			return err
		}
		n, err := q.DeleteUserPlatformRole(ctx, store.DeleteUserPlatformRoleParams{UserID: u.ID, RoleID: role.ID})
		if err != nil {
			return err
		}
		if n > 0 {
			if err := (audit.Entry{
				Action: audit.PlatformRoleRevoked, TargetUserID: u.ID, RoleID: role.ID, RoleCode: role.Code, Detail: viaCLI,
			}).Insert(ctx, q); err != nil {
				return err
			}
		}
		if _, err := q.SetUserActive(ctx, store.SetUserActiveParams{ID: u.ID, IsActive: false}); err != nil {
			return err
		}
		return audit.Entry{Action: audit.UserDeactivated, TargetUserID: u.ID, Detail: viaCLI}.Insert(ctx, q)
	})
	if err != nil {
		return false, fmt.Errorf("users: retirar: %w", err)
	}
	return found, nil
}
