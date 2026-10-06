// Package mail es el puerto de salida del correo. El servicio no sabe si el mensaje
// sale por SMTP, por un proveedor HTTP o no sale: solo pide enviarlo.
//
// Hoy la única implementación es LogSender, que deja constancia sin enviar nada.
// (auth-domicilia/GoTrue SÍ envía correo real en staging, por SMTP de Azure Communication
// Services; ese canal es de GoTrue y el core todavía no lo usa: falta una implementación
// SMTP de este puerto.) Quien necesite que el destinatario reciba algo de verdad (el
// enlace de una invitación) debe devolvérselo también a quien lo pide, por otra vía.
package mail

import (
	"context"
	"log/slog"
)

// Message es un correo de texto plano.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender envía correos.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// LogSender no envía: registra que había un correo pendiente. Nunca escribe el
// cuerpo en el log, porque puede traer un enlace con un token.
type LogSender struct{ Log *slog.Logger }

// Send implementa Sender.
func (s LogSender) Send(ctx context.Context, m Message) error {
	s.Log.InfoContext(ctx, "correo no enviado: no hay proveedor de correo configurado", "to", m.To, "subject", m.Subject)
	return nil
}
