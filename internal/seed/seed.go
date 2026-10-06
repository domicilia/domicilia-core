// Package seed crea los datos de demo del entorno de desarrollo: una organización y
// un usuario por rol (superadmin, admin de organización, empleado). Es el
// reemplazo de scripts/seed.py de domicilia-api, que dejó de servir cuando los roles
// pasaron a tablas.
//
// Es idempotente: cada recurso que ya existe se salta. Nunca cambia la contraseña de
// una cuenta existente. NO es para staging ni producción (el comando lo rechaza): usa
// las contraseñas que se le den, y son de demostración.
package seed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/organizations"
	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/store"
	"github.com/domicilia/domicilia-core/internal/users"
)

// IdentityProvider es lo que necesita de auth-domicilia (GoTrue).
type IdentityProvider interface {
	CreateUser(ctx context.Context, email, password string) (gotrue.User, error)
	UserByEmail(ctx context.Context, email string) (*gotrue.User, error)
}

// Account es un usuario de demo.
type Account struct {
	Email    string
	Password string
}

// Options son los datos a sembrar.
type Options struct {
	OrgName  string
	Admin    Account // superadmin de la plataforma (y admin de la organización)
	OrgAdmin Account // admin de la organización
	Employee Account // empleado de la organización
}

// Validate comprueba que no falte nada.
func (o Options) Validate() error {
	var missing []string
	for name, v := range map[string]string{
		"ADMIN_ORG": o.OrgName, "ADMIN_EMAIL": o.Admin.Email, "ADMIN_PASSWORD": o.Admin.Password,
		"ORG_ADMIN_EMAIL": o.OrgAdmin.Email, "ORG_ADMIN_PASSWORD": o.OrgAdmin.Password,
		"EMPLOYEE_EMAIL": o.Employee.Email, "EMPLOYEE_PASSWORD": o.Employee.Password,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing) // el mapa no tiene orden: sin esto el mensaje cambia entre ejecuciones
	if len(missing) > 0 {
		return fmt.Errorf("faltan variables de entorno: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Run siembra los datos. Escribe un resumen legible en out.
func Run(ctx context.Context, pool *pgxpool.Pool, idp IdentityProvider, opts Options, out io.Writer) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	q := store.New(pool)
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }

	say("→ Organización")
	org, err := ensureOrganization(ctx, pool, opts.OrgName, say)
	if err != nil {
		return err
	}

	say("→ Superadmin")
	sa, err := ensureSuperadmin(ctx, pool, idp, opts.Admin, say)
	if err != nil {
		return err
	}
	if err := ensureMembership(ctx, q, sa, org, access.RoleAdmin, say); err != nil {
		return err
	}

	say("→ Admin de la organización")
	orgAdmin, err := ensureUser(ctx, q, idp, opts.OrgAdmin, say)
	if err != nil {
		return err
	}
	if err := ensureMembership(ctx, q, orgAdmin, org, access.RoleAdmin, say); err != nil {
		return err
	}

	say("→ Empleado")
	emp, err := ensureUser(ctx, q, idp, opts.Employee, say)
	if err != nil {
		return err
	}
	if err := ensureMembership(ctx, q, emp, org, access.RoleEmployee, say); err != nil {
		return err
	}

	say("✓ Listo.")
	return nil
}

// ensureOrganization crea la organización de demo con el mismo repositorio que usa la
// API, así queda completa (ajustes, suscripción y auditoría). Va en el plan más alto
// para poder probar todas las funciones en desarrollo; se baja desde el panel.
func ensureOrganization(ctx context.Context, pool *pgxpool.Pool, name string, say func(string, ...any)) (store.Organization, error) {
	q := store.New(pool)
	name = strings.TrimSpace(name)
	slug := organizations.Slugify(name)
	org, err := q.GetOrganizationBySlug(ctx, slug)
	if err == nil {
		say("  La organización %q ya existe: se salta.", name)
		return org, nil
	}
	if !db.IsNoRows(err) {
		return store.Organization{}, fmt.Errorf("seed: buscar organización: %w", err)
	}
	if _, _, err := organizations.NewRepository(pool).Create(ctx, organizations.NewOrganization{
		ID: uuid.New(), Name: name, Slug: slug, PlanTier: string(plans.Enterprise), Settings: organizations.DefaultSettings(),
	}); err != nil {
		return store.Organization{}, fmt.Errorf("seed: crear organización: %w", err)
	}
	if org, err = q.GetOrganizationBySlug(ctx, slug); err != nil {
		return store.Organization{}, fmt.Errorf("seed: leer la organización creada: %w", err)
	}
	say("  Organización %q creada (plan %s).", name, plans.Enterprise)
	return org, nil
}

// identityID devuelve el id de la cuenta en GoTrue, creándola si no existe.
func identityID(ctx context.Context, idp IdentityProvider, acc Account) (uuid.UUID, error) {
	created, err := idp.CreateUser(ctx, acc.Email, acc.Password)
	if err == nil {
		return created.ID, nil
	}
	if !gotrue.IsEmailExists(err) {
		return uuid.Nil, fmt.Errorf("seed: crear %s en auth-domicilia: %w", acc.Email, err)
	}
	found, ferr := idp.UserByEmail(ctx, acc.Email)
	if ferr != nil {
		return uuid.Nil, fmt.Errorf("seed: buscar %s en auth-domicilia: %w", acc.Email, ferr)
	}
	if found == nil {
		return uuid.Nil, fmt.Errorf("seed: %s existe en auth-domicilia pero no se pudo recuperar su id", acc.Email)
	}
	return found.ID, nil
}

func ensureUser(ctx context.Context, q *store.Queries, idp IdentityProvider, acc Account, say func(string, ...any)) (store.User, error) {
	email := strings.ToLower(strings.TrimSpace(acc.Email))
	if u, err := q.GetUserByEmail(ctx, email); err == nil {
		say("  Usuario %q ya existe: se salta.", email)
		return u, nil
	} else if !db.IsNoRows(err) {
		return store.User{}, fmt.Errorf("seed: buscar usuario: %w", err)
	}
	id, err := identityID(ctx, idp, Account{Email: email, Password: acc.Password})
	if err != nil {
		return store.User{}, err
	}
	u, err := q.InsertUser(ctx, store.InsertUserParams{ID: id, Email: email})
	if err != nil {
		return store.User{}, fmt.Errorf("seed: crear usuario %s: %w", email, err)
	}
	say("  Usuario %q creado (id=%s).", email, id)
	return u, nil
}

// ensureSuperadmin crea o promueve al superadmin. Usa el repositorio, no el
// servicio: el servicio exige contraseñas de 12+ caracteres para una cuenta de
// producción, y aquí las contraseñas de demo pueden ser más cortas.
func ensureSuperadmin(ctx context.Context, pool *pgxpool.Pool, idp IdentityProvider, acc Account, say func(string, ...any)) (store.User, error) {
	email := strings.ToLower(strings.TrimSpace(acc.Email))
	repo := users.NewSuperadminRepository(pool)
	q := store.New(pool)

	existing, err := repo.ByEmail(ctx, email)
	switch {
	case err == nil:
		if _, perr := repo.PromoteToSuperadmin(ctx, existing.ID, nil); perr != nil {
			return store.User{}, perr
		}
		say("  Usuario %q ya existe: se asegura el rol superadmin.", email)
	case errors.Is(err, users.ErrNotFound):
		id, ierr := identityID(ctx, idp, Account{Email: email, Password: acc.Password})
		if ierr != nil {
			return store.User{}, ierr
		}
		if _, cerr := repo.CreateSuperadmin(ctx, id, email, nil); cerr != nil {
			return store.User{}, cerr
		}
		say("  Superadmin %q creado (id=%s).", email, id)
	default:
		return store.User{}, err
	}
	u, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		return store.User{}, fmt.Errorf("seed: leer superadmin: %w", err)
	}
	return u, nil
}

func ensureMembership(ctx context.Context, q *store.Queries, u store.User, org store.Organization, role string, say func(string, ...any)) error {
	if _, err := q.GetMembership(ctx, store.GetMembershipParams{UserID: u.ID, OrganizationID: org.ID}); err == nil {
		say("  Membresía %q → %q ya existe: se salta.", u.Email, org.Name)
		return nil
	} else if !db.IsNoRows(err) {
		return fmt.Errorf("seed: buscar membresía: %w", err)
	}
	r, err := q.GetSystemRoleByCode(ctx, store.GetSystemRoleByCodeParams{Scope: string(access.ScopeOrganization), Code: role})
	if err != nil {
		return fmt.Errorf("seed: rol %s: %w", role, err)
	}
	if err := q.InsertMembership(ctx, store.InsertMembershipParams{UserID: u.ID, OrganizationID: org.ID, RoleID: r.ID}); err != nil {
		return fmt.Errorf("seed: crear membresía: %w", err)
	}
	say("  Membresía %q → %q como %s creada.", u.Email, org.Name, role)
	return nil
}
