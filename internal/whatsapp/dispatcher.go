package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/inboxes"
	"github.com/domicilia/domicilia-core/internal/meta"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

// Parámetros del envío.
const (
	batchSize      = 20
	leaseSeconds   = 60
	maxAttempts    = 6
	baseBackoff    = 5 * time.Second
	maxBackoff     = 15 * time.Minute
	idleInterval   = 5 * time.Second
	pruneInterval  = time.Hour
	errCodeExhaust = "retries_exhausted"
	errCodeUnknown = "delivery_unknown"
	errCodeInbox   = "inbox_disconnected"
	maxErrorDetail = 500
)

// Sender es lo que se usa de Meta para enviar.
type Sender interface {
	SendText(ctx context.Context, token, phoneNumberID string, m meta.TextMessage) (string, error)
}

// Credentials da el token de una bandeja y marca el que Meta rechazó.
type Credentials interface {
	Credentials(ctx context.Context, orgID, inboxID uuid.UUID) (inboxes.Credentials, error)
	MarkNeedsReauth(ctx context.Context, orgID, inboxID, accountID uuid.UUID) error
}

// Dispatcher entrega a Meta los mensajes que están en cola.
//
// Reglas de entrega (lo que más importa que sea correcto):
//   - Cada conversación entrega EN ORDEN y de a un mensaje (lo garantiza la consulta que los reclama).
//   - Un mensaje se reclama con un arrendamiento: si el proceso muere a mitad de camino, vence y otro lo retoma.
//   - Un fallo de Meta que no mejora con el tiempo (ventana cerrada, número sin WhatsApp) falla el mensaje YA.
//   - Un fallo transitorio se reintenta con espera creciente, hasta un tope.
//   - Un fallo AMBIGUO (Meta pudo haber aceptado el mensaje) NO se reintenta: reenviar duplicaría el mensaje ante
//     el cliente. Queda "failed" con delivery_unknown y quien atiende decide si reenviarlo.
type Dispatcher struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	creds  Credentials
	sender Sender
	log    *slog.Logger
	now    func() time.Time
	wake   chan struct{}
}

// NewDispatcher crea el trabajador. now puede ser nil.
func NewDispatcher(pool *pgxpool.Pool, creds Credentials, sender Sender, log *slog.Logger, now func() time.Time) *Dispatcher {
	if now == nil {
		now = time.Now
	}
	return &Dispatcher{pool: pool, q: store.New(pool), creds: creds, sender: sender, log: log, now: now, wake: make(chan struct{}, 1)}
}

// Wake pide una vuelta inmediata (hay un mensaje nuevo). No bloquea.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default: // ya hay una vuelta pendiente
	}
}

// Run entrega mensajes hasta que se cancele el contexto. Entre vueltas duerme hasta que lo despierten o
// pase el intervalo, y cada hora poda lo viejo.
func (d *Dispatcher) Run(ctx context.Context) {
	idle := time.NewTicker(idleInterval)
	prune := time.NewTicker(pruneInterval)
	defer idle.Stop()
	defer prune.Stop()
	for {
		n, err := d.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.log.Error("entrega de mensajes de WhatsApp", "error", err)
		}
		if n >= batchSize { // había más de un lote: seguir sin esperar
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-idle.C:
		case <-prune.C:
			d.Prune(ctx)
		}
	}
}

// Prune borra lo que ya no sirve: estados sin mensaje de más de un día y entregas crudas de más de 30 días.
func (d *Dispatcher) Prune(ctx context.Context) {
	if _, err := d.q.PruneUnmatchedStatuses(ctx); err != nil {
		d.log.Warn("podar estados sin mensaje", "error", err)
	}
	if _, err := d.q.PruneWebhookEvents(ctx); err != nil {
		d.log.Warn("podar entregas de webhook", "error", err)
	}
}

// RunOnce reclama un lote y lo entrega. Devuelve cuántos mensajes tomó. Los mensajes del lote son de
// conversaciones distintas, así que se entregan en paralelo.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	claimed, err := d.q.ClaimOutbox(ctx, store.ClaimOutboxParams{BatchSize: batchSize, LeaseSeconds: leaseSeconds})
	if err != nil {
		return 0, fmt.Errorf("reclamar mensajes: %w", err)
	}
	var wg sync.WaitGroup
	for _, m := range claimed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.deliver(ctx, m)
		}()
	}
	wg.Wait()
	return len(claimed), nil
}

func (d *Dispatcher) deliver(ctx context.Context, m store.Message) {
	log := d.log.With("message_id", m.ID, "organization_id", m.OrganizationID)
	if err := d.deliverOne(ctx, m); err != nil {
		// Un error aquí es de NUESTRA infraestructura (base de datos, descifrado): el arrendamiento vence y se reintenta.
		log.Error("no se pudo procesar el mensaje saliente", "error", err)
	}
}

