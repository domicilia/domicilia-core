// Package whatsapp conecta el core con WhatsApp: recibe lo que Meta envía por webhook (mensajes de
// clientes y estados de entrega) y entrega los mensajes que el equipo escribió.
//
// Dos garantías gobiernan el diseño:
//
//   - Aislamiento: Meta no dice de qué organización es un evento, solo a qué número llegó. Cada evento se
//     autoriza con la firma que le corresponde a ESE número (su propio secreto de app, o el de la
//     plataforma). Una firma válida de un negocio nunca autoriza eventos de otro.
//   - Idempotencia: Meta reintenta y reordena. Procesar dos veces el mismo evento, o verlo fuera de orden,
//     deja el mismo resultado.
package whatsapp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/contacts"
	"github.com/domicilia/domicilia-core/internal/meta"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

// Errores que el handler traduce a un código HTTP.
var (
	ErrBadSignature = errors.New("whatsapp: firma inválida")
	ErrBadPayload   = errors.New("whatsapp: carga inválida")
)

const (
	eventSource      = "whatsapp"
	maxBodyRunes     = 4096
	previewRunes     = 140
	maxFutureSkew    = 5 * time.Minute
	unsupportedText  = "Mensaje no soportado"
	unknownMediaText = "[archivo]"
)

// SecretOpener descifra el secreto de app propio de una cuenta.
type SecretOpener interface {
	OpenAppSecret(accountID uuid.UUID, enc *string) (string, error)
}

// IngestConfig son los secretos de la app de Meta de la plataforma.
type IngestConfig struct {
	// AppSecret firma los webhooks de la app de la plataforma. Vacío: solo valen los secretos propios.
	AppSecret string
	// VerifyToken responde el desafío de suscripción (GET). Vacío: el webhook no está configurado.
	VerifyToken string
}

// Ingest procesa los webhooks de Meta.
type Ingest struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	secrets SecretOpener
	cfg     IngestConfig
	log     *slog.Logger
	now     func() time.Time
}

// NewIngest crea el procesador. now puede ser nil.
func NewIngest(pool *pgxpool.Pool, secrets SecretOpener, cfg IngestConfig, log *slog.Logger, now func() time.Time) *Ingest {
	if now == nil {
		now = time.Now
	}
	return &Ingest{pool: pool, q: store.New(pool), secrets: secrets, cfg: cfg, log: log, now: now}
}

// Configured dice si el webhook puede responder al desafío de suscripción.
func (i *Ingest) Configured() bool { return i.cfg.VerifyToken != "" }

// VerifyChallenge responde el desafío de suscripción de Meta (GET). Devuelve el texto a responder y si el
// token coincide. La comparación es de tiempo constante.
func (i *Ingest) VerifyChallenge(mode, token, challenge string) (string, bool) {
	if i.cfg.VerifyToken == "" || mode != "subscribe" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(i.cfg.VerifyToken)) != 1 {
		return "", false
	}
	return challenge, true
}

// channelInfo es lo que se sabe del número al que llegó un evento.
type channelInfo struct {
	row       store.GetChannelByPhoneNumberIDRow
	ownSecret string
}

