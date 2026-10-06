package config_test

import (
	"strings"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/config"
)

const goodSecret = "0123456789abcdef0123456789abcdef" // gitleaks:allow — valor falso de prueba

func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CORE_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("GOTRUE_JWT_SECRET", goodSecret)
}

func TestLoadValoresPorDefecto(t *testing.T) {
	baseEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.JWTAudience != "authenticated" || cfg.DBMaxConns != 10 {
		t.Fatalf("defaults inesperados: %+v", cfg)
	}
	if cfg.IsProduction() {
		t.Fatal("development no es producción")
	}
}

func TestLoadFaltaObligatoria(t *testing.T) {
	t.Setenv("GOTRUE_JWT_SECRET", goodSecret)
	t.Setenv("CORE_DATABASE_URL", "")

	if _, err := config.Load(); err == nil {
		t.Fatal("debía fallar sin CORE_DATABASE_URL")
	}
}

func TestLoadRechazaSecretoCorto(t *testing.T) {
	baseEnv(t)
	t.Setenv("GOTRUE_JWT_SECRET", "corto")

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "GOTRUE_JWT_SECRET") {
		t.Fatalf("debía rechazar el secreto corto, err = %v", err)
	}
}

func TestLoadCORSComodinSoloEnDesarrollo(t *testing.T) {
	baseEnv(t)
	t.Setenv("CORS_ORIGINS", "*")

	if _, err := config.Load(); err != nil {
		t.Fatalf("en desarrollo el comodín es válido: %v", err)
	}

	t.Setenv("APP_ENV", "staging")
	t.Setenv("CORE_AUTH_URL", "http://auth:9999") // que el rechazo sea por CORS, no por otra cosa
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "CORS_ORIGINS") {
		t.Fatalf(`en staging CORS "*" debía rechazarse por CORS_ORIGINS, err = %v`, err)
	}
}

func TestLoadCORSMultiplesOrigenes(t *testing.T) {
	baseEnv(t)
	t.Setenv("CORS_ORIGINS", "https://a.com,https://b.com")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CORSOrigins) != 2 {
		t.Fatalf("CORSOrigins = %v", cfg.CORSOrigins)
	}
}

func TestLoadAuthURLPorDefectoSoloEnDesarrollo(t *testing.T) {
	baseEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuthURL != "http://localhost:9999" {
		t.Fatalf("AuthURL = %q, quería el localhost de desarrollo", cfg.AuthURL)
	}

	// En staging/producción un localhost por omisión apuntaría al propio
	// contenedor y las invitaciones fallarían recién al usarlas.
	t.Setenv("APP_ENV", "production")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "CORE_AUTH_URL") {
		t.Fatalf("en producción CORE_AUTH_URL es obligatoria, err = %v", err)
	}

	t.Setenv("CORE_AUTH_URL", "http://auth:9999")
	t.Setenv("CORS_ORIGINS", "https://domicilia.com.co") // de ahí sale CORE_APP_URL
	if _, err := config.Load(); err != nil {
		t.Fatalf("con CORE_AUTH_URL debía cargar: %v", err)
	}
}

