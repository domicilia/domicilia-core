package conversations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// Plans es la puerta de las funciones por plan.
type Plans interface {
	Require(ctx context.Context, orgID uuid.UUID, f plans.Feature) error
}

// Notifier despierta al trabajador que envía los mensajes en cuanto hay uno nuevo. Es opcional:
// sin él, el trabajador los encuentra en su siguiente vuelta.
type Notifier interface{ Wake() }

// Service reúne las reglas de las conversaciones y de los mensajes salientes.
type Service struct {
	repo   Repository
	gate   *tenant.Gate
	plans  Plans
	notify Notifier
	now    func() time.Time
}

// NewService crea el servicio. now y notify pueden ser nil.
func NewService(repo Repository, gate *tenant.Gate, p Plans, notify Notifier, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, gate: gate, plans: p, notify: notify, now: now}
}

// ListInput son los filtros del listado.
type ListInput struct {
	InboxID  *uuid.UUID
	Status   string // open (por omisión) | pending | resolved | snoozed | all
	Assignee string // all (por omisión) | me | unassigned | <id de usuario>
	Search   string
	Limit    int
	Before   *Cursor
}

func parseStatus(s string) (string, error) {
	switch s {
	case "":
		return "open", nil
	case "all":
		return "", nil
	}
	if !slices.Contains(Statuses, s) {
		return "", apperr.Invalid("status debe ser open, pending, resolved, snoozed o all")
	}
	return s, nil
}

// List devuelve una página de conversaciones, la de actividad más reciente primero. Devuelve UNA fila más
// que Limit si hay otra página: el llamador arma el cursor.
func (s *Service) List(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in ListInput) ([]Conversation, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxRead, false); err != nil {
		return nil, err
	}
	status, err := parseStatus(in.Status)
	if err != nil {
		return nil, err
	}
	q := ListQuery{OrganizationID: orgID, InboxID: in.InboxID, Status: status, Search: strings.TrimSpace(in.Search), Before: in.Before, Limit: in.Limit, AssigneeMode: "all"}
	switch in.Assignee {
	case "", "all":
	case "me":
		q.AssigneeMode, q.AssigneeID = "me", &actor.ID
	case "unassigned":
		q.AssigneeMode = "unassigned"
	default:
		id, perr := uuid.Parse(in.Assignee)
		if perr != nil {
			return nil, apperr.Invalid("assignee debe ser all, me, unassigned o el id de un usuario")
		}
		q.AssigneeMode, q.AssigneeID = "user", &id
	}
	rows, err := s.repo.List(ctx, q)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]Conversation, 0, len(rows))
	for _, r := range rows {
		out = append(out, withWindow(r.Conversation, now, r.ChannelStatus))
	}
	return out, nil
}

// Tabs devuelve los contadores de las pestañas (mías, sin asignar, todas) para un filtro de bandeja y estado.
func (s *Service) Tabs(ctx context.Context, actor identity.Principal, orgID uuid.UUID, inboxID *uuid.UUID, rawStatus string) (Tabs, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxRead, false); err != nil {
		return Tabs{}, err
	}
	status, err := parseStatus(rawStatus)
	if err != nil {
		return Tabs{}, err
	}
	return s.repo.Tabs(ctx, orgID, actor.ID, inboxID, status)
}

func (s *Service) load(ctx context.Context, orgID, id uuid.UUID) (Conversation, ConversationRow, error) {
	row, err := s.repo.Get(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return Conversation{}, ConversationRow{}, apperr.NotFound("conversación no encontrada")
	}
	if err != nil {
		return Conversation{}, ConversationRow{}, err
	}
	return withWindow(row.Conversation, s.now(), row.ChannelStatus), row, nil
}

// Get devuelve una conversación de la organización.
func (s *Service) Get(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Conversation, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxRead, false); err != nil {
		return Conversation{}, err
	}
	c, _, err := s.load(ctx, orgID, id)
	return c, err
}

// UpdateInput es un cambio parcial. En los campos Field, ausente no toca, null deja en blanco.
type UpdateInput struct {
	Status       *string
	Priority     httpserver.Field[string]
	Assignee     httpserver.Field[uuid.UUID]
	HandledBy    *string
	SnoozedUntil httpserver.Field[time.Time]
}

