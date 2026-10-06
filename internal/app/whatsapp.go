package app

import (
	"context"

	"github.com/domicilia/domicilia-core/internal/inboxes"
	"github.com/domicilia/domicilia-core/internal/meta"
	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/platform/secretbox"
	"github.com/domicilia/domicilia-core/internal/tenant"
	"github.com/domicilia/domicilia-core/internal/whatsapp"
)

// Workers son los trabajadores en segundo plano. main los arranca con Run; las pruebas los usan sin
// arrancarlos, llamando a RunOnce.
type Workers struct {
	WhatsApp *whatsapp.Dispatcher
}

// Run ejecuta los trabajadores hasta que se cancele el contexto.
func (w *Workers) Run(ctx context.Context) { w.WhatsApp.Run(ctx) }

// NewWorkers arma los trabajadores. Se le pasa a New (Deps.Workers) para que las rutas puedan despertarlos
// cuando hay trabajo nuevo.
func NewWorkers(d Deps) *Workers {
	p := newWhatsAppParts(d)
	return &Workers{WhatsApp: whatsapp.NewDispatcher(d.Pool, p.inboxes, p.meta, d.Log, d.Now)}
}

// whatsappParts son las piezas de WhatsApp que comparten las rutas y los trabajadores.
type whatsappParts struct {
	inboxes *inboxes.Service
	meta    *meta.Client
	gate    *tenant.Gate
}

func newWhatsAppParts(d Deps) whatsappParts {
	cfg := d.Config
	var box *secretbox.Box
	if cfg.SecretsKey != "" {
		key, err := secretbox.ParseKey(cfg.SecretsKey)
		if err == nil {
			box, err = secretbox.New(key)
		}
		if err != nil { // config.Load ya lo valida: aquí solo llegan pruebas mal armadas
			d.Log.Error("CORE_SECRETS_KEY inválida: no se podrá conectar WhatsApp", "error", err)
			box = nil
		}
	}
	client := meta.New(meta.Config{BaseURL: cfg.WhatsAppAPIBase, Version: cfg.WhatsAppAPIVersion})
	gate := tenant.NewGate(tenant.NewLoader(d.Pool))
	plansSvc := plans.NewService(plans.NewRepository(d.Pool))
	return whatsappParts{
		inboxes: inboxes.NewService(inboxes.NewRepository(d.Pool), gate, client, box, plansSvc, d.Log),
		meta:    client,
		gate:    gate,
	}
}
