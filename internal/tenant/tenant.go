// Package tenant es la puerta común de todo lo que se hace DENTRO de una organización: exige
// el permiso, que la organización exista y esté disponible (suspendida o archivada) y que la
// llave de la URL sea de un miembro. Cada dominio (bandejas, contactos, conversaciones...)
// pasa por aquí en lugar de repetir las reglas: un olvido en una copia sería un fallo de
// aislamiento entre inquilinos.
package tenant

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

// Org es lo mínimo de la organización que las reglas necesitan.
type Org struct {
	ID       uuid.UUID
	Status   string
	PlanTier string
}

// Loader lee una organización. La base de datos lo implementa; las pruebas usan otro.
type Loader interface {
	Organization(ctx context.Context, id uuid.UUID) (Org, error)
}

// ErrNotFound: la organización no existe.
var ErrNotFound = errors.New("tenant: organización no encontrada")

type pgLoader struct{ q *store.Queries }

// NewLoader crea el lector sobre Postgres.
func NewLoader(pool *pgxpool.Pool) Loader { return pgLoader{q: store.New(pool)} }

func (l pgLoader) Organization(ctx context.Context, id uuid.UUID) (Org, error) {
	o, err := l.q.GetOrganization(ctx, id)
	if db.IsNoRows(err) {
		return Org{}, ErrNotFound
	}
	if err != nil {
		return Org{}, err
	}
	return Org{ID: o.ID, Status: o.Status, PlanTier: o.PlanTier}, nil
}

// Gate aplica las reglas de acceso a una organización.
type Gate struct{ loader Loader }

// NewGate crea la puerta.
func NewGate(l Loader) *Gate { return &Gate{loader: l} }

// Open exige el permiso y devuelve la organización si está disponible.
//
//   - Sin el permiso: 403 ANTES de tocar la base, así quien no puede no averigua si la
//     organización existe.
//   - Archivada: para un miembro "no existe" (404); el operador la lee pero no la modifica (409).
//   - Suspendida: sus miembros no la abren ni la operan (403); el operador sí.
//
// write indica si la operación cambia datos.
func (g *Gate) Open(ctx context.Context, actor identity.Principal, orgID uuid.UUID, perm access.Permission, write bool) (Org, error) {
	if !actor.CanInOrg(orgID, perm) {
		return Org{}, apperr.Forbidden("no tienes permiso para esta acción en la organización")
	}
	org, err := g.loader.Organization(ctx, orgID)
	if errors.Is(err, ErrNotFound) {
		return Org{}, apperr.NotFound("organización no encontrada")
	}
	if err != nil {
		return Org{}, err
	}
	operator := actor.Can(access.PlatformOrganizationsAccessAll)
	switch org.Status {
	case "archived":
		if !operator {
			return Org{}, apperr.NotFound("organización no encontrada")
		}
		if write {
			return Org{}, apperr.Conflict("la organización está archivada: restáurala primero")
		}
	case "suspended":
		if !operator {
			return Org{}, apperr.Forbidden("la organización está suspendida")
		}
	}
	return org, nil
}

// OpenForCustomer comprueba que la organización exista y esté activa, sin exigir ningún permiso:
// un cliente pidiendo no es miembro del negocio, así que Open (que exige actor.CanInOrg) nunca le
// serviría. Suspendida o archivada responde igual que "no existe" — mismo criterio que la cara
// pública de una organización (organizations.Service.Public): no se revela el estado interno a
// quien va a pedir.
func (g *Gate) OpenForCustomer(ctx context.Context, orgID uuid.UUID) (Org, error) {
	org, err := g.loader.Organization(ctx, orgID)
	if errors.Is(err, ErrNotFound) {
		return Org{}, apperr.NotFound("organización no encontrada")
	}
	if err != nil {
		return Org{}, err
	}
	if org.Status != "active" {
		return Org{}, apperr.NotFound("organización no encontrada")
	}
	return org, nil
}
