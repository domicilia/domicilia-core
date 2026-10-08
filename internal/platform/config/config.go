// Package config carga y valida la configuración desde variables de entorno.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/domicilia/domicilia-core/internal/platform/secretbox"
)

const (
	minJWTSecretLen   = 32
	defaultDevAuthURL = "http://localhost:9999"
	defaultDevAppURL  = "http://localhost:3000"
)

// Config es la configuración completa del servicio. Falla al arrancar, no en
// la primera petición: un secreto faltante nunca debe descubrirse en runtime.
//
// Los nombres CORE_* son a propósito: el .env de cada entorno lo comparten
// varios servicios (fetch-secrets.sh) y ya trae DATABASE_URL (la de GoTrue,
// con otro usuario) y PORT=9999. Reusarlos apuntaría este servicio a la base
// equivocada.
type Config struct {
	Env         string `env:"APP_ENV" envDefault:"development"`
	HTTPAddr    string `env:"CORE_HTTP_ADDR" envDefault:":8080"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`
	DatabaseURL string `env:"CORE_DATABASE_URL,required,notEmpty"`
	DBMaxConns  int32  `env:"CORE_DB_MAX_CONNS" envDefault:"10"`

	// Mismo secreto HS256 con el que auth-domicilia (GoTrue) firma los JWT.
	JWTSecret   string `env:"GOTRUE_JWT_SECRET,required,notEmpty"`
	JWTAudience string `env:"GOTRUE_JWT_AUD" envDefault:"authenticated"`

	// Dirección interna de auth-domicilia (admin API), para crear la identidad de
	// quien entra por invitación o aprobación. En staging/producción es
	// obligatoria: un localhost por omisión apuntaría al propio contenedor.
	AuthURL string `env:"CORE_AUTH_URL"`

	// Dirección pública del frontend, para armar los enlaces que se mandan a las
	// personas (invitaciones). Si falta, usa el primer origen de CORS_ORIGINS.
	AppURL string `env:"CORE_APP_URL"`

	// Dirección pública de ESTE servicio (no la del frontend, que es AppURL) — para darle a la
	// pasarela de pago una URL de confirmación a la que pueda llamarnos. Obligatoria si hay una
	// pasarela configurada (CORE_EPAYCO_*).
	PublicURL string `env:"CORE_PUBLIC_URL"`

	// Correo saliente del core (invitaciones, avisos). Sin CORE_SMTP_HOST no se envía
	// nada: solo se registra que había un correo pendiente. Con host, todo lo demás
	// es obligatorio. Mismo servidor y credenciales que usa auth-domicilia.
	SMTPHost     string `env:"CORE_SMTP_HOST"`
	SMTPPort     int    `env:"CORE_SMTP_PORT" envDefault:"587"`
	SMTPUser     string `env:"CORE_SMTP_USER"`
	SMTPPass     string `env:"CORE_SMTP_PASS"`
	SMTPFrom     string `env:"CORE_SMTP_FROM"`
	SMTPFromName string `env:"CORE_SMTP_FROM_NAME" envDefault:"Domicilia"`

	// WhatsApp. Sin CORE_SECRETS_KEY no se puede conectar ningún número (el token de Meta se guarda cifrado, nunca
	// en claro): conectar responde 503. La llave son 32 bytes en base64 (`openssl rand -base64 32`).
	SecretsKey string `env:"CORE_SECRETS_KEY"`
	// Secretos de la app de Meta de la plataforma: firman los webhooks (APP_SECRET) y responden el desafío de
	// suscripción (VERIFY_TOKEN). Sin ellos el webhook no acepta eventos de la app de la plataforma.
	WhatsAppAppSecret   string `env:"CORE_WHATSAPP_APP_SECRET"`
	WhatsAppVerifyToken string `env:"CORE_WHATSAPP_VERIFY_TOKEN"`
	WhatsAppAPIBase     string `env:"CORE_WHATSAPP_API_BASE" envDefault:"https://graph.facebook.com"`
	WhatsAppAPIVersion  string `env:"CORE_WHATSAPP_API_VERSION" envDefault:"v23.0"`

	// ePayco (pasarela de pago, modo B: el cliente paga y ePayco liquida a Domicilia — ver
	// docs/ecommerce.md §0/§3). Sin CORE_EPAYCO_PRIVATE_KEY no hay pasarela: iniciar un pago
	// responde 503, igual que WhatsApp sin CORE_SECRETS_KEY. Con ella, el resto es obligatorio.
	EpaycoPublicKey  string `env:"CORE_EPAYCO_PUBLIC_KEY"`
	EpaycoPrivateKey string `env:"CORE_EPAYCO_PRIVATE_KEY"`
	EpaycoCustomerID string `env:"CORE_EPAYCO_CUSTOMER_ID"`
	EpaycoTestMode   bool   `env:"CORE_EPAYCO_TEST_MODE" envDefault:"true"`
	// API que crea las sesiones del checkout onpage (checkout-v2). Configurable para pruebas.
	EpaycoApifyURL string `env:"CORE_EPAYCO_APIFY_URL" envDefault:"https://apify.epayco.co"`

	// Fotos de producto en Azure Blob Storage (ver domicilia-infra/modules/storage). El VM sube
	// con su identidad administrada (IMDS) — ver internal/platform/azblob — sin ninguna
	// credencial en disco. Sin CORE_MEDIA_STORAGE_ACCOUNT, subir una foto responde 503, igual
	// que WhatsApp sin CORE_SECRETS_KEY o los pagos sin CORE_EPAYCO_*.
	MediaStorageAccount string `env:"CORE_MEDIA_STORAGE_ACCOUNT"`
	MediaContainer      string `env:"CORE_MEDIA_CONTAINER" envDefault:"product-media"`

	CORSOrigins     []string      `env:"CORS_ORIGINS" envSeparator:","`
	RequestTimeout  time.Duration `env:"CORE_REQUEST_TIMEOUT" envDefault:"15s"`
	ShutdownTimeout time.Duration `env:"CORE_SHUTDOWN_TIMEOUT" envDefault:"15s"`
	// 8 MiB: el único cliente que manda cuerpos grandes es la subida de fotos de producto (hasta
	// 5 MiB, ver internal/catalog), con margen para el resto de la petición multipart.
	MaxBodyBytes int64 `env:"CORE_MAX_BODY_BYTES" envDefault:"8388608"`
}

