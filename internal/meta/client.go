// Package meta es el cliente de la Graph API de Meta (WhatsApp Cloud API) y el análisis
// de sus webhooks. No conoce organizaciones ni la base de datos: recibe el token en cada
// llamada, así quien lo usa decide de dónde sale y cómo se guarda.
//
// El token viaja solo en la cabecera Authorization: nunca en la URL, para que no acabe
// en un log de acceso ni en un mensaje de error.
package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL y DefaultVersion son los de la Graph API pública.
	DefaultBaseURL = "https://graph.facebook.com"
	DefaultVersion = "v23.0"

	defaultTimeout = 15 * time.Second
	maxResponse    = 1 << 20 // 1 MiB: una respuesta más grande no es de Meta
	maxPages       = 10
)

// Códigos de error de Meta que cambian qué hacer con un envío fallido.
// https://developers.facebook.com/docs/whatsapp/cloud-api/support/error-codes
const (
	CodeTokenInvalid     = 190    // token vencido, revocado o inválido
	CodeRateLimit        = 4      // límite de llamadas de la app
	CodePairRateLimit    = 131056 // demasiados mensajes a la misma persona
	CodeThroughput       = 130429 // se superó el ritmo del número
	CodeWindowClosed     = 131047 // pasaron más de 24 h: solo plantillas
	CodeNotOnWhatsApp    = 131026 // el destinatario no puede recibir el mensaje
	CodeTemporaryFailure = 131000 // fallo interno de Meta, reintentar
	CodeServiceUnavail   = 131016
)

// Client habla con la Graph API.
type Client struct {
	baseURL string
	version string
	http    *http.Client
}

// Config configura el cliente. Los campos vacíos toman los valores públicos.
type Config struct {
	BaseURL string
	Version string
	HTTP    *http.Client
}

// New crea el cliente.
func New(cfg Config) *Client {
	c := &Client{baseURL: strings.TrimRight(cfg.BaseURL, "/"), version: cfg.Version, http: cfg.HTTP}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.version == "" {
		c.version = DefaultVersion
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: defaultTimeout}
	}
	return c
}

// APIError es un error que devolvió Meta. Message es de Meta y puede mostrarse; el token
// nunca forma parte de él.
type APIError struct {
	HTTPStatus int
	Code       int
	Subcode    int
	Message    string
	TraceID    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("meta: HTTP %d, código %d: %s", e.HTTPStatus, e.Code, e.Message)
}

// AuthFailed dice si el token dejó de servir: hay que pedir uno nuevo.
func (e *APIError) AuthFailed() bool {
	return e.Code == CodeTokenInvalid || e.HTTPStatus == http.StatusUnauthorized
}

// Retryable dice si vale la pena reintentar el mismo envío más tarde: límites de ritmo,
// caídas de Meta y errores de servidor. Un rechazo por contenido, ventana o destinatario
// no mejora con el tiempo.
func (e *APIError) Retryable() bool {
	switch e.Code {
	case CodeRateLimit, CodeThroughput, CodePairRateLimit, CodeTemporaryFailure, CodeServiceUnavail:
		return true
	}
	return e.HTTPStatus == http.StatusTooManyRequests || e.HTTPStatus >= http.StatusInternalServerError
}

// WindowClosed dice si Meta rechazó el mensaje por estar fuera de la ventana de 24 h.
func (e *APIError) WindowClosed() bool { return e.Code == CodeWindowClosed }

// TransportError es un fallo antes de obtener respuesta (red, tiempo agotado): el envío
// puede o no haber llegado a Meta, así que siempre es reintentable.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "meta: sin respuesta: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// NeverSent dice si el fallo ocurrió ANTES de que la petición saliera (no se pudo conectar): reintentar es
// seguro. Cualquier otro fallo de red (tiempo agotado esperando la respuesta) es ambiguo: Meta pudo haber
// aceptado el mensaje, y reintentarlo lo duplicaría ante el cliente.
func (e *TransportError) NeverSent() bool {
	var op *net.OpError
	if errors.As(e.Err, &op) && op.Op == "dial" {
		return true
	}
	var dns *net.DNSError
	return errors.As(e.Err, &dns)
}

// PhoneNumber es un número de una cuenta de WhatsApp Business.
type PhoneNumber struct {
	ID               string `json:"id"`
	DisplayPhone     string `json:"display_phone_number"`
	VerifiedName     string `json:"verified_name"`
	QualityRating    string `json:"quality_rating"`
	MessagingTier    string `json:"messaging_limit_tier"`
	CodeVerification string `json:"code_verification_status"`
}

// ListPhoneNumbers devuelve los números de una cuenta. También sirve para validar un token:
// si Meta lo rechaza o no tiene acceso a la cuenta, devuelve un *APIError.
func (c *Client) ListPhoneNumbers(ctx context.Context, token, wabaID string) ([]PhoneNumber, error) {
	q := url.Values{}
	q.Set("fields", "id,display_phone_number,verified_name,quality_rating,messaging_limit_tier,code_verification_status")
	q.Set("limit", "100")
	var out []PhoneNumber
	for range maxPages {
		var page struct {
			Data   []PhoneNumber `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, url.PathEscape(wabaID)+"/phone_numbers?"+q.Encode(), token, nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Data...)
		if page.Paging.Next == "" || page.Paging.Cursors.After == "" {
			return out, nil
		}
		q.Set("after", page.Paging.Cursors.After)
	}
	return out, nil
}

// TextMessage es un mensaje de texto de servicio (dentro de la ventana de 24 h).
type TextMessage struct {
	To        string // número internacional sin "+"
	Body      string
	ReplyToID string // wamid al que responde, opcional
}

// SendText envía un texto y devuelve el id que Meta le dio (wamid).
func (c *Client) SendText(ctx context.Context, token, phoneNumberID string, m TextMessage) (string, error) {
	body := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                m.To,
		"type":              "text",
		"text":              map[string]any{"body": m.Body, "preview_url": false},
	}
	if m.ReplyToID != "" {
		body["context"] = map[string]string{"message_id": m.ReplyToID}
	}
	var resp struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := c.do(ctx, http.MethodPost, url.PathEscape(phoneNumberID)+"/messages", token, body, &resp); err != nil {
		return "", err
	}
	if len(resp.Messages) == 0 || resp.Messages[0].ID == "" {
		return "", errors.New("meta: la respuesta no trae el id del mensaje")
	}
	return resp.Messages[0].ID, nil
}

func (c *Client) do(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("meta: codificar la petición: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/"+c.version+"/"+path, body)
	if err != nil {
		return fmt.Errorf("meta: armar la petición: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error incluye la URL: no lleva el token (va en la cabecera), pero se
		// reduce al motivo para no arrastrar nada más.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &TransportError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return &TransportError{Err: err}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("meta: respuesta ilegible: %w", err)
	}
	return nil
}

func parseAPIError(status int, raw []byte) *APIError {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
			Subcode int    `json:"error_subcode"`
			TraceID string `json:"fbtrace_id"`
			Data    struct {
				Details string `json:"details"`
			} `json:"error_data"`
		} `json:"error"`
	}
	ae := &APIError{HTTPStatus: status, Message: http.StatusText(status)}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		ae.Code, ae.Subcode, ae.TraceID = e.Error.Code, e.Error.Subcode, e.Error.TraceID
		ae.Message = e.Error.Message
		if e.Error.Data.Details != "" {
			ae.Message += ": " + e.Error.Data.Details
		}
	}
	return ae
}
