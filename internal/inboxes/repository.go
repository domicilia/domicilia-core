package inboxes

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/audit"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

// NewConnection son los datos para conectar un número. Los ids los genera el servicio porque
// el id de la cuenta forma parte de lo que ata el cifrado del token a su fila.
type NewConnection struct {
	OrganizationID uuid.UUID
	InboxID        uuid.UUID
	AccountID      uuid.UUID
	Name           string
	WABAID         string
	PhoneNumberID  string
	DisplayPhone   string
	VerifiedName   string
	QualityRating  string
	MessagingTier  string
	TokenEnc       string
	AppSecretEnc   string // vacío: sin secreto propio (usa el de la plataforma)
	Actor          uuid.UUID
}

// StoredCredentials es lo guardado, todavía cifrado.
type StoredCredentials struct {
	InboxID        uuid.UUID
	OrganizationID uuid.UUID
	AccountID      uuid.UUID
	PhoneNumberID  string
	WABAID         string
	TokenEnc       string
	AppSecretEnc   *string
	ChannelStatus  string
	TokenStatus    string
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	List(ctx context.Context, orgID uuid.UUID, includeArchived bool) ([]Inbox, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Inbox, error)
	CountActive(ctx context.Context, orgID uuid.UUID) (int64, error)
	// Connect crea cuenta, bandeja y canal en una transacción y audita. Devuelve ErrNameTaken,
	// ErrPhoneTaken o ErrWABATaken.
	Connect(ctx context.Context, n NewConnection) error
	Rename(ctx context.Context, orgID, id uuid.UUID, name string, actor uuid.UUID) error
	// Archive desconecta: archiva la bandeja, libera el número y borra la cuenta si queda huérfana.
	Archive(ctx context.Context, orgID, id, actor uuid.UUID) error
	Stored(ctx context.Context, orgID, inboxID uuid.UUID) (StoredCredentials, error)
	RotateToken(ctx context.Context, orgID, inboxID, accountID uuid.UUID, tokenEnc string, actor uuid.UUID) error
	RefreshInfo(ctx context.Context, orgID, inboxID uuid.UUID, display, verifiedName, quality, tier string) error
	MarkNeedsReauth(ctx context.Context, orgID, inboxID, accountID uuid.UUID) error
}

type pgRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool, q: store.New(pool)}
}

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// row reúne lo común de ListInboxesRow y GetInboxRow (sqlc genera un tipo por consulta).
type row struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	Name           string
	ChannelType    string
	ArchivedAt     pgtype.Timestamptz
	CreatedAt      time.Time
	UpdatedAt      time.Time
	PhoneNumberID  *string
	DisplayPhone   *string
	VerifiedName   *string
	QualityRating  *string
	MessagingTier  *string
	ChannelStatus  *string
	LastCheckedAt  pgtype.Timestamptz
	WabaID         *string
	TokenStatus    *string
}

func (r row) inbox() Inbox {
	in := Inbox{
		ID: r.ID, OrganizationID: r.OrganizationID, Name: r.Name, ChannelType: r.ChannelType,
		ArchivedAt: timePtr(r.ArchivedAt), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.PhoneNumberID != nil {
		in.WhatsApp = &WhatsAppChannel{
			PhoneNumberID: *r.PhoneNumberID, DisplayPhone: deref(r.DisplayPhone), VerifiedName: deref(r.VerifiedName),
			QualityRating: deref(r.QualityRating), MessagingTier: deref(r.MessagingTier), Status: deref(r.ChannelStatus),
			WABAID: deref(r.WabaID), TokenStatus: deref(r.TokenStatus), LastCheckedAt: timePtr(r.LastCheckedAt),
		}
	}
	return in
}

func (r *pgRepository) List(ctx context.Context, orgID uuid.UUID, includeArchived bool) ([]Inbox, error) {
	rows, err := r.q.ListInboxes(ctx, store.ListInboxesParams{OrganizationID: orgID, IncludeArchived: includeArchived})
	if err != nil {
		return nil, fmt.Errorf("inboxes: listar: %w", err)
	}
	out := make([]Inbox, 0, len(rows))
	for _, x := range rows {
		out = append(out, row(x).inbox())
	}
	return out, nil
}

func (r *pgRepository) Get(ctx context.Context, orgID, id uuid.UUID) (Inbox, error) {
	x, err := r.q.GetInbox(ctx, store.GetInboxParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Inbox{}, ErrNotFound
	}
	if err != nil {
		return Inbox{}, fmt.Errorf("inboxes: leer: %w", err)
	}
	return row(x).inbox(), nil
}

func (r *pgRepository) CountActive(ctx context.Context, orgID uuid.UUID) (int64, error) {
	n, err := r.q.CountActiveInboxes(ctx, orgID)
	if err != nil {
		return 0, fmt.Errorf("inboxes: contar: %w", err)
	}
	return n, nil
}

func (r *pgRepository) Connect(ctx context.Context, n NewConnection) error {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.InsertWhatsAppAccount(ctx, store.InsertWhatsAppAccountParams{
			ID: n.AccountID, OrganizationID: n.OrganizationID, WabaID: n.WABAID, AccessTokenEnc: n.TokenEnc,
			AppSecretEnc: optString(n.AppSecretEnc), ConnectedBy: nullUUID(n.Actor),
		}); err != nil {
			return err
		}
		if _, err := q.InsertInbox(ctx, store.InsertInboxParams{ID: n.InboxID, OrganizationID: n.OrganizationID, Name: n.Name}); err != nil {
			return err
		}
		if _, err := q.InsertWhatsAppChannel(ctx, store.InsertWhatsAppChannelParams{
			InboxID: n.InboxID, OrganizationID: n.OrganizationID, WhatsappAccountID: n.AccountID, PhoneNumberID: n.PhoneNumberID,
			DisplayPhone: n.DisplayPhone, VerifiedName: optString(n.VerifiedName), QualityRating: optString(n.QualityRating),
			MessagingTier: optString(n.MessagingTier),
		}); err != nil {
			return err
		}
		return audit.Entry{
			ActorID: n.Actor, Action: audit.InboxConnected, OrganizationID: n.OrganizationID,
			Detail: map[string]any{"inbox_id": n.InboxID, "name": n.Name, "phone": n.DisplayPhone, "waba_id": n.WABAID},
		}.Insert(ctx, q)
	})
	if db.IsUniqueViolation(err) {
		switch db.ConstraintName(err) {
		case "whatsapp_accounts_waba_id_key":
			return ErrWABATaken
		case "whatsapp_channels_phone_number_id_key":
			return ErrPhoneTaken
		case "inboxes_organization_name_key":
			return ErrNameTaken
		}
	}
	if err != nil {
		return fmt.Errorf("inboxes: conectar: %w", err)
	}
	return nil
}

