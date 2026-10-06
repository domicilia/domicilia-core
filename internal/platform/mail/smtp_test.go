package mail_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	dmail "github.com/domicilia/domicilia-core/internal/platform/mail"
)

// Un servidor SMTP mínimo pero real: habla el protocolo por un socket, negocia
// STARTTLS con un certificado propio y captura lo que recibe. Así se prueba el envío de
// verdad (TLS, autenticación, codificación), no un doble del cliente.

type smtpServer struct {
	t          *testing.T
	ln         net.Listener
	tlsCfg     *tls.Config
	rootCAs    *x509.CertPool
	starttls   bool     // ofrece STARTTLS
	auths      []string // mecanismos AUTH que ofrece tras TLS
	rejectAuth bool
	rejectRcpt bool
	slow       bool // no responde tras conectar

	mu       sync.Mutex
	sawAuth  bool // llegó un AUTH SIN TLS
	user     string
	pass     string
	from     string
	rcpts    []string
	data     string
	authMech string
}

func newCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startSMTP(t *testing.T, mutate func(*smtpServer)) *smtpServer {
	t.Helper()
	cert, pool := newCert(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpServer{
		t: t, ln: ln, rootCAs: pool, starttls: true, auths: []string{"PLAIN", "LOGIN"},
		tlsCfg: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	if mutate != nil {
		mutate(s)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *smtpServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *smtpServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if s.slow {
		time.Sleep(5 * time.Second)
		return
	}
	var rw *bufio.ReadWriter
	setup := func(c net.Conn) { rw = bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c)) }
	reply := func(line string) { _, _ = rw.WriteString(line + "\r\n"); _ = rw.Flush() }
	setup(conn)
	tlsOn := false
	reply("220 fake ESMTP")
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		switch verb {
		case "EHLO", "HELO":
			exts := []string{"250-fake"}
			if s.starttls && !tlsOn {
				exts = append(exts, "250-STARTTLS")
			}
			if tlsOn && len(s.auths) > 0 {
				exts = append(exts, "250-AUTH "+strings.Join(s.auths, " "))
			}
			exts = append(exts, "250 8BITMIME")
			for _, e := range exts {
				reply(e)
			}
		case "STARTTLS":
			reply("220 listo")
			tc := tls.Server(conn, s.tlsCfg)
			if err := tc.HandshakeContext(context.Background()); err != nil {
				return
			}
			conn = tc
			setup(tc)
			tlsOn = true
		case "AUTH":
			if !tlsOn {
				s.mu.Lock()
				s.sawAuth = true
				s.mu.Unlock()
				reply("530 use STARTTLS")
				continue
			}
			s.handleAuth(line, rw, reply)
		case "MAIL":
			s.mu.Lock()
			s.from = line
			s.mu.Unlock()
			reply("250 ok")
		case "RCPT":
			if s.rejectRcpt {
				reply("550 buzón inexistente")
				continue
			}
			s.mu.Lock()
			s.rcpts = append(s.rcpts, line)
			s.mu.Unlock()
			reply("250 ok")
		case "DATA":
			reply("354 adelante")
			var b strings.Builder
			for {
				l, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".")) // deshace el punto de relleno
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			reply("250 aceptado")
		case "QUIT":
			reply("221 adiós")
			return
		default:
			reply("500 no entiendo")
		}
	}
}