// Database es lo mínimo para operar sobre la base (migraciones): esos comandos
// no necesitan el secreto JWT ni el resto de la configuración del servidor.
type Database struct {
	Env string `env:"APP_ENV" envDefault:"development"`
	URL string `env:"CORE_DATABASE_URL,required,notEmpty"`
}

// IsProduction es true en staging y producción.
func (d Database) IsProduction() bool {
	return d.Env == "production" || d.Env == "staging"
}

// LoadDatabase lee solo la configuración de base de datos.
func LoadDatabase() (Database, error) {
	d, err := env.ParseAs[Database]()
	if err != nil {
		return Database{}, fmt.Errorf("config: %w", err)
	}
	return d, nil
}

// IsProduction es true en staging y producción: ahí las validaciones son estrictas.
func (c Config) IsProduction() bool {
	return c.Env == "production" || c.Env == "staging"
}

// Load lee el entorno y valida. Devuelve todos los problemas juntos.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if cfg.AuthURL == "" && !cfg.IsProduction() {
		cfg.AuthURL = defaultDevAuthURL
	}
	if cfg.AppURL == "" {
		cfg.AppURL = defaultAppURL(cfg)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// defaultAppURL deduce la URL del frontend: el primer origen concreto de CORS y, en
// desarrollo, el puerto habitual de Next.js. En staging/producción sin ninguno de los
// dos devuelve "" y la validación lo rechaza: un enlace de invitación a localhost
// llegaría roto.
func defaultAppURL(c Config) string {
	for _, o := range c.CORSOrigins {
		if o != "" && o != "*" {
			return o
		}
	}
	if !c.IsProduction() {
		return defaultDevAppURL
	}
	return ""
}

func (c Config) validate() error {
	var errs []error

	if len(c.JWTSecret) < minJWTSecretLen {
		errs = append(errs, fmt.Errorf("GOTRUE_JWT_SECRET debe tener al menos %d caracteres", minJWTSecretLen))
	}
	if u, err := url.Parse(c.AuthURL); c.AuthURL == "" || err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, errors.New("CORE_AUTH_URL debe ser una URL completa (p. ej. http://auth:9999)"))
	}
	if u, err := url.Parse(c.AppURL); c.AppURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, errors.New("CORE_APP_URL debe ser la URL completa del frontend (p. ej. https://app.domicilia.co)"))
	}
	if c.SecretsKey != "" {
		if _, err := secretbox.ParseKey(c.SecretsKey); err != nil {
			errs = append(errs, fmt.Errorf("CORE_SECRETS_KEY: %w", err))
		}
	}
	insecureOK := !c.IsProduction() // http solo se admite fuera de staging/producción (las pruebas apuntan a un servidor local)
	if u, err := url.Parse(c.WhatsAppAPIBase); err != nil || u.Host == "" || (u.Scheme != "https" && (u.Scheme != "http" || !insecureOK)) {
		errs = append(errs, errors.New("CORE_WHATSAPP_API_BASE debe ser una URL https (http solo fuera de staging/producción)"))
	}
	if c.SMTPHost != "" {
		if c.SMTPUser == "" || c.SMTPPass == "" || c.SMTPFrom == "" {
			errs = append(errs, errors.New("con CORE_SMTP_HOST, también son obligatorias CORE_SMTP_USER, CORE_SMTP_PASS y CORE_SMTP_FROM"))
		}
		if c.SMTPPort < 1 || c.SMTPPort > 65535 {
			errs = append(errs, errors.New("CORE_SMTP_PORT debe estar entre 1 y 65535"))
		}
	}
	if c.EpaycoPublicKey != "" {
		if c.EpaycoPrivateKey == "" || c.EpaycoCustomerID == "" {
			errs = append(errs, errors.New("con CORE_EPAYCO_PUBLIC_KEY, también son obligatorias CORE_EPAYCO_PRIVATE_KEY y CORE_EPAYCO_CUSTOMER_ID"))
		}
		if u, err := url.Parse(c.PublicURL); c.PublicURL == "" || err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, errors.New("con una pasarela de pago configurada, CORE_PUBLIC_URL es obligatoria (la URL pública de este servicio)"))
		}
	}
	if c.DBMaxConns < 1 {
		errs = append(errs, errors.New("CORE_DB_MAX_CONNS debe ser >= 1"))
	}
	if c.MaxBodyBytes < 1 {
		errs = append(errs, errors.New("CORE_MAX_BODY_BYTES debe ser >= 1"))
	}
	if c.IsProduction() && slices.Contains(c.CORSOrigins, "*") {
		errs = append(errs, errors.New(`CORS_ORIGINS no puede ser "*" en staging/producción`))
	}

	return errors.Join(errs...)
}