func (r *pgRepository) Rename(ctx context.Context, orgID, id uuid.UUID, name string, actor uuid.UUID) error {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.RenameInbox(ctx, store.RenameInboxParams{ID: id, OrganizationID: orgID, Name: name}); err != nil {
			return err
		}
		return audit.Entry{ActorID: actor, Action: audit.InboxRenamed, OrganizationID: orgID,
			Detail: map[string]any{"inbox_id": id, "name": name}}.Insert(ctx, q)
	})
	switch {
	case db.IsNoRows(err):
		return ErrNotFound
	case db.IsUniqueViolation(err):
		return ErrNameTaken
	case err != nil:
		return fmt.Errorf("inboxes: renombrar: %w", err)
	}
	return nil
}

func (r *pgRepository) Archive(ctx context.Context, orgID, id, actor uuid.UUID) error {
	found := true
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		n, err := q.ArchiveInbox(ctx, store.ArchiveInboxParams{ID: id, OrganizationID: orgID})
		if err != nil {
			return err
		}
		if n == 0 {
			found = false
			return nil
		}
		if err := q.DeleteWhatsAppChannel(ctx, store.DeleteWhatsAppChannelParams{InboxID: id, OrganizationID: orgID}); err != nil {
			return err
		}
		if err := q.DeleteOrphanWhatsAppAccounts(ctx, orgID); err != nil {
			return err
		}
		return audit.Entry{ActorID: actor, Action: audit.InboxArchived, OrganizationID: orgID,
			Detail: map[string]any{"inbox_id": id}}.Insert(ctx, q)
	})
	if err != nil {
		return fmt.Errorf("inboxes: archivar: %w", err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

func (r *pgRepository) Stored(ctx context.Context, orgID, inboxID uuid.UUID) (StoredCredentials, error) {
	x, err := r.q.GetChannelCredentials(ctx, store.GetChannelCredentialsParams{InboxID: inboxID, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return StoredCredentials{}, ErrNotFound
	}
	if err != nil {
		return StoredCredentials{}, fmt.Errorf("inboxes: leer credenciales: %w", err)
	}
	return StoredCredentials{
		InboxID: x.InboxID, OrganizationID: x.OrganizationID, AccountID: x.AccountID, PhoneNumberID: x.PhoneNumberID,
		WABAID: x.WabaID, TokenEnc: x.AccessTokenEnc, AppSecretEnc: x.AppSecretEnc, ChannelStatus: x.ChannelStatus, TokenStatus: x.TokenStatus,
	}, nil
}

func (r *pgRepository) RotateToken(ctx context.Context, orgID, inboxID, accountID uuid.UUID, tokenEnc string, actor uuid.UUID) error {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if err := q.UpdateAccountToken(ctx, store.UpdateAccountTokenParams{ID: accountID, OrganizationID: orgID, AccessTokenEnc: tokenEnc}); err != nil {
			return err
		}
		if err := q.SetChannelStatus(ctx, store.SetChannelStatusParams{InboxID: inboxID, OrganizationID: orgID, Status: ChannelConnected}); err != nil {
			return err
		}
		return audit.Entry{ActorID: actor, Action: audit.InboxCredentialsRotated, OrganizationID: orgID,
			Detail: map[string]any{"inbox_id": inboxID}}.Insert(ctx, q)
	})
	if err != nil {
		return fmt.Errorf("inboxes: rotar token: %w", err)
	}
	return nil
}

func (r *pgRepository) RefreshInfo(ctx context.Context, orgID, inboxID uuid.UUID, display, verifiedName, quality, tier string) error {
	err := r.q.RefreshChannelInfo(ctx, store.RefreshChannelInfoParams{
		InboxID: inboxID, OrganizationID: orgID, DisplayPhone: display,
		VerifiedName: optString(verifiedName), QualityRating: optString(quality), MessagingTier: optString(tier),
	})
	if err != nil {
		return fmt.Errorf("inboxes: actualizar datos del número: %w", err)
	}
	return nil
}

func (r *pgRepository) MarkNeedsReauth(ctx context.Context, orgID, inboxID, accountID uuid.UUID) error {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if err := q.SetAccountTokenStatus(ctx, store.SetAccountTokenStatusParams{ID: accountID, OrganizationID: orgID, TokenStatus: "invalid"}); err != nil {
			return err
		}
		return q.SetChannelStatus(ctx, store.SetChannelStatusParams{InboxID: inboxID, OrganizationID: orgID, Status: ChannelNeedsReauth})
	})
	if err != nil {
		return fmt.Errorf("inboxes: marcar token inválido: %w", err)
	}
	return nil
}