// Handle verifica y procesa una entrega. Devuelve ErrBadPayload, ErrBadSignature o un error interno (que
// el handler responde con 5xx para que Meta reintente).
func (i *Ingest) Handle(ctx context.Context, body []byte, signature string) error {
	w, err := meta.ParseWebhook(body)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadPayload, err)
	}

	// 1. Resolver el número de cada cambio (dato NO confiable todavía: solo elige qué secreto probar).
	channels := map[string]*channelInfo{}
	for _, e := range w.Entry {
		for _, ch := range e.Changes {
			id := ch.Value.Metadata.PhoneNumberID
			if id == "" || channels[id] != nil {
				continue
			}
			row, err := i.q.GetChannelByPhoneNumberID(ctx, id)
			if db.IsNoRows(err) {
				channels[id] = &channelInfo{} // número desconocido: solo la firma de la plataforma lo autoriza
				continue
			}
			if err != nil {
				return fmt.Errorf("whatsapp: buscar el número: %w", err)
			}
			secret, err := i.secrets.OpenAppSecret(row.AccountID, row.AppSecretEnc)
			if err != nil {
				return fmt.Errorf("whatsapp: descifrar el secreto de la app: %w", err)
			}
			channels[id] = &channelInfo{row: row, ownSecret: secret}
		}
	}

	// 2. Autorizar CADA cambio con el secreto de su número. Si alguno no lo está, se rechaza todo: una entrega
	// legítima de Meta viene firmada por una sola app.
	platformOK := i.cfg.AppSecret != "" && meta.VerifySignature(i.cfg.AppSecret, body, signature)
	ownOK := map[string]bool{}
	for id, ci := range channels {
		ownOK[id] = ci.ownSecret != "" && meta.VerifySignature(ci.ownSecret, body, signature)
	}
	anyChange := false
	for _, e := range w.Entry {
		for _, ch := range e.Changes {
			anyChange = true
			if !platformOK && !ownOK[ch.Value.Metadata.PhoneNumberID] {
				return ErrBadSignature
			}
		}
	}
	if !anyChange && !platformOK {
		return ErrBadSignature
	}

	// 3. Registrar la entrega (idempotencia por contenido) y procesarla.
	sum := sha256.Sum256(body)
	ev, err := i.q.UpsertWebhookEvent(ctx, store.UpsertWebhookEventParams{Source: eventSource, EventKey: sum[:], Payload: json.RawMessage(body)})
	if err != nil {
		return fmt.Errorf("whatsapp: registrar el evento: %w", err)
	}
	if ev.ProcessedAt.Valid {
		return nil // reentrega de algo ya procesado
	}
	for _, e := range w.Entry {
		for _, ch := range e.Changes {
			if ch.Field != "messages" {
				continue // otros campos suscritos se guardan crudos, sin procesar
			}
			ci := channels[ch.Value.Metadata.PhoneNumberID]
			if ci == nil || ci.row.InboxID == uuid.Nil {
				i.log.Warn("evento para un número desconocido: se ignora", "phone_number_id", ch.Value.Metadata.PhoneNumberID)
				continue
			}
			if ci.row.OrganizationStatus == "archived" {
				continue
			}
			if err := i.processChange(ctx, ci.row, ch.Value); err != nil {
				_ = i.q.MarkWebhookFailed(ctx, store.MarkWebhookFailedParams{ID: ev.ID, Error: optString(err.Error())})
				return fmt.Errorf("whatsapp: procesar: %w", err)
			}
		}
	}
	if err := i.q.MarkWebhookProcessed(ctx, ev.ID); err != nil {
		return fmt.Errorf("whatsapp: cerrar el evento: %w", err)
	}
	return nil
}

func (i *Ingest) processChange(ctx context.Context, ch store.GetChannelByPhoneNumberIDRow, v meta.ChangeValue) error {
	return db.InTx(ctx, i.pool, func(tx pgx.Tx) error {
		q := i.q.WithTx(tx)
		profiles := map[string]string{}
		for _, c := range v.Contacts {
			profiles[c.WAID] = c.Profile.Name
		}
		for _, m := range v.Messages {
			if err := i.ingestMessage(ctx, q, ch, profiles, m); err != nil {
				return err
			}
		}
		for _, st := range v.Statuses {
			if err := i.applyStatus(ctx, q, ch, st); err != nil {
				return err
			}
		}
		return nil
	})
}

// classified es un mensaje entrante ya interpretado.
type classified struct {
	kind    string
	body    string
	payload map[string]any
	media   *meta.Media
}

