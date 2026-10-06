package main

import (
	"fmt"
	"log/slog"

	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/mail"
)

// newMailer elige cómo sale el correo: por SMTP si hay CORE_SMTP_HOST y, si no, solo se
// registra que había uno pendiente (mail.LogSender). Dice cuál eligió al arrancar, para
// que un despliegue sin correo no se descubra cuando alguien no recibe una invitación.
func newMailer(cfg config.Config, log *slog.Logger) (mail.Sender, error) {
	if cfg.SMTPHost == "" {
		log.Info("correo saliente: sin SMTP configurado; los correos solo se registran en el log")
		return mail.LogSender{Log: log}, nil
	}
	s, err := mail.NewSMTPSender(mail.SMTPConfig{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUser, Password: cfg.SMTPPass,
		From: cfg.SMTPFrom, FromName: cfg.SMTPFromName,
	})
	if err != nil {
		return nil, fmt.Errorf("correo saliente: %w", err)
	}
	log.Info("correo saliente por SMTP", "host", cfg.SMTPHost, "port", cfg.SMTPPort, "from", cfg.SMTPFrom)
	return s, nil
}
