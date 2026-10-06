package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/mail"
)

func TestNewMailerSinSMTPSoloRegistra(t *testing.T) {
	var buf bytes.Buffer
	m, err := newMailer(config.Config{}, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(mail.LogSender); !ok {
		t.Fatalf("mailer = %T, quería LogSender", m)
	}
	if !strings.Contains(buf.String(), "sin SMTP") {
		t.Errorf("debía avisar al arrancar que no hay SMTP: %s", buf.String())
	}
}

func TestNewMailerConSMTPNoRegistraLaContrasena(t *testing.T) {
	var buf bytes.Buffer
	cfg := config.Config{SMTPHost: "smtp.ejemplo.com", SMTPPort: 587, SMTPUser: "u", SMTPPass: "clave-muy-secreta", SMTPFrom: "a@ejemplo.com", SMTPFromName: "Domicilia"}
	m, err := newMailer(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(*mail.SMTPSender); !ok {
		t.Fatalf("mailer = %T, quería SMTPSender", m)
	}
	if strings.Contains(buf.String(), "clave-muy-secreta") {
		t.Fatalf("el log contiene la contraseña: %s", buf.String())
	}
}

func TestNewMailerConSMTPInvalidoFalla(t *testing.T) {
	cfg := config.Config{SMTPHost: "h", SMTPPort: 587, SMTPUser: "u", SMTPPass: "p", SMTPFrom: "no-es-correo"}
	if _, err := newMailer(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err == nil {
		t.Fatal("un remitente inválido debía impedir el arranque, no descubrirse al enviar")
	}
}
