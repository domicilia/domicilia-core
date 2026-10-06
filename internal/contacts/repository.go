package contacts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

type pgRepository struct{ q *store.Queries }

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository { return &pgRepository{q: store.New(pool)} }

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func toContact(c store.Contact) (Contact, error) {
	attrs := map[string]any{}
	if len(c.CustomAttributes) > 0 {
		if err := json.Unmarshal(c.CustomAttributes, &attrs); err != nil {
			return Contact{}, fmt.Errorf("contacts: atributos de %s: %w", c.ID, err)
		}
	}
	return Contact{
		ID: c.ID, OrganizationID: c.OrganizationID, Phone: c.PhoneE164, Name: c.Name, Email: c.Email,
		CustomAttributes: attrs, Blocked: c.Blocked, Source: c.Source, LastActivityAt: timePtr(c.LastActivityAt),
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}, nil
}

func (r *pgRepository) Insert(ctx context.Context, n NewContact) (Contact, error) {
	attrs := n.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return Contact{}, fmt.Errorf("contacts: codificar atributos: %w", err)
	}
	c, err := r.q.InsertContact(ctx, store.InsertContactParams{
		OrganizationID: n.OrganizationID, PhoneE164: n.Phone, Name: n.Name, Email: n.Email, CustomAttributes: raw, Source: SourceManual,
	})
	if db.IsUniqueViolation(err) {
		return Contact{}, ErrDuplicate
	}
	if err != nil {
		return Contact{}, fmt.Errorf("contacts: crear: %w", err)
	}
	return toContact(c)
}

func (r *pgRepository) Get(ctx context.Context, orgID, id uuid.UUID) (Contact, error) {
	c, err := r.q.GetContact(ctx, store.GetContactParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Contact{}, ErrNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("contacts: leer: %w", err)
	}
	return toContact(c)
}

func (r *pgRepository) List(ctx context.Context, orgID uuid.UUID, search string, before *Cursor, limit int) ([]Contact, error) {
	p := store.ListContactsParams{OrganizationID: orgID, PageSize: int32(limit + 1)} //nolint:gosec // acotado por el paginador
	if search != "" {
		s := db.EscapeLike(search)
		p.Search = &s
	}
	if before != nil {
		p.BeforeAt = pgtype.Timestamptz{Time: before.At, Valid: true}
		p.BeforeID = uuid.NullUUID{UUID: before.ID, Valid: true}
	}
	rows, err := r.q.ListContacts(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("contacts: listar: %w", err)
	}
	out := make([]Contact, 0, len(rows))
	for _, row := range rows {
		c, err := toContact(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *pgRepository) Update(ctx context.Context, orgID, id uuid.UUID, p Patch) (Contact, error) {
	params := store.UpdateContactParams{
		ID: id, OrganizationID: orgID, SetName: p.SetName, Name: p.Name, SetEmail: p.SetEmail, Email: p.Email, Blocked: p.Blocked,
	}
	if p.Attributes != nil {
		raw, err := json.Marshal(p.Attributes)
		if err != nil {
			return Contact{}, fmt.Errorf("contacts: codificar atributos: %w", err)
		}
		params.CustomAttributes = raw
	}
	c, err := r.q.UpdateContact(ctx, params)
	if db.IsNoRows(err) {
		return Contact{}, ErrNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("contacts: actualizar: %w", err)
	}
	return toContact(c)
}
