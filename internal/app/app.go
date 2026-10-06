// Package app arma la aplicación completa: servicios, handlers y rutas. main y
// las pruebas de contrato lo usan por igual, así lo que se prueba es lo que corre.
package app

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/catalog"
	"github.com/domicilia/domicilia-core/internal/contacts"
	"github.com/domicilia/domicilia-core/internal/conversations"
	"github.com/domicilia/domicilia-core/internal/customers"
	"github.com/domicilia/domicilia-core/internal/drivers"
	"github.com/domicilia/domicilia-core/internal/epayco"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/inboxes"
	"github.com/domicilia/domicilia-core/internal/orders"
	"github.com/domicilia/domicilia-core/internal/organizations"
	"github.com/domicilia/domicilia-core/internal/payments"
	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/platform/auth"
	"github.com/domicilia/domicilia-core/internal/platform/azblob"
	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
	"github.com/domicilia/domicilia-core/internal/platform/mail"
	"github.com/domicilia/domicilia-core/internal/promotions"
	"github.com/domicilia/domicilia-core/internal/roles"
	"github.com/domicilia/domicilia-core/internal/saas"
	"github.com/domicilia/domicilia-core/internal/tenant"
	"github.com/domicilia/domicilia-core/internal/users"
	"github.com/domicilia/domicilia-core/internal/whatsapp"
)

// IdentityProvider crea y borra cuentas en auth-domicilia (GoTrue). En
// producción es *gotrue.Client; las pruebas ponen uno propio.
type IdentityProvider interface {
	organizations.IdentityProvider
	drivers.IdentityProvider
}

// Deps son las dependencias externas de la aplicación.
type Deps struct {
	Log      *slog.Logger
	Config   config.Config
	Pool     *pgxpool.Pool
	Identity IdentityProvider
	// Mailer envía los correos. nil usa mail.LogSender: hoy no hay proveedor real.
	Mailer mail.Sender
	// Now es el reloj. nil usa time.Now; las pruebas lo fijan.
	Now func() time.Time
	// Workers son los trabajadores en segundo plano (NewWorkers). nil: las rutas que encolan trabajo no
	// despiertan a nadie y el trabajador lo encuentra en su siguiente vuelta.
	Workers *Workers
	// MediaUploader sube fotos de producto. nil construye el real (azblob.Client) a partir de
	// Config.MediaStorageAccount — o deja el catálogo sin subida (503) si no hay cuenta
	// configurada. Las pruebas pueden fijar uno falso para probar el flujo completo sin IMDS
	// (que solo responde dentro de una VM de Azure).
	MediaUploader catalog.MediaUploader
}