// Update cambia el estado, la prioridad, el responsable o quién la atiende (bot o persona).
func (s *Service) Update(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, in UpdateInput) (Conversation, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxReply, true); err != nil {
		return Conversation{}, err
	}
	p := Patch{}
	changed := false

	if in.Status != nil {
		if !slices.Contains(Statuses, *in.Status) {
			return Conversation{}, apperr.Invalid("status debe ser open, pending, resolved o snoozed")
		}
		p.Status = in.Status
		changed = true
		if *in.Status == "snoozed" {
			if !in.SnoozedUntil.Set || in.SnoozedUntil.Value == nil || !in.SnoozedUntil.Value.After(s.now()) {
				return Conversation{}, apperr.Invalid("snoozed_until debe ser una fecha futura para posponer")
			}
			p.SetSnoozed, p.SnoozedUntil = true, in.SnoozedUntil.Value
		} else {
			p.SetSnoozed, p.SnoozedUntil = true, nil // salir de "pospuesta" limpia la fecha
		}
	} else if in.SnoozedUntil.Set {
		return Conversation{}, apperr.Invalid("snoozed_until solo se envía junto con status snoozed")
	}

	if in.Priority.Set {
		if in.Priority.Value != nil && !slices.Contains(Priorities, *in.Priority.Value) {
			return Conversation{}, apperr.Invalid("priority debe ser low, medium, high, urgent o null")
		}
		p.SetPriority, p.Priority, changed = true, in.Priority.Value, true
	}
	if in.HandledBy != nil {
		if !slices.Contains(HandledBy, *in.HandledBy) {
			return Conversation{}, apperr.Invalid("handled_by debe ser bot o human")
		}
		p.HandledBy, changed = in.HandledBy, true
	}
	if in.Assignee.Set {
		if in.Assignee.Value != nil {
			ok, err := s.repo.CanReply(ctx, orgID, *in.Assignee.Value)
			if err != nil {
				return Conversation{}, err
			}
			if !ok {
				return Conversation{}, apperr.Invalid("esa persona no puede atender las conversaciones de esta organización")
			}
		}
		p.SetAssignee, p.AssigneeID, changed = true, in.Assignee.Value, true
	}
	if !changed {
		return Conversation{}, apperr.Invalid("envía status, priority, assignee_id o handled_by")
	}

	switch err := s.repo.Update(ctx, orgID, id, p); {
	case errors.Is(err, ErrNotFound):
		return Conversation{}, apperr.NotFound("conversación no encontrada")
	case errors.Is(err, ErrActiveExists):
		return Conversation{}, apperr.Conflict("el contacto ya tiene otra conversación abierta: ciérrala antes de reabrir esta")
	case err != nil:
		return Conversation{}, err
	}
	c, _, err := s.load(ctx, orgID, id)
	return c, err
}

// MarkRead pone en cero los mensajes sin leer de una conversación.
func (s *Service) MarkRead(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) error {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxRead, true); err != nil {
		return err
	}
	if _, _, err := s.load(ctx, orgID, id); err != nil {
		return err
	}
	return s.repo.MarkRead(ctx, orgID, id)
}

// Messages devuelve una página del hilo, del más nuevo al más viejo. Devuelve UNA fila más que limit si
// hay otra página.
func (s *Service) Messages(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, before *Cursor, limit int) ([]Message, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxRead, false); err != nil {
		return nil, err
	}
	if _, _, err := s.load(ctx, orgID, id); err != nil {
		return nil, err
	}
	return s.repo.Messages(ctx, orgID, id, before, limit)
}

// SendInput es un texto a enviar.
type SendInput struct {
	Body             string
	ReplyToMessageID *uuid.UUID
}

const previewLength = 140

func preview(body string) string {
	body = strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(body) <= previewLength {
		return body
	}
	return string([]rune(body)[:previewLength-1]) + "…"
}

// Send encola un texto libre. Solo se puede dentro de la ventana de 24 h, a un contacto no bloqueado y por
// una bandeja conectada; de lo contrario responde 409 diciendo por qué (las mismas razones que
// Conversation.reply_block_reason). El envío a Meta lo hace el trabajador: aquí el mensaje nace en "queued".
func (s *Service) Send(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, in SendInput) (Message, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxReply, true); err != nil {
		return Message{}, err
	}
	body, err := validate.Required("body", in.Body, maxBody)
	if err != nil {
		return Message{}, err
	}
	if err := s.plans.Require(ctx, orgID, plans.Inbox24h); err != nil {
		return Message{}, err
	}
	conv, _, err := s.load(ctx, orgID, id)
	if err != nil {
		return Message{}, err
	}
	if !conv.CanReply {
		reason := ""
		if conv.ReplyBlockReason != nil {
			reason = *conv.ReplyBlockReason
		}
		return Message{}, apperr.Conflict(blockMessage(reason))
	}
	if in.ReplyToMessageID != nil {
		ok, err := s.repo.MessageInConversation(ctx, orgID, id, *in.ReplyToMessageID)
		if err != nil {
			return Message{}, err
		}
		if !ok {
			return Message{}, apperr.Invalid("reply_to_message_id no es un mensaje de esta conversación")
		}
	}
	msg, err := s.repo.Enqueue(ctx, NewOutbound{
		OrganizationID: orgID, ConversationID: id, InboxID: conv.InboxID, Body: body, SenderUserID: actor.ID,
		ReplyToMessageID: in.ReplyToMessageID, Preview: preview(body), At: s.now(),
	})
	if err != nil {
		return Message{}, err
	}
	if s.notify != nil {
		s.notify.Wake()
	}
	return msg, nil
}

func blockMessage(reason string) string {
	switch reason {
	case BlockWindowClosed:
		return "window_closed: pasaron más de 24 horas desde el último mensaje del cliente; solo se puede enviar una plantilla aprobada"
	case BlockContactBlock:
		return "contact_blocked: el contacto está bloqueado"
	case BlockInboxDisabled:
		return "inbox_disconnected: la bandeja no tiene un número conectado; conéctalo o actualiza su token"
	}
	return "no se puede responder a esta conversación ahora"
}
