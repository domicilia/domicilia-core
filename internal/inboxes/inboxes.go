// Package inboxes son las bandejas de una organización y su canal de WhatsApp: qué número
// está conectado, con qué cuenta de Meta, y en qué estado. También guarda —cifrado— el token
// con el que el core habla con Meta en nombre de la organización.
//
// El token NUNCA sale de este paquete en claro hacia la API: ni en respuestas, ni en
// auditoría, ni en logs. Solo el envío de mensajes lo descifra, para usarlo.
package inboxes

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound  = errors.New("inboxes: no encontrada")
	ErrNameTaken = errors.New("inboxes: nombre repetido")
	// ErrPhoneTaken: ese número ya está conectado a otra bandeja (de cualquier organización).
	ErrPhoneTaken = errors.New("inboxes: número ya conectado")
	// ErrWABATaken: esa cuenta de WhatsApp Business ya está conectada (de cualquier organización).
	ErrWABATaken = errors.New("inboxes: cuenta ya conectada")
)

// Estados del canal.
const (
	ChannelConnected   = "connected"
	ChannelNeedsReauth = "needs_reauth"
)

// Inbox es una bandeja. WhatsApp es nil si la bandeja está archivada (el número se libera al
// desconectarla).
type Inbox struct {
	ID             uuid.UUID        `json:"id"`
	OrganizationID uuid.UUID        `json:"organization_id"`
	Name           string           `json:"name"`
	ChannelType    string           `json:"channel_type"`
	ArchivedAt     *time.Time       `json:"archived_at"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	WhatsApp       *WhatsAppChannel `json:"whatsapp"`
}

// WhatsAppChannel es el número conectado. No trae secretos.
type WhatsAppChannel struct {
	PhoneNumberID string `json:"phone_number_id"`
	DisplayPhone  string `json:"display_phone"`
	VerifiedName  string `json:"verified_name"`
	// QualityRating (GREEN, YELLOW, RED) y MessagingTier (TIER_250, TIER_1K...) son los de Meta.
	QualityRating string `json:"quality_rating"`
	MessagingTier string `json:"messaging_tier"`
	// Status: connected o needs_reauth (Meta rechazó el token: hay que cargar uno nuevo).
	Status        string     `json:"status"`
	WABAID        string     `json:"waba_id"`
	TokenStatus   string     `json:"token_status"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
}

// Credentials es lo necesario para hablar con Meta por una bandeja. Uso interno: el token va
// en claro en memoria y no tiene etiquetas JSON, para que no se serialice por accidente.
type Credentials struct {
	InboxID        uuid.UUID
	OrganizationID uuid.UUID
	AccountID      uuid.UUID
	PhoneNumberID  string
	WABAID         string
	AccessToken    string
	ChannelStatus  string
}

// Límites de los campos.
const (
	maxName        = 100
	maxTokenLength = 1024
)
