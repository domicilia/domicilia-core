package conversations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

type pgRepository struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool, q: store.New(pool)}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	return &n.UUID
}

func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func toTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// convRow reúne lo común de GetConversationRow y ListConversationsRow (sqlc genera un tipo por consulta).
type convRow struct {
	ID                    uuid.UUID
	OrganizationID        uuid.UUID
	InboxID               uuid.UUID
	ContactID             uuid.UUID
	DisplayID             int64
	Status                string
	Priority              *string
	AssigneeID            uuid.NullUUID
	HandledBy             string
	LastCustomerMessageAt pgtype.Timestamptz
	LastActivityAt        time.Time
	LastMessagePreview    *string
	LastMessageDirection  *string
	FirstResponseAt       pgtype.Timestamptz
	WaitingSince          pgtype.Timestamptz
	SnoozedUntil          pgtype.Timestamptz
	UnreadCount           int32
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ContactName           *string
	ContactPhone          string
	ContactBlocked        bool
	ChannelStatus         *string
}

func (r convRow) row() ConversationRow {
	return ConversationRow{
		ChannelStatus: r.ChannelStatus,
		Conversation: Conversation{
			ID: r.ID, OrganizationID: r.OrganizationID, InboxID: r.InboxID, DisplayID: r.DisplayID, Status: r.Status,
			Priority: r.Priority, AssigneeID: uuidPtr(r.AssigneeID), HandledBy: r.HandledBy,
			Contact:               ContactBrief{ID: r.ContactID, Name: r.ContactName, Phone: r.ContactPhone, Blocked: r.ContactBlocked},
			LastCustomerMessageAt: timePtr(r.LastCustomerMessageAt), LastActivityAt: r.LastActivityAt,
			LastMessagePreview: r.LastMessagePreview, LastMessageDirection: r.LastMessageDirection,
			UnreadCount: int(r.UnreadCount), FirstResponseAt: timePtr(r.FirstResponseAt), WaitingSince: timePtr(r.WaitingSince),
			SnoozedUntil: timePtr(r.SnoozedUntil), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		},
	}
}

func (r *pgRepository) List(ctx context.Context, q ListQuery) ([]ConversationRow, error) {
	p := store.ListConversationsParams{
		OrganizationID: q.OrganizationID, InboxID: nullUUID(q.InboxID), AssigneeMode: q.AssigneeMode, AssigneeID: nullUUID(q.AssigneeID),
		PageSize: int32(q.Limit + 1), //nolint:gosec // acotado por el paginador
	}
	if q.Status != "" {
		p.Status = &q.Status
	}
	if q.Search != "" {
		s := db.EscapeLike(q.Search)
		p.Search = &s
	}
	if q.Before != nil {
		p.BeforeAt = pgtype.Timestamptz{Time: q.Before.At, Valid: true}
		p.BeforeID = uuid.NullUUID{UUID: q.Before.ID, Valid: true}
	}
	rows, err := r.q.ListConversations(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("conversations: listar: %w", err)
	}
	out := make([]ConversationRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, convRow(x).row())
	}
	return out, nil
}

func (r *pgRepository) Tabs(ctx context.Context, orgID, userID uuid.UUID, inboxID *uuid.UUID, status string) (Tabs, error) {
	p := store.CountConversationTabsParams{OrganizationID: orgID, UserID: uuid.NullUUID{UUID: userID, Valid: true}, InboxID: nullUUID(inboxID)}
	if status != "" {
		p.Status = &status
	}
	x, err := r.q.CountConversationTabs(ctx, p)
	if err != nil {
		return Tabs{}, fmt.Errorf("conversations: contar: %w", err)
	}
	return Tabs{Mine: x.Mine, Unassigned: x.Unassigned, All: x.Total}, nil
}

func (r *pgRepository) Get(ctx context.Context, orgID, id uuid.UUID) (ConversationRow, error) {
	x, err := r.q.GetConversation(ctx, store.GetConversationParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return ConversationRow{}, ErrNotFound
	}
	if err != nil {
		return ConversationRow{}, fmt.Errorf("conversations: leer: %w", err)
	}
	return convRow(x).row(), nil
}

func (r *pgRepository) Update(ctx context.Context, orgID, id uuid.UUID, p Patch) error {
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.GetConversationForUpdate(ctx, store.GetConversationForUpdateParams{ID: id, OrganizationID: orgID}); err != nil {
			return err
		}
		_, err := q.UpdateConversation(ctx, store.UpdateConversationParams{
			ID: id, OrganizationID: orgID, Status: p.Status, SetPriority: p.SetPriority, Priority: p.Priority,
			SetAssignee: p.SetAssignee, AssigneeID: nullUUID(p.AssigneeID), HandledBy: p.HandledBy,
			SetSnoozed: p.SetSnoozed, SnoozedUntil: toTimestamptz(p.SnoozedUntil),
		})
		return err
	})
	switch {
	case db.IsNoRows(err):
		return ErrNotFound
	case db.IsUniqueViolation(err) && db.ConstraintName(err) == "conversations_one_active_key":
		return ErrActiveExists
	case err != nil:
		return fmt.Errorf("conversations: actualizar: %w", err)
	}
	return nil
}