func classify(m meta.Message) (classified, bool) {
	switch m.Type {
	case "text":
		if m.Text == nil {
			return classified{kind: "unsupported", body: unsupportedText}, true
		}
		return classified{kind: "text", body: m.Text.Body}, true
	case "image", "video", "document", "audio", "sticker":
		var med *meta.Media
		switch m.Type {
		case "image":
			med = m.Image
		case "video":
			med = m.Video
		case "document":
			med = m.Document
		case "audio":
			med = m.Audio
		case "sticker":
			med = m.Sticker
		}
		if med == nil {
			return classified{kind: "unsupported", body: unsupportedText}, true
		}
		return classified{kind: m.Type, body: med.Caption, media: med}, true
	case "location":
		if m.Location == nil {
			return classified{kind: "unsupported", body: unsupportedText}, true
		}
		body := m.Location.Name
		if body == "" {
			body = m.Location.Address
		}
		return classified{kind: "location", body: body, payload: map[string]any{
			"latitude": m.Location.Latitude, "longitude": m.Location.Longitude, "name": m.Location.Name, "address": m.Location.Address,
		}}, true
	case "interactive":
		if m.Interactive == nil {
			return classified{kind: "unsupported", body: unsupportedText}, true
		}
		p := map[string]any{"type": m.Interactive.Type}
		body := ""
		switch {
		case m.Interactive.ButtonReply != nil:
			p["id"], p["title"] = m.Interactive.ButtonReply.ID, m.Interactive.ButtonReply.Title
			body = m.Interactive.ButtonReply.Title
		case m.Interactive.ListReply != nil:
			p["id"], p["title"], p["description"] = m.Interactive.ListReply.ID, m.Interactive.ListReply.Title, m.Interactive.ListReply.Description
			body = m.Interactive.ListReply.Title
		}
		return classified{kind: "interactive", body: body, payload: p}, true
	case "button":
		if m.Button == nil {
			return classified{kind: "unsupported", body: unsupportedText}, true
		}
		return classified{kind: "button", body: m.Button.Text, payload: map[string]any{"payload": m.Button.Payload}}, true
	case "contacts":
		p := map[string]any{}
		if len(m.Contacts) > 0 {
			var raw any
			if json.Unmarshal(m.Contacts, &raw) == nil {
				p["contacts"] = raw
			}
		}
		return classified{kind: "contacts", body: "Contacto compartido", payload: p}, true
	case "reaction":
		return classified{}, false // una reacción no es un mensaje de la conversación
	default:
		return classified{kind: "unsupported", body: unsupportedText, payload: map[string]any{"original_type": m.Type}}, true
	}
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func previewOf(c classified) string {
	if c.body != "" {
		return truncateRunes(strings.Join(strings.Fields(c.body), " "), previewRunes)
	}
	return "[" + c.kind + "]"
}

func (i *Ingest) ingestMessage(ctx context.Context, q *store.Queries, ch store.GetChannelByPhoneNumberIDRow, profiles map[string]string, m meta.Message) error {
	c, ok := classify(m)
	if !ok {
		return nil
	}
	if m.ID == "" || m.From == "" {
		return nil // sin id no se puede deduplicar; sin remitente no hay a quién atribuirlo
	}
	// El teléfono se valida ANTES de tocar la base: una violación de restricción dentro de la transacción la aborta
	// entera (y con ella los demás mensajes de la entrega).
	phone, perr := contacts.NormalizePhone(meta.E164(m.From))
	if perr != nil {
		i.log.Warn("remitente con un número inválido: se ignora", "organization_id", ch.OrganizationID)
		return nil
	}
	var name *string
	if n := strings.TrimSpace(profiles[m.From]); n != "" {
		n = truncateRunes(n, 255)
		name = &n
	}
	contact, err := q.UpsertContactByPhone(ctx, store.UpsertContactByPhoneParams{
		OrganizationID: ch.OrganizationID, PhoneE164: phone, Name: name, Source: "inbound",
	})
	if err != nil {
		return fmt.Errorf("contacto: %w", err)
	}
	if contact.Blocked {
		return nil // un contacto bloqueado no abre conversaciones
	}

	now := i.now()
	at := meta.Time(m.Timestamp, now)
	if at.After(now.Add(maxFutureSkew)) {
		at = now // un reloj adelantado no debe abrir una ventana en el futuro
	}

	conv, err := i.activeConversation(ctx, q, ch, contact.ID, at)
	if err != nil {
		return err
	}

	payload := c.payload
	if payload == nil {
		payload = map[string]any{}
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	var body *string
	if c.body != "" {
		b := truncateRunes(c.body, maxBodyRunes)
		body = &b
	}
	var replyTo uuid.NullUUID
	if m.Context != nil && m.Context.ID != "" {
		if orig, err := q.GetMessageByWamid(ctx, store.GetMessageByWamidParams{InboxID: ch.InboxID, Wamid: &m.Context.ID}); err == nil {
			replyTo = uuid.NullUUID{UUID: orig.ID, Valid: true}
		}
	}
	msg, err := q.InsertInboundMessage(ctx, store.InsertInboundMessageParams{
		OrganizationID: ch.OrganizationID, ConversationID: conv.ID, InboxID: ch.InboxID, Kind: c.kind, Body: body, Wamid: &m.ID,
		Payload: rawPayload, ReplyToMessageID: replyTo, CreatedAt: at,
	})
	if db.IsNoRows(err) {
		return nil // el mismo mensaje ya se procesó (Meta reintenta)
	}
	if err != nil {
		return fmt.Errorf("mensaje: %w", err)
	}
	if c.media != nil {
		if err := q.InsertMessageAttachment(ctx, store.InsertMessageAttachmentParams{
			OrganizationID: ch.OrganizationID, MessageID: msg.ID, Kind: c.kind, MimeType: optString(c.media.MimeType),
			Filename: optString(c.media.Filename), Sha256: optString(c.media.SHA256), WaMediaID: optString(c.media.ID), Caption: optString(c.media.Caption),
		}); err != nil {
			return fmt.Errorf("adjunto: %w", err)
		}
	}
	prev := previewOf(c)
	if err := q.TouchConversationInbound(ctx, store.TouchConversationInboundParams{
		ID: conv.ID, OrganizationID: ch.OrganizationID, At: pgtype.Timestamptz{Time: at, Valid: true}, Preview: &prev,
	}); err != nil {
		return fmt.Errorf("conversación: %w", err)
	}
	return nil
}

// activeConversation devuelve la conversación no resuelta del contacto en esa bandeja (bloqueada para actualizarla)
// o crea una nueva. Dos mensajes simultáneos de alguien nuevo no crean dos: el segundo choca con el índice único
// y reutiliza la primera.
func (i *Ingest) activeConversation(ctx context.Context, q *store.Queries, ch store.GetChannelByPhoneNumberIDRow, contactID uuid.UUID, at time.Time) (store.Conversation, error) {
	get := func() (store.Conversation, error) {
		return q.GetActiveConversationForUpdate(ctx, store.GetActiveConversationForUpdateParams{
			InboxID: ch.InboxID, ContactID: contactID, OrganizationID: ch.OrganizationID,
		})
	}
	conv, err := get()
	if err == nil {
		return conv, nil
	}
	if !db.IsNoRows(err) {
		return store.Conversation{}, fmt.Errorf("buscar conversación: %w", err)
	}
	seq, err := q.NextConversationDisplayID(ctx, ch.OrganizationID)
	if err != nil {
		return store.Conversation{}, fmt.Errorf("numerar conversación: %w", err)
	}
	conv, err = q.InsertConversation(ctx, store.InsertConversationParams{
		OrganizationID: ch.OrganizationID, InboxID: ch.InboxID, ContactID: contactID, DisplayID: seq, LastActivityAt: at,
	})
	if db.IsNoRows(err) { // otro proceso la creó entre la búsqueda y el alta
		if conv, err = get(); err != nil {
			return store.Conversation{}, fmt.Errorf("reutilizar conversación: %w", err)
		}
		return conv, nil
	}
	if err != nil {
		return store.Conversation{}, fmt.Errorf("crear conversación: %w", err)
	}
	return conv, nil
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ApplyStatus aplica el estado de entrega de un mensaje nuestro. Si el mensaje aún no tiene su wamid (el estado llegó
// antes de que el envío lo registrara) se guarda aparte y se aplica cuando lo tenga.
func (i *Ingest) applyStatus(ctx context.Context, q *store.Queries, ch store.GetChannelByPhoneNumberIDRow, st meta.Status) error {
	if st.ID == "" {
		return nil
	}
	switch st.Status {
	case "sent", "delivered", "read", "failed":
	default:
		return nil // "deleted", "warning"...: no cambian el estado de entrega
	}
	now := i.now()
	at := meta.Time(st.Timestamp, now)
	var code, detail *string
	if st.Status == "failed" && len(st.Errors) > 0 {
		c := strconv.Itoa(st.Errors[0].Code)
		d := truncateRunes(st.Errors[0].Detail(), 500)
		code, detail = &c, optString(d)
	}
	var category *string
	var billable *bool
	if st.Pricing != nil {
		category, billable = optString(st.Pricing.Category), &st.Pricing.Billable
	}
	n, err := q.ApplyMessageStatus(ctx, store.ApplyMessageStatusParams{
		InboxID: ch.InboxID, Wamid: &st.ID, Status: st.Status, At: pgtype.Timestamptz{Time: at, Valid: true},
		ErrorCode: code, ErrorDetail: detail, PricingCategory: category, Billable: billable,
	})
	if err != nil {
		return fmt.Errorf("estado: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := q.GetMessageByWamid(ctx, store.GetMessageByWamidParams{InboxID: ch.InboxID, Wamid: &st.ID}); err == nil {
		return nil // existe pero ya estaba en un estado igual o posterior: no retrocede
	} else if !db.IsNoRows(err) {
		return fmt.Errorf("estado: %w", err)
	}
	if err := q.InsertUnmatchedStatus(ctx, store.InsertUnmatchedStatusParams{
		Wamid: st.ID, Status: st.Status, OccurredAt: at, ErrorCode: code, ErrorDetail: detail, PricingCategory: category, Billable: billable,
	}); err != nil {
		return fmt.Errorf("guardar estado sin mensaje: %w", err)
	}
	return nil
}