func TestLoadSMTP(t *testing.T) {
	t.Run("sin CORE_SMTP_HOST no hay correo y no se pide nada más", func(t *testing.T) {
		baseEnv(t)
		cfg, err := config.Load()
		if err != nil || cfg.SMTPHost != "" {
			t.Fatalf("SMTPHost = %q, err = %v", cfg.SMTPHost, err)
		}
	})

	t.Run("con host exige usuario, contraseña y remitente", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("CORE_SMTP_HOST", "smtp.azurecomm.net")
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "CORE_SMTP_USER") {
			t.Fatalf("debía exigir usuario, contraseña y remitente, err = %v", err)
		}
		t.Setenv("CORE_SMTP_USER", "u")
		t.Setenv("CORE_SMTP_PASS", "p")
		t.Setenv("CORE_SMTP_FROM", "DoNotReply@dominio.azurecomm.net")
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.SMTPPort != 587 || cfg.SMTPFromName != "Domicilia" {
			t.Fatalf("valores por omisión = %d, %q", cfg.SMTPPort, cfg.SMTPFromName)
		}
	})

	t.Run("rechaza un puerto fuera de rango", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("CORE_SMTP_HOST", "h")
		t.Setenv("CORE_SMTP_USER", "u")
		t.Setenv("CORE_SMTP_PASS", "p")
		t.Setenv("CORE_SMTP_FROM", "a@b.co")
		t.Setenv("CORE_SMTP_PORT", "70000")
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "CORE_SMTP_PORT") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLoadAppURL(t *testing.T) {
	t.Run("en desarrollo usa el puerto habitual de Next.js", func(t *testing.T) {
		baseEnv(t)
		cfg, err := config.Load()
		if err != nil || cfg.AppURL != "http://localhost:3000" {
			t.Fatalf("AppURL = %q, err = %v", cfg.AppURL, err)
		}
	})

	t.Run("sale del primer origen concreto de CORS", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("CORS_ORIGINS", "*,https://app.domicilia.co,https://www.domicilia.co")
		cfg, err := config.Load()
		if err != nil || cfg.AppURL != "https://app.domicilia.co" {
			t.Fatalf("AppURL = %q, err = %v", cfg.AppURL, err)
		}
	})

	t.Run("CORE_APP_URL manda sobre CORS", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("CORS_ORIGINS", "https://otro.domicilia.co")
		t.Setenv("CORE_APP_URL", "https://app.domicilia.co")
		cfg, err := config.Load()
		if err != nil || cfg.AppURL != "https://app.domicilia.co" {
			t.Fatalf("AppURL = %q, err = %v", cfg.AppURL, err)
		}
	})

	t.Run("en producción sin CORS ni CORE_APP_URL falla: un enlace a localhost llegaría roto", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("CORE_AUTH_URL", "http://auth:9999")
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "CORE_APP_URL") {
			t.Fatalf("debía exigir CORE_APP_URL, err = %v", err)
		}
	})

	t.Run("rechaza una URL que no es completa", func(t *testing.T) {
		baseEnv(t)
		for _, bad := range []string{"app.domicilia.co", "ftp://app.domicilia.co", "https://"} {
			t.Setenv("CORE_APP_URL", bad)
			if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "CORE_APP_URL") {
				t.Fatalf("CORE_APP_URL=%q debía rechazarse, err = %v", bad, err)
			}
		}
	})
}

func TestLoadRechazaAuthURLInvalida(t *testing.T) {
	baseEnv(t)
	for _, bad := range []string{"auth:9999", "no es una url", "://x"} {
		t.Setenv("CORE_AUTH_URL", bad)
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "CORE_AUTH_URL") {
			t.Fatalf("CORE_AUTH_URL=%q debía rechazarse, err = %v", bad, err)
		}
	}
}

func TestLoadMediaStorage(t *testing.T) {
	baseEnv(t)

	// Sin configurar: el contenedor por omisión existe de todos modos (subir una foto responde
	// 503 por falta de CUENTA, nunca por falta de contenedor — ver internal/catalog).
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MediaStorageAccount != "" || cfg.MediaContainer != "product-media" {
		t.Fatalf("media storage por omisión = %+v", cfg)
	}

	t.Setenv("CORE_MEDIA_STORAGE_ACCOUNT", "stdomiciliamediark0d4")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MediaStorageAccount != "stdomiciliamediark0d4" || cfg.MediaContainer != "product-media" {
		t.Fatalf("media storage = %+v", cfg)
	}
}

func TestLoadDatabaseSoloNecesitaLaURL(t *testing.T) {
	// Las migraciones no deben exigir el secreto JWT ni el resto del servidor.
	t.Setenv("CORE_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("GOTRUE_JWT_SECRET", "")

	d, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("LoadDatabase: %v", err)
	}
	if d.URL == "" || d.IsProduction() {
		t.Fatalf("database = %+v", d)
	}

	t.Setenv("APP_ENV", "staging")
	d, err = config.LoadDatabase()
	if err != nil || !d.IsProduction() {
		t.Fatalf("staging debía ser producción: %+v, err = %v", d, err)
	}

	t.Setenv("CORE_DATABASE_URL", "")
	if _, err := config.LoadDatabase(); err == nil {
		t.Fatal("debía fallar sin CORE_DATABASE_URL")
	}
}