func (d *Dispatcher) deliverOne(ctx context.Context, m store.Message) error {
	conv, err := d.q.GetConversation(ctx, store.GetConversationParams{ID: m.ConversationID, OrganizationID: m.OrganizationID})
	if err != nil {
		return fmt.Errorf("leer la conversación: %w", err)
	}
	creds, err := d.creds.Credentials(ctx, m.OrganizationID, m.InboxID)
	if err != nil {
		// La bandeja ya no existe o se desconectó: el mensaje no se puede entregar nunca.
		return d.fail(ctx, m, errCodeInbox, "la bandeja no tiene un número conectado")
	}
	if creds.ChannelStatus != inboxes.ChannelConnected {
		return d.fail(ctx, m, errCodeInbox, "el token de la bandeja fue rechazado por Meta: hay que cargar uno nuevo")
	}

	msg := meta.TextMessage{To: strings.TrimPrefix(conv.ContactPhone, "+"), Body: deref(m.Body)}
	if m.ReplyToMessageID.Valid {
		if orig, err := d.q.GetMessage(ctx, store.GetMessageParams{ID: m.ReplyToMessageID.UUID, OrganizationID: m.OrganizationID}); err == nil && orig.Wamid != nil {
			msg.ReplyToID = *orig.Wamid
		}
	}

	wamid, err := d.sender.SendText(ctx, creds.AccessToken, creds.PhoneNumberID, msg)
	if err == nil {
		return d.markSent(ctx, m, creds.InboxID, wamid)
	}
	return d.handleSendError(ctx, m, creds, err)
}

func (d *Dispatcher) handleSendError(ctx context.Context, m store.Message, creds inboxes.Credentials, err error) error {
	var ae *meta.APIError
	if errors.As(err, &ae) {
		if ae.AuthFailed() {
			// El token dejó de servir: se avisa y no se siguen intentando envíos que van a fallar.
			if merr := d.creds.MarkNeedsReauth(ctx, creds.OrganizationID, creds.InboxID, creds.AccountID); merr != nil {
				d.log.Error("marcar token inválido", "error", merr)
			}
			return d.fail(ctx, m, strconv.Itoa(ae.Code), ae.Message)
		}
		if ae.Retryable() {
			return d.retry(ctx, m, strconv.Itoa(ae.Code), ae.Message)
		}
		return d.fail(ctx, m, strconv.Itoa(ae.Code), ae.Message)
	}
	var te *meta.TransportError
	if errors.As(err, &te) {
		if te.NeverSent() {
			return d.retry(ctx, m, "network", "no se pudo conectar con Meta")
		}
		return d.fail(ctx, m, errCodeUnknown, "no se sabe si Meta recibió el mensaje: revisa la conversación antes de reenviarlo")
	}
	// Respuesta ilegible o sin id: Meta la procesó, pero no sabemos cómo. Tampoco se reintenta.
	return d.fail(ctx, m, errCodeUnknown, "respuesta inesperada de Meta")
}

// markSent registra el wamid y aplica los estados que llegaron antes que él (webhook más rápido que la respuesta).
func (d *Dispatcher) markSent(ctx context.Context, m store.Message, inboxID uuid.UUID, wamid string) error {
	return db.InTx(ctx, d.pool, func(tx pgx.Tx) error {
		q := d.q.WithTx(tx)
		if err := q.MarkMessageSent(ctx, store.MarkMessageSentParams{ID: m.ID, OrganizationID: m.OrganizationID, Wamid: &wamid}); err != nil {
			return fmt.Errorf("marcar enviado: %w", err)
		}
		pending, err := q.ListUnmatchedStatuses(ctx, wamid)
		if err != nil {
			return fmt.Errorf("estados pendientes: %w", err)
		}
		for _, p := range pending {
			if _, err := q.ApplyMessageStatus(ctx, store.ApplyMessageStatusParams{
				InboxID: inboxID, Wamid: &wamid, Status: p.Status, At: pgtype.Timestamptz{Time: p.OccurredAt, Valid: true},
				ErrorCode: p.ErrorCode, ErrorDetail: p.ErrorDetail, PricingCategory: p.PricingCategory, Billable: p.Billable,
			}); err != nil {
				return fmt.Errorf("aplicar estado pendiente: %w", err)
			}
		}
		if len(pending) > 0 {
			return q.DeleteUnmatchedStatuses(ctx, wamid)
		}
		return nil
	})
}

func (d *Dispatcher) fail(ctx context.Context, m store.Message, code, detail string) error {
	detail = truncateRunes(detail, maxErrorDetail)
	if err := d.q.MarkMessageFailed(ctx, store.MarkMessageFailedParams{
		ID: m.ID, OrganizationID: m.OrganizationID, ErrorCode: &code, ErrorDetail: &detail,
	}); err != nil {
		return fmt.Errorf("marcar fallido: %w", err)
	}
	return nil
}

// retry reprograma el envío con espera creciente (5 s, 10 s, 20 s...) o lo da por fallido al agotar los intentos.
func (d *Dispatcher) retry(ctx context.Context, m store.Message, code, detail string) error {
	if int(m.Attempts) >= maxAttempts {
		return d.fail(ctx, m, errCodeExhaust, "se agotaron los reintentos: "+detail)
	}
	delay := baseBackoff << (m.Attempts - 1)
	if delay > maxBackoff || delay <= 0 {
		delay = maxBackoff
	}
	detail = truncateRunes(detail, maxErrorDetail)
	if err := d.q.ScheduleMessageRetry(ctx, store.ScheduleMessageRetryParams{
		ID: m.ID, OrganizationID: m.OrganizationID, NextAttemptAt: pgtype.Timestamptz{Time: d.now().Add(delay), Valid: true},
		ErrorCode: &code, ErrorDetail: &detail,
	}); err != nil {
		return fmt.Errorf("reprogramar: %w", err)
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
