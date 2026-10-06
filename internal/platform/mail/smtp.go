package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// defaultTimeout acota todo el envío: conectar, negociar TLS, autenticar y entregar.
const defaultTimeout = 20 * time.Second

// ErrNoSTARTTLS: el servidor no ofrece STARTTLS. Nunca se envían credenciales en claro,
// así que en ese caso no se envía nada.
var ErrNoSTARTTLS = errors.New("mail: el servidor SMTP no ofrece STARTTLS: no se envían credenciales en claro")

// SMTPConfig es la configuración de un servidor SMTP con STARTTLS (puerto 587), como el
// de Azure Communication Services que ya usa auth-domicilia.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// From es la dirección remitente; en ACS debe ser una dirección MailFrom del dominio
	// conectado. FromName es el nombre que se muestra ("Domicilia").
	From     string
	FromName string
	// Timeout acota todo el envío; cero usa 20 s.
	Timeout time.Duration
	// TLS reemplaza la configuración de TLS por omisión (las pruebas confían en su
	// propio certificado). Nil usa la del sistema, con TLS 1.2 como mínimo.
	TLS *tls.Config
}

// SMTPSender envía por SMTP con STARTTLS y autenticación. No reutiliza conexiones: el
// volumen de correo del core (invitaciones, avisos) no lo justifica y una conexión por
// mensaje no deja estado que se pueda corromper.
type SMTPSender struct{ cfg SMTPConfig }

// NewSMTPSender valida la configuración y crea el remitente.
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("mail: SMTP necesita host, usuario y contraseña")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, errors.New("mail: el puerto SMTP debe estar entre 1 y 65535")
	}
	if a, err := netmail.ParseAddress(cfg.From); err != nil || a.Address != cfg.From {
		return nil, errors.New("mail: el remitente SMTP debe ser una dirección de correo sola")
	}
	if hasLineBreak(cfg.FromName) {
		return nil, errors.New("mail: el nombre del remitente no puede tener saltos de línea")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &SMTPSender{cfg: cfg}, nil
}

func hasLineBreak(s string) bool { return strings.ContainsAny(s, "\r\n") }

// Send implementa Sender. El error nunca incluye la contraseña.
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	body, err := s.build(m)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: conectar a %s: %w", addr, err)
	}
	// Si el contexto vence o se cancela, cerrar la conexión desbloquea cualquier lectura.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: saludo del servidor SMTP: %w", ctxErr(ctx, err))
	}
	defer func() { _ = c.Close() }()

	if err := s.negotiate(c); err != nil {
		return ctxErr(ctx, err)
	}
	if err := s.deliver(c, m.To, body); err != nil {
		return ctxErr(ctx, err)
	}
	return nil
}

// ctxErr devuelve el motivo del contexto si fue lo que cortó el envío.
func ctxErr(ctx context.Context, err error) error {
	if ce := ctx.Err(); ce != nil {
		return fmt.Errorf("mail: envío interrumpido: %w", ce)
	}
	return err
}

// negotiate pasa a TLS y autentica. Sin STARTTLS no hay credenciales.
func (s *SMTPSender) negotiate(c *smtp.Client) error {
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return ErrNoSTARTTLS
	}
	tc := s.cfg.TLS
	if tc == nil {
		tc = &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	}
	if err := c.StartTLS(tc); err != nil {
		return fmt.Errorf("mail: STARTTLS: %w", err)
	}
	auth, err := s.pickAuth(c)
	if err != nil {
		return err
	}
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("mail: autenticación SMTP rechazada: %w", err)
	}
	return nil
}

// pickAuth elige PLAIN si el servidor lo ofrece y LOGIN si no.
func (s *SMTPSender) pickAuth(c *smtp.Client) (smtp.Auth, error) {
	_, mechs := c.Extension("AUTH")
	offered := strings.Fields(strings.ToUpper(mechs))
	for _, mech := range offered {
		if mech == "PLAIN" {
			return smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host), nil
		}
	}
	for _, mech := range offered {
		if mech == "LOGIN" {
			return &loginAuth{username: s.cfg.Username, password: s.cfg.Password}, nil
		}
	}
	return nil, fmt.Errorf("mail: el servidor SMTP no ofrece PLAIN ni LOGIN (ofrece %q)", mechs)
}

func (s *SMTPSender) deliver(c *smtp.Client, to string, body []byte) error {
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("mail: remitente rechazado: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mail: destinatario rechazado: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: escribir el mensaje: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: el servidor no aceptó el mensaje: %w", err)
	}
	// El mensaje ya fue aceptado: si QUIT falla no se reporta un envío fallido.
	_ = c.Quit()
	return nil
}

// build arma el mensaje MIME. Valida contra inyección de cabeceras: un salto de línea
// en el asunto o en el destinatario permitiría añadir cabeceras (Bcc:) o cuerpo.
func (s *SMTPSender) build(m Message) ([]byte, error) {
	to, err := netmail.ParseAddress(m.To)
	if err != nil || to.Address != m.To || hasLineBreak(m.To) {
		return nil, fmt.Errorf("mail: destinatario inválido %q", m.To)
	}
	if hasLineBreak(m.Subject) {
		return nil, errors.New("mail: el asunto no puede tener saltos de línea")
	}
	from := netmail.Address{Name: s.cfg.FromName, Address: s.cfg.From}

	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("mail: generar Message-ID: %w", err)
	}
	domain := s.cfg.From[strings.LastIndex(s.cfg.From, "@")+1:]

	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=UTF-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")

	var qp strings.Builder
	w := quotedprintable.NewWriter(&qp)
	if _, err := w.Write([]byte(normalizeNewlines(m.Text))); err != nil {
		return nil, fmt.Errorf("mail: codificar el cuerpo: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("mail: codificar el cuerpo: %w", err)
	}
	b.WriteString(qp.String())
	return []byte(b.String()), nil
}

// normalizeNewlines pasa todo salto de línea a CRLF, que es lo que exige SMTP.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// loginAuth implementa AUTH LOGIN, que algunos servidores ofrecen en lugar de PLAIN.
// Solo se usa tras STARTTLS: negotiate lo garantiza.
type loginAuth struct{ username, password string }

func (a *loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.username), nil
	case "password:":
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("mail: desafío AUTH LOGIN inesperado %q", fromServer)
	}
}
