// Package conversations es la bandeja de atención: las conversaciones con cada contacto, sus
// mensajes, y cómo se responde. Aquí vive la regla que gobierna WhatsApp: un texto libre solo se
// puede enviar dentro de las 24 horas siguientes al último mensaje del cliente; fuera de esa
// ventana solo se admiten plantillas aprobadas (otro módulo).
//
// Enviar NO habla con Meta: el mensaje nace "queued" y lo entrega un trabajador
// (internal/whatsapp), así un fallo de Meta no rompe la petición de quien atiende.
package conversations

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// WindowDuration es la ventana de servicio de WhatsApp.
const WindowDuration = 24 * time.Hour

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound = errors.New("conversations: no encontrada")
	// ErrActiveExists: reabrir chocaría con otra conversación no resuelta del mismo contacto.
	ErrActiveExists = errors.New("conversations: el contacto ya tiene otra conversación abierta")
)

// Estados y valores permitidos.
var (
	Statuses   = []string{"open", "pending", "resolved", "snoozed"}
	Priorities = []string{"low", "medium", "high", "urgent"}
	HandledBy  = []string{"bot", "human"}
)

// Motivos por los que no se puede responder con texto libre.
const (
	BlockWindowClosed  = "window_closed"
	BlockContactBlock  = "contact_blocked"
	BlockInboxDisabled = "inbox_disconnected"
)

// ContactBrief es lo mínimo del contacto para mostrar la conversación.
type ContactBrief struct {
	ID      uuid.UUID `json:"id"`
	Name    *string   `json:"name"`
	Phone   string    `json:"phone"`
	Blocked bool      `json:"blocked"`
}

// Conversation es una conversación con un contacto.
type Conversation struct {
	ID             uuid.UUID    `json:"id"`
	OrganizationID uuid.UUID    `json:"organization_id"`
	InboxID        uuid.UUID    `json:"inbox_id"`
	DisplayID      int64        `json:"display_id"`
	Status         string       `json:"status"`
	Priority       *string      `json:"priority"`
	AssigneeID     *uuid.UUID   `json:"assignee_id"`
	HandledBy      string       `json:"handled_by"`
	Contact        ContactBrief `json:"contact"`

	LastCustomerMessageAt *time.Time `json:"last_customer_message_at"`
	// WindowExpiresAt es cuándo se cierra la ventana de 24 h (null si el cliente nunca escribió).
	WindowExpiresAt *time.Time `json:"window_expires_at"`
	// CanReply dice si ahora se puede enviar un texto libre; si no, ReplyBlockReason dice por qué.
	CanReply         bool    `json:"can_reply"`
	ReplyBlockReason *string `json:"reply_block_reason"`

	LastActivityAt       time.Time  `json:"last_activity_at"`
	LastMessagePreview   *string    `json:"last_message_preview"`
	LastMessageDirection *string    `json:"last_message_direction"`
	UnreadCount          int        `json:"unread_count"`
	FirstResponseAt      *time.Time `json:"first_response_at"`
	WaitingSince         *time.Time `json:"waiting_since"`
	SnoozedUntil         *time.Time `json:"snoozed_until"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// Attachment describe un archivo de un mensaje. La URL de descarga se pide aparte (caduca).
type Attachment struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"`
	MimeType  *string   `json:"mime_type"`
	Filename  *string   `json:"filename"`
	SizeBytes *int64    `json:"size_bytes"`
	Caption   *string   `json:"caption"`
}