func (s *smtpServer) handleAuth(line string, rw *bufio.ReadWriter, reply func(string)) {
	fields := strings.Fields(line)
	mech := strings.ToUpper(fields[1])
	s.mu.Lock()
	s.authMech = mech
	s.mu.Unlock()
	dec := func(v string) string { b, _ := base64.StdEncoding.DecodeString(v); return string(b) }
	switch mech {
	case "PLAIN":
		parts := strings.Split(dec(fields[2]), "\x00")
		s.mu.Lock()
		s.user, s.pass = parts[1], parts[2]
		s.mu.Unlock()
	case "LOGIN":
		reply("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
		u, _ := rw.ReadString('\n')
		reply("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
		p, _ := rw.ReadString('\n')
		s.mu.Lock()
		s.user, s.pass = dec(strings.TrimSpace(u)), dec(strings.TrimSpace(p))
		s.mu.Unlock()
	default:
		reply("504 mecanismo no soportado")
		return
	}
	if s.rejectAuth {
		reply("535 5.7.3 Authentication unsuccessful")
		return
	}
	reply("235 autenticado")
}

func (s *smtpServer) sender(t *testing.T, mutate func(*dmail.SMTPConfig)) *dmail.SMTPSender {
	t.Helper()
	cfg := dmail.SMTPConfig{
		Host: "127.0.0.1", Port: s.port(), Username: "usuario", Password: "clave-secreta-123",
		From: "DoNotReply@dominio.azurecomm.net", FromName: "Domicilia",
		Timeout: 3 * time.Second, TLS: &tls.Config{RootCAs: s.rootCAs, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	snd, err := dmail.NewSMTPSender(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return snd
}

// capture es lo que el servidor recibió, sin el candado.
type capture struct {
	sawAuth          bool
	user, pass, from string
	rcpts            []string
	data, authMech   string
}

func (s *smtpServer) snapshot() capture {
	s.mu.Lock()
	defer s.mu.Unlock()
	return capture{sawAuth: s.sawAuth, user: s.user, pass: s.pass, from: s.from, rcpts: append([]string(nil), s.rcpts...), data: s.data, authMech: s.authMech}
}

func TestEnvioRealPorSMTPConSTARTTLSYPlain(t *testing.T) {
	srv := startSMTP(t, nil)
	err := srv.sender(t, nil).Send(context.Background(), dmail.Message{
		To: "persona@ejemplo.com", Subject: "Te invitaron a Café Ñandú",
		Text: "Hola\nAcepta aquí: https://app.ejemplo.com/auth/accept-invite?token=abc_DEF-123\n.línea que empieza con punto\n",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := srv.snapshot()
	if got.authMech != "PLAIN" || got.user != "usuario" || got.pass != "clave-secreta-123" {
		t.Errorf("autenticación = %s %q/%q", got.authMech, got.user, got.pass)
	}
	if !strings.Contains(got.from, "DoNotReply@dominio.azurecomm.net") || len(got.rcpts) != 1 || !strings.Contains(got.rcpts[0], "persona@ejemplo.com") {
		t.Errorf("sobre = from %q rcpt %v", got.from, got.rcpts)
	}

	msg, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatalf("el mensaje no es MIME válido: %v\n%s", err, got.data)
	}
	if from, _ := mail.ParseAddress(msg.Header.Get("From")); from == nil || from.Name != "Domicilia" || from.Address != "DoNotReply@dominio.azurecomm.net" {
		t.Errorf("From = %q", msg.Header.Get("From"))
	}
	subj, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subj != "Te invitaron a Café Ñandú" {
		t.Errorf("Subject = %q, %v", subj, err)
	}
	if !strings.HasPrefix(msg.Header.Get("Content-Type"), "text/plain; charset=UTF-8") || msg.Header.Get("Message-ID") == "" || msg.Header.Get("Date") == "" {
		t.Errorf("cabeceras = %v", msg.Header)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if !strings.Contains(string(body), "token=abc_DEF-123") || !strings.Contains(string(body), "\r\n.línea que empieza con punto") {
		t.Errorf("cuerpo = %q", body)
	}
}

func TestSeUsaLOGINSiElServidorNoOfrecePLAIN(t *testing.T) {
	srv := startSMTP(t, func(s *smtpServer) { s.auths = []string{"LOGIN"} })
	if err := srv.sender(t, nil).Send(context.Background(), dmail.Message{To: "a@ejemplo.com", Subject: "x", Text: "y"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.snapshot(); got.authMech != "LOGIN" || got.user != "usuario" || got.pass != "clave-secreta-123" {
		t.Errorf("autenticación = %s %q/%q", got.authMech, got.user, got.pass)
	}
}

func TestNuncaSeEnvianCredencialesSinSTARTTLS(t *testing.T) {
	srv := startSMTP(t, func(s *smtpServer) { s.starttls = false })
	err := srv.sender(t, nil).Send(context.Background(), dmail.Message{To: "a@ejemplo.com", Subject: "x", Text: "y"})
	if !errors.Is(err, dmail.ErrNoSTARTTLS) {
		t.Fatalf("err = %v, quería ErrNoSTARTTLS", err)
	}
	if got := srv.snapshot(); got.sawAuth || got.pass != "" || got.data != "" {
		t.Fatalf("el servidor recibió credenciales o datos sin TLS: %+v", got)
	}
}

func TestRechazaUnCertificadoQueNoEsDeConfianza(t *testing.T) {
	srv := startSMTP(t, nil)
	snd := srv.sender(t, func(c *dmail.SMTPConfig) { c.TLS = nil }) // TLS del sistema: no conoce el certificado de la prueba
	if err := snd.Send(context.Background(), dmail.Message{To: "a@ejemplo.com", Subject: "x", Text: "y"}); err == nil {
		t.Fatal("aceptó un certificado que no es de confianza: un intermediario podría leer las credenciales")
	}
	if got := srv.snapshot(); got.pass != "" {
		t.Fatal("se enviaron credenciales por un canal sin verificar")
	}
}

func TestErroresDelServidorNoFiltranLaContrasena(t *testing.T) {
	cases := map[string]func(*smtpServer){
		"autenticación rechazada": func(s *smtpServer) { s.rejectAuth = true },
		"destinatario rechazado":  func(s *smtpServer) { s.rejectRcpt = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			srv := startSMTP(t, mutate)
			err := srv.sender(t, nil).Send(context.Background(), dmail.Message{To: "a@ejemplo.com", Subject: "x", Text: "y"})
			if err == nil {
				t.Fatal("debía fallar")
			}
			if strings.Contains(err.Error(), "clave-secreta-123") {
				t.Fatalf("el error contiene la contraseña: %v", err)
			}
		})
	}
}

func TestNoInyectaCabecerasNiDestinatariosExtra(t *testing.T) {
	srv := startSMTP(t, nil)
	snd := srv.sender(t, nil)
	bad := []dmail.Message{
		{To: "a@ejemplo.com", Subject: "hola\r\nBcc: victima@ejemplo.com", Text: "x"},
		{To: "a@ejemplo.com", Subject: "hola\nBcc: victima@ejemplo.com", Text: "x"},
		{To: "a@ejemplo.com\r\nBcc: victima@ejemplo.com", Subject: "x", Text: "x"},
		{To: "Nombre <a@ejemplo.com>", Subject: "x", Text: "x"},
		{To: "a@ejemplo.com, b@ejemplo.com", Subject: "x", Text: "x"},
		{To: "", Subject: "x", Text: "x"},
	}
	for _, m := range bad {
		if err := snd.Send(context.Background(), m); err == nil {
			t.Errorf("aceptó un mensaje malicioso: %+v", m)
		}
	}
	if got := srv.snapshot(); got.data != "" || len(got.rcpts) != 0 {
		t.Fatalf("algún mensaje llegó al servidor: %+v", got)
	}
}

func TestUnServidorQueNoRespondeNoBloqueaPorSiempre(t *testing.T) {
	srv := startSMTP(t, func(s *smtpServer) { s.slow = true })
	snd := srv.sender(t, func(c *dmail.SMTPConfig) { c.Timeout = 300 * time.Millisecond })
	start := time.Now()
	err := snd.Send(context.Background(), dmail.Message{To: "a@ejemplo.com", Subject: "x", Text: "y"})
	if err == nil {
		t.Fatal("debía fallar por tiempo")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("tardó %v: el tiempo límite no se respetó", time.Since(start))
	}
}

func TestSeCancelaConElContexto(t *testing.T) {
	srv := startSMTP(t, func(s *smtpServer) { s.slow = true })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	err := srv.sender(t, func(c *dmail.SMTPConfig) { c.Timeout = 10 * time.Second }).Send(ctx, dmail.Message{To: "a@ejemplo.com", Subject: "x", Text: "y"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, quería context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("no respetó la cancelación: %v", time.Since(start))
	}
}

func TestConfiguracionInvalida(t *testing.T) {
	ok := dmail.SMTPConfig{Host: "h", Port: 587, Username: "u", Password: "p", From: "a@b.co"}
	if _, err := dmail.NewSMTPSender(ok); err != nil {
		t.Fatalf("una configuración válida falló: %v", err)
	}
	for name, mutate := range map[string]func(*dmail.SMTPConfig){
		"sin host":             func(c *dmail.SMTPConfig) { c.Host = "" },
		"sin usuario":          func(c *dmail.SMTPConfig) { c.Username = "" },
		"sin contraseña":       func(c *dmail.SMTPConfig) { c.Password = "" },
		"puerto 0":             func(c *dmail.SMTPConfig) { c.Port = 0 },
		"puerto enorme":        func(c *dmail.SMTPConfig) { c.Port = 70000 },
		"remitente inválido":   func(c *dmail.SMTPConfig) { c.From = "no-es-correo" },
		"remitente con nombre": func(c *dmail.SMTPConfig) { c.From = "Domicilia <a@b.co>" },
		"nombre con salto":     func(c *dmail.SMTPConfig) { c.FromName = "Dom\r\nBcc: x@y.co" },
	} {
		cfg := ok
		mutate(&cfg)
		if _, err := dmail.NewSMTPSender(cfg); err == nil {
			t.Errorf("%s: debía rechazarse", name)
		}
	}
}

func TestLogSenderNoEscribeElCuerpo(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	err := dmail.LogSender{Log: log}.Send(context.Background(), dmail.Message{To: "a@ejemplo.com", Subject: "Asunto", Text: "https://app/accept?token=SECRETO"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "SECRETO") || !strings.Contains(buf.String(), "a@ejemplo.com") {
		t.Fatalf("log = %s", buf.String())
	}
}