// New devuelve el servidor Echo con todas las rutas.
//
// Hay tres niveles de acceso bajo /v1, de menos a más exigente:
//   - público: la postulación de domiciliarios (quien se postula aún no tiene
//     cuenta), la cara pública de un negocio por slug y la vista previa de una
//     invitación (el token es la credencial);
//   - autenticado: JWT válido de GoTrue, aunque todavía no exista perfil (el alta
//     de perfil y aceptar una invitación son justo lo que lo crea);
//   - de negocio: además, un usuario activo en public.users. Es el nivel por
//     omisión: una ruta nueva debe justificar bajar de él.
//
// Dentro del nivel de negocio, cada ruta exige además el permiso que le
// corresponde (identity.Require para los de plataforma; los de organización los
// comprueba el servicio, porque dependen de la organización).
func New(d Deps) *echo.Echo {
	cfg := d.Config
	e := httpserver.New(httpserver.Options{
		Logger:         d.Log,
		CORSOrigins:    cfg.CORSOrigins,
		RequestTimeout: cfg.RequestTimeout,
		MaxBodyBytes:   cfg.MaxBodyBytes,
		Ready:          d.Pool.Ping,
	})

	mailer := d.Mailer
	if mailer == nil {
		mailer = mail.LogSender{Log: d.Log}
	}

	// roles, plans y customers son hojas: no dependen de ningún otro dominio.
	rolesSvc := roles.NewService(roles.NewRepository(d.Pool))
	plansSvc := plans.NewService(plans.NewRepository(d.Pool))
	customersSvc := customers.NewService(customers.NewRepository(d.Pool))

	usersSvc := users.NewService(users.NewRepository(d.Pool), customersSvc, rolesSvc)
	orgsSvc := organizations.NewService(organizations.NewRepository(d.Pool), d.Identity, rolesSvc, plansSvc, mailer, d.Log,
		organizations.Options{AppURL: cfg.AppURL, Now: d.Now})
	driversSvc := drivers.NewService(drivers.NewRepository(d.Pool), d.Identity, d.Log)
	saasSvc := saas.NewService(orgsSvc, plansSvc, usersSvc)

	wa := newWhatsAppParts(d)
	contactsSvc := contacts.NewService(contacts.NewRepository(d.Pool), wa.gate)
	var notify conversations.Notifier
	if d.Workers != nil {
		notify = d.Workers.WhatsApp
	}
	conversationsSvc := conversations.NewService(conversations.NewRepository(d.Pool), wa.gate, plansSvc, notify, d.Now)
	// Puerta propia, no wa.gate: el catálogo (y pedidos, más abajo) no tienen nada que ver con
	// WhatsApp, solo comparten el mismo tipo de puerta (sin estado propio: solo envuelve un
	// Loader). Un solo repositorio de catálogo: lo usa su propio servicio Y pedidos, que lo
	// necesita para armar el snapshot de una línea (ver orders.CatalogReader).
	catalogGate := tenant.NewGate(tenant.NewLoader(d.Pool))
	catalogRepo := catalog.NewRepository(d.Pool)
	// mediaUploader nil es válido: significa "sin almacenamiento de fotos" — subir una responde
	// 503, mismo criterio que la pasarela de pago sin configurar.
	mediaUploader := d.MediaUploader
	if mediaUploader == nil && cfg.MediaStorageAccount != "" {
		mediaUploader = azblob.New(cfg.MediaStorageAccount, cfg.MediaContainer)
	}
	catalogSvc := catalog.NewService(catalogRepo, catalogGate, mediaUploader)
	ordersSvc := orders.NewService(orders.NewRepository(d.Pool), catalogRepo, catalogGate)
	ingest := whatsapp.NewIngest(d.Pool, wa.inboxes, whatsapp.IngestConfig{
		AppSecret: cfg.WhatsAppAppSecret, VerifyToken: cfg.WhatsAppVerifyToken,
	}, d.Log, d.Now)
	// El webhook lo llama Meta, no un usuario: cuelga de la raíz y se autentica con firma.
	whatsapp.NewHandler(ingest).Register(e)

	// gateway nil es válido: sin CORE_EPAYCO_* configuradas, iniciar un pago responde 503 (ver
	// payments.Service.Initiate) en vez de fallar al arrancar — mismo criterio que WhatsApp sin
	// CORE_SECRETS_KEY.
	var gateway payments.Gateway
	if cfg.EpaycoPublicKey != "" {
		gateway = epayco.New(epayco.Config{
			PublicKey: cfg.EpaycoPublicKey, PrivateKey: cfg.EpaycoPrivateKey,
			CustomerID: cfg.EpaycoCustomerID, TestMode: cfg.EpaycoTestMode,
		})
	}
	paymentsSvc := payments.NewService(payments.NewRepository(d.Pool), ordersSvc, catalogGate, gateway, cfg.PublicURL, cfg.AppURL)
	// El webhook lo llama la pasarela, no un usuario: cuelga de la raíz y se autentica con su
	// propia firma, igual que WhatsApp.
	payments.NewWebhookHandler(paymentsSvc).Register(e)
	promotionsSvc := promotions.NewService(promotions.NewRepository(d.Pool), ordersSvc, catalogGate)

	public := httpserver.V1(e, cfg.RequestTimeout)
	authed := public.Group("", auth.Middleware(auth.NewVerifier(cfg.JWTSecret, cfg.JWTAudience)))
	business := authed.Group("", identity.RequireUser(identity.NewPGStore(d.Pool)))

	drivers.NewHandler(driversSvc).RegisterPublic(public)
	organizations.NewHandler(orgsSvc).RegisterPublic(public)
	catalog.NewHandler(catalogSvc).RegisterPublic(public)

	authed.GET("/whoami", auth.Whoami)
	users.NewHandler(usersSvc).RegisterProvision(authed)
	organizations.NewHandler(orgsSvc).RegisterAuthenticated(authed)

	users.NewHandler(usersSvc).Register(business)
	customers.NewHandler(customersSvc).Register(business)
	organizations.NewHandler(orgsSvc).Register(business)
	plans.NewHandler(plansSvc).Register(business)
	roles.NewHandler(rolesSvc).Register(business)
	drivers.NewHandler(driversSvc).Register(business)
	saas.NewHandler(saasSvc).Register(business)
	inboxes.NewHandler(wa.inboxes).Register(business)
	contacts.NewHandler(contactsSvc).Register(business)
	conversations.NewHandler(conversationsSvc).Register(business)
	catalog.NewHandler(catalogSvc).Register(business)
	orders.NewHandler(ordersSvc).Register(business)
	payments.NewHandler(paymentsSvc).Register(business)
	promotions.NewHandler(promotionsSvc).Register(business)

	return e
}