// Message es un mensaje de una conversación.
type Message struct {
	ID               uuid.UUID      `json:"id"`
	ConversationID   uuid.UUID      `json:"conversation_id"`
	Direction        string         `json:"direction"`
	Kind             string         `json:"kind"`
	Body             *string        `json:"body"`
	Status           string         `json:"status"`
	ErrorCode        *string        `json:"error_code"`
	ErrorDetail      *string        `json:"error_detail"`
	SenderUserID     *uuid.UUID     `json:"sender_user_id"`
	ReplyToMessageID *uuid.UUID     `json:"reply_to_message_id"`
	Payload          map[string]any `json:"payload"`
	Attachments      []Attachment   `json:"attachments"`
	SentAt           *time.Time     `json:"sent_at"`
	DeliveredAt      *time.Time     `json:"delivered_at"`
	ReadAt           *time.Time     `json:"read_at"`
	FailedAt         *time.Time     `json:"failed_at"`
	CreatedAt        time.Time      `json:"created_at"`
}

// Tabs son los contadores de las pestañas de la bandeja.
type Tabs struct {
	Mine       int64 `json:"mine"`
	Unassigned int64 `json:"unassigned"`
	All        int64 `json:"all"`
}

// Cursor es la posición de la última fila vista.
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// Límites.
const maxBody = 4096

// withWindow completa lo que se deduce del reloj: la ventana y si se puede responder.
func withWindow(c Conversation, now time.Time, channelStatus *string) Conversation {
	if c.LastCustomerMessageAt != nil {
		end := c.LastCustomerMessageAt.Add(WindowDuration)
		c.WindowExpiresAt = &end
	}
	block := func(reason string) Conversation {
		c.CanReply, c.ReplyBlockReason = false, &reason
		return c
	}
	switch {
	case channelStatus == nil || *channelStatus != "connected":
		return block(BlockInboxDisabled)
	case c.Contact.Blocked:
		return block(BlockContactBlock)
	case c.WindowExpiresAt == nil || !now.Before(*c.WindowExpiresAt):
		return block(BlockWindowClosed)
	}
	c.CanReply, c.ReplyBlockReason = true, nil
	return c
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	List(ctx context.Context, q ListQuery) ([]ConversationRow, error)
	Tabs(ctx context.Context, orgID, userID uuid.UUID, inboxID *uuid.UUID, status string) (Tabs, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (ConversationRow, error)
	// Update aplica el cambio bajo bloqueo. Devuelve ErrNotFound o ErrActiveExists.
	Update(ctx context.Context, orgID, id uuid.UUID, p Patch) error
	MarkRead(ctx context.Context, orgID, id uuid.UUID) error
	CanReply(ctx context.Context, orgID, userID uuid.UUID) (bool, error)

	Messages(ctx context.Context, orgID, convID uuid.UUID, before *Cursor, limit int) ([]Message, error)
	// Enqueue crea el mensaje saliente en estado queued y actualiza la conversación, en una transacción.
	Enqueue(ctx context.Context, m NewOutbound) (Message, error)
	MessageInConversation(ctx context.Context, orgID, convID, msgID uuid.UUID) (bool, error)
}

// ConversationRow es la conversación con lo que el reloj no sabe (estado del canal).
type ConversationRow struct {
	Conversation  Conversation
	ChannelStatus *string
}

// ListQuery son los filtros del listado.
type ListQuery struct {
	OrganizationID uuid.UUID
	InboxID        *uuid.UUID
	Status         string // "" = todos
	AssigneeMode   string // all | me | unassigned | user
	AssigneeID     *uuid.UUID
	Search         string
	Before         *Cursor
	Limit          int
}

// Patch es un cambio parcial de una conversación.
type Patch struct {
	Status       *string
	SetPriority  bool
	Priority     *string
	SetAssignee  bool
	AssigneeID   *uuid.UUID
	HandledBy    *string
	SetSnoozed   bool
	SnoozedUntil *time.Time
}

// NewOutbound son los datos de un mensaje saliente.
type NewOutbound struct {
	OrganizationID   uuid.UUID
	ConversationID   uuid.UUID
	InboxID          uuid.UUID
	Body             string
	SenderUserID     uuid.UUID
	ReplyToMessageID *uuid.UUID
	Preview          string
	At               time.Time
}
