package meta

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// VerifySignature comprueba la cabecera X-Hub-Signature-256 ("sha256=<hex>") contra el
// cuerpo EXACTO recibido, con el secreto de la app. La comparación es de tiempo constante.
// Un secreto vacío nunca valida: sin secreto no hay forma de saber que el mensaje es de Meta.
func VerifySignature(secret string, body []byte, header string) bool {
	if secret == "" {
		return false
	}
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// Sign calcula la cabecera para un cuerpo. La usan las pruebas y las herramientas de
// diagnóstico; Meta la calcula por su lado.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Webhook es la carga de un webhook de WhatsApp Business.
type Webhook struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

// Entry es una cuenta (WABA) con sus cambios.
type Entry struct {
	ID      string   `json:"id"` // id de la cuenta de WhatsApp Business
	Changes []Change `json:"changes"`
}

// Change es un cambio de un campo suscrito.
type Change struct {
	Field string      `json:"field"`
	Value ChangeValue `json:"value"`
}

// ChangeValue es el contenido de un cambio del campo "messages".
type ChangeValue struct {
	Metadata Metadata  `json:"metadata"`
	Contacts []Contact `json:"contacts"`
	Messages []Message `json:"messages"`
	Statuses []Status  `json:"statuses"`
}

// Metadata dice a qué número de la empresa llegó el evento.
type Metadata struct {
	DisplayPhone  string `json:"display_phone_number"`
	PhoneNumberID string `json:"phone_number_id"`
}

// Contact es el perfil de quien escribió.
type Contact struct {
	WAID    string `json:"wa_id"`
	Profile struct {
		Name string `json:"name"`
	} `json:"profile"`
}

// Message es un mensaje entrante.
type Message struct {
	From      string `json:"from"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Context   *struct {
		ID string `json:"id"`
	} `json:"context"`

	Text *struct {
		Body string `json:"body"`
	} `json:"text"`
	Image    *Media `json:"image"`
	Audio    *Media `json:"audio"`
	Video    *Media `json:"video"`
	Document *Media `json:"document"`
	Sticker  *Media `json:"sticker"`
	Location *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Name      string  `json:"name"`
		Address   string  `json:"address"`
	} `json:"location"`
	Interactive *struct {
		Type        string `json:"type"`
		ButtonReply *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"button_reply"`
		ListReply *struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"list_reply"`
	} `json:"interactive"`
	Button *struct {
		Text    string `json:"text"`
		Payload string `json:"payload"`
	} `json:"button"`
	Reaction *struct {
		MessageID string `json:"message_id"`
		Emoji     string `json:"emoji"`
	} `json:"reaction"`
	Contacts json.RawMessage `json:"contacts"`
	Errors   []EventError    `json:"errors"`
}

// Media describe un archivo entrante. La URL de descarga no viene en el webhook: se pide
// aparte con el id, y caduca.
type Media struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	SHA256   string `json:"sha256"`
	Caption  string `json:"caption"`
	Filename string `json:"filename"`
	Voice    bool   `json:"voice"`
	Animated bool   `json:"animated"`
}

// Status es el estado de un mensaje que enviamos.
type Status struct {
	ID          string       `json:"id"` // wamid del mensaje
	Status      string       `json:"status"`
	Timestamp   string       `json:"timestamp"`
	RecipientID string       `json:"recipient_id"`
	Errors      []EventError `json:"errors"`
	Pricing     *struct {
		Billable     bool   `json:"billable"`
		PricingModel string `json:"pricing_model"`
		Category     string `json:"category"`
	} `json:"pricing"`
}

// EventError es un error dentro de un evento.
type EventError struct {
	Code    int    `json:"code"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Data    struct {
		Details string `json:"details"`
	} `json:"error_data"`
}

// Detail es el texto del error para mostrar.
func (e EventError) Detail() string {
	switch {
	case e.Data.Details != "":
		return e.Data.Details
	case e.Message != "":
		return e.Message
	default:
		return e.Title
	}
}

// ParseWebhook decodifica y valida la forma mínima. Un objeto que no sea de WhatsApp
// Business se rechaza: la app puede estar suscrita a otros productos.
func ParseWebhook(body []byte) (Webhook, error) {
	var w Webhook
	if err := json.Unmarshal(body, &w); err != nil {
		return Webhook{}, fmt.Errorf("meta: webhook ilegible: %w", err)
	}
	if w.Object != "whatsapp_business_account" {
		return Webhook{}, errors.New("meta: el webhook no es de WhatsApp Business")
	}
	return w, nil
}

// Time convierte el timestamp en segundos (texto) que usa Meta. Uno ilegible devuelve el
// instante de fallback: un evento no se pierde por un timestamp raro.
func Time(ts string, fallback time.Time) time.Time {
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sec <= 0 {
		return fallback
	}
	return time.Unix(sec, 0).UTC()
}

// E164 pasa un número de Meta ("573001234567") a formato E.164 ("+573001234567").
func E164(waID string) string {
	waID = strings.TrimSpace(waID)
	if waID == "" || strings.HasPrefix(waID, "+") {
		return waID
	}
	return "+" + waID
}
