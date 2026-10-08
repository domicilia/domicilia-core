package epayco

// Checkout onpage de ePayco (checkout-v2): el SERVIDOR crea una sesión y el frontend la abre con
// https://checkout.epayco.co/checkout-v2.js (`ePayco.checkout.configure({sessionId, type:"onpage"})`).
// Documentación consultada el 2026-10-08: https://docs.epayco.com/docs/checkout-implementacion
//
//  1. POST {apify}/login con Basic auth (llave pública:llave privada) → token.
//  2. POST {apify}/payment/session/create con "Authorization: Bearer <token>" → sessionId.
//
// VERIFICAR CONTRA EL SANDBOX ANTES DE PRODUCCIÓN (mismo tipo de advertencia que el resto del
// paquete): la documentación pública no publica el esquema completo de la respuesta ni todos los
// campos de la sesión; se leen las formas más probables ({token}/{data:{token}} y
// {sessionId}/{data:{sessionId}}) y el objeto splitPayment sigue el ejemplo de la documentación.
// No se encontró cómo RESTRINGIR el medio de pago por sesión: el medio elegido viaja solo como
// extra y la discrepancia se detecta en el webhook (x_franchise) — docs/pagos.md §7.6.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/domicilia/domicilia-core/internal/payments"
)

// DefaultApifyURL es la API de ePayco que crea las sesiones del checkout.
const DefaultApifyURL = "https://apify.epayco.co"

// ErrSession es una falla al crear la sesión (red, credenciales o respuesta inesperada).
var ErrSession = errors.New("epayco: no se pudo crear la sesión del checkout")

// TestMode dice si el comercio está en modo de pruebas (el frontend lo pasa a checkout-v2.js).
func (g *Gateway) TestMode() bool { return g.cfg.TestMode }

// CreateSession implementa payments.SessionGateway.
func (g *Gateway) CreateSession(ctx context.Context, req payments.ChargeRequest) (string, error) {
	token, err := g.login(ctx)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"checkout_version": "2",
		"name":             "Domicilia",
		"description":      req.Description,
		"invoice":          req.PaymentID.String(),
		"currency":         req.Currency,
		"amount":           centsToNumber(int64(req.AmountCents)),
		"taxBase":          0,
		"tax":              0,
		"country":          "CO",
		"lang":             "ES",
		"test":             g.cfg.TestMode,
		"confirmation":     req.ConfirmationURL,
		"response":         req.ResponseURL,
		"method":           "POST",
		"extras": map[string]string{
			"extra1": req.PaymentID.String(),
			"extra2": string(req.Method),
		},
	}
	if req.CustomerEmail != "" {
		body["billing"] = map[string]string{"email": req.CustomerEmail}
	}
	if req.Split != nil && len(req.Split.Receivers) > 0 {
		receivers := make([]map[string]any, len(req.Split.Receivers))
		for i, r := range req.Split.Receivers {
			receivers[i] = map[string]any{
				"merchantId": r.MerchantID, "amount": centsToNumber(r.AmountCents),
				"taxBase": 0, "tax": 0, "fee": 0,
			}
		}
		// VERIFICAR: la documentación muestra type "percentage" con montos; aquí los montos son
		// fijos (lo que le corresponde al restaurante), así que se usa "fixed".
		body["splitPayment"] = map[string]any{"type": "fixed", "receivers": receivers}
	}
	var out struct {
		SessionID string `json:"sessionId"`
		Data      struct {
			SessionID string `json:"sessionId"`
		} `json:"data"`
	}
	if err := g.call(ctx, "/payment/session/create", "Bearer "+token, body, &out); err != nil {
		return "", err
	}
	id := out.SessionID
	if id == "" {
		id = out.Data.SessionID
	}
	if id == "" {
		return "", fmt.Errorf("%w: la respuesta no trae sessionId", ErrSession)
	}
	return id, nil
}

// login obtiene el token de la API (Basic auth con las llaves del comercio).
func (g *Gateway) login(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apifyURL()+"/login", http.NoBody)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSession, err)
	}
	req.SetBasicAuth(g.cfg.PublicKey, g.cfg.PrivateKey)
	var out struct {
		Token string `json:"token"`
		Data  struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := g.do(req, &out); err != nil {
		return "", err
	}
	token := out.Token
	if token == "" {
		token = out.Data.Token
	}
	if token == "" {
		return "", fmt.Errorf("%w: el login no devolvió token", ErrSession)
	}
	return token, nil
}

func (g *Gateway) call(ctx context.Context, path, auth string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSession, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apifyURL()+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSession, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	return g.do(req, out)
}

func (g *Gateway) do(req *http.Request, out any) error {
	client := g.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSession, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSession, err)
	}
	if res.StatusCode >= 300 {
		// Nunca se registra el cuerpo de la petición (lleva datos del cliente); de la respuesta,
		// solo un recorte para diagnosticar.
		return fmt.Errorf("%w: HTTP %d: %s", ErrSession, res.StatusCode, truncate(string(raw), 200))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: respuesta no es JSON: %w", ErrSession, err)
	}
	return nil
}

func (g *Gateway) apifyURL() string {
	if g.cfg.ApifyURL != "" {
		return strings.TrimRight(g.cfg.ApifyURL, "/")
	}
	return DefaultApifyURL
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// centsToNumber pasa centavos a pesos como número JSON (ePayco espera el monto en pesos).
func centsToNumber(cents int64) json.Number {
	if cents%100 == 0 {
		return json.Number(fmt.Sprintf("%d", cents/100))
	}
	return json.Number(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}