func (r *pgRepository) MarkRead(ctx context.Context, orgID, id uuid.UUID) error {
	if err := r.q.MarkConversationRead(ctx, store.MarkConversationReadParams{ID: id, OrganizationID: orgID}); err != nil {
		return fmt.Errorf("conversations: marcar leída: %w", err)
	}
	return nil
}

func (r *pgRepository) CanReply(ctx context.Context, orgID, userID uuid.UUID) (bool, error) {
	ok, err := r.q.UserCanReplyInOrganization(ctx, store.UserCanReplyInOrganizationParams{UserID: userID, OrganizationID: orgID})
	if err != nil {
		return false, fmt.Errorf("conversations: comprobar agente: %w", err)
	}
	return ok, nil
}

func toMessage(m store.Message, atts []store.MessageAttachment) (Message, error) {
	payload := map[string]any{}
	if len(m.Payload) > 0 {
		if err := json.Unmarshal(m.Payload, &payload); err != nil {
			return Message{}, fmt.Errorf("conversations: payload de %s: %w", m.ID, err)
		}
	}
	out := Message{
		ID: m.ID, ConversationID: m.ConversationID, Direction: m.Direction, Kind: m.Kind, Body: m.Body, Status: m.Status,
		ErrorCode: m.ErrorCode, ErrorDetail: m.ErrorDetail, SenderUserID: uuidPtr(m.SenderUserID), ReplyToMessageID: uuidPtr(m.ReplyToMessageID),
		Payload: payload, Attachments: []Attachment{}, SentAt: timePtr(m.SentAt), DeliveredAt: timePtr(m.DeliveredAt),
		ReadAt: timePtr(m.ReadAt), FailedAt: timePtr(m.FailedAt), CreatedAt: m.CreatedAt,
	}
	for _, a := range atts {
		out.Attachments = append(out.Attachments, Attachment{
			ID: a.ID, Kind: a.Kind, MimeType: a.MimeType, Filename: a.Filename, SizeBytes: a.SizeBytes, Caption: a.Caption,
		})
	}
	return out, nil
}

func (r *pgRepository) Messages(ctx context.Context, orgID, convID uuid.UUID, before *Cursor, limit int) ([]Message, error) {
	p := store.ListMessagesParams{ConversationID: convID, OrganizationID: orgID, PageSize: int32(limit + 1)} //nolint:gosec // acotado por el paginador
	if before != nil {
		p.BeforeAt = pgtype.Timestamptz{Time: before.At, Valid: true}
		p.BeforeID = uuid.NullUUID{UUID: before.ID, Valid: true}
	}
	rows, err := r.q.ListMessages(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("conversations: listar mensajes: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, m := range rows {
		ids = append(ids, m.ID)
	}
	byMsg := map[uuid.UUID][]store.MessageAttachment{}
	if len(ids) > 0 {
		atts, err := r.q.ListAttachmentsForMessages(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("conversations: listar adjuntos: %w", err)
		}
		for _, a := range atts {
			byMsg[a.MessageID] = append(byMsg[a.MessageID], a)
		}
	}
	out := make([]Message, 0, len(rows))
	for _, m := range rows {
		msg, err := toMessage(m, byMsg[m.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

func (r *pgRepository) Enqueue(ctx context.Context, n NewOutbound) (Message, error) {
	var msg store.Message
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		var err error
		if msg, err = q.InsertOutboundMessage(ctx, store.InsertOutboundMessageParams{
			OrganizationID: n.OrganizationID, ConversationID: n.ConversationID, InboxID: n.InboxID, Kind: "text", Body: &n.Body,
			SenderUserID: uuid.NullUUID{UUID: n.SenderUserID, Valid: n.SenderUserID != uuid.Nil}, ReplyToMessageID: nullUUID(n.ReplyToMessageID),
		}); err != nil {
			return err
		}
		return q.TouchConversationOutbound(ctx, store.TouchConversationOutboundParams{
			ID: n.ConversationID, OrganizationID: n.OrganizationID, At: n.At, Preview: &n.Preview,
		})
	})
	if err != nil {
		return Message{}, fmt.Errorf("conversations: encolar mensaje: %w", err)
	}
	return toMessage(msg, nil)
}

func (r *pgRepository) MessageInConversation(ctx context.Context, orgID, convID, msgID uuid.UUID) (bool, error) {
	m, err := r.q.GetMessage(ctx, store.GetMessageParams{ID: msgID, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("conversations: leer mensaje: %w", err)
	}
	return m.ConversationID == convID, nil
}
