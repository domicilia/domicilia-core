package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
	"github.com/domicilia/domicilia-core/internal/users"
)

// superadminOps es lo que el comando necesita del servicio de superadmins.
type superadminOps interface {
	Exists(ctx context.Context, email string) (bool, error)
	Ensure(ctx context.Context, email, password string, fullName *string) (users.User, error)
	Retire(ctx context.Context, email string) (bool, error)
}

// superadminCLI es `api create-superadmin` con su entrada y salida inyectadas,
// para poder probarlo sin terminal ni base de datos.
type superadminCLI struct {
	ops    superadminOps
	in     *bufio.Reader
	out    io.Writer
	getenv func(string) string
	// readPassword lee una contraseña sin mostrarla.
	readPassword func(prompt string) (string, error)
}

// runCreateSuperadmin atiende `api create-superadmin`: crea (o promueve) a un
// superadmin de la plataforma y, opcionalmente, retira a otros. Se corre dentro
// de la red del stack, porque necesita llegar a la base y a auth-domicilia:
//
//	docker compose run --rm -it core create-superadmin
//
// Variables (todas opcionales; lo que falte se pregunta):
//
//	SUPERADMIN_EMAIL     correo del superadmin
//	SUPERADMIN_NAME      nombre para mostrar
//	SUPERADMIN_PASSWORD  contraseña (evítala: queda en el historial y el entorno;
//	                     si no está, se pide sin eco)
//	SUPERADMIN_RETIRE    correos (separados por coma) a los que se les quita el
//	                     rol y se desactiva la cuenta, p. ej. las cuentas semilla.
//	                     Se aplica DESPUÉS de crear al nuevo.
func runCreateSuperadmin() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	idp := gotrue.New(cfg.AuthURL, cfg.JWTSecret, nil)
	svc := users.NewSuperadminService(users.NewSuperadminRepository(pool), idp)

	cli := &superadminCLI{
		ops:          svc,
		in:           bufio.NewReader(os.Stdin),
		out:          os.Stdout,
		getenv:       os.Getenv,
		readPassword: terminalPassword,
	}
	return cli.run(ctx)
}

func (c *superadminCLI) run(ctx context.Context) error {
	email, err := c.askEmail()
	if err != nil {
		return err
	}

	name := c.getenv("SUPERADMIN_NAME")
	if name == "" {
		if name, err = c.ask("Nombre (opcional): "); err != nil {
			return err
		}
	}
	var fullName *string
	if name != "" {
		fullName = &name
	}

	exists, err := c.ops.Exists(ctx, email)
	if err != nil {
		return err
	}
	password := ""
	if exists {
		c.printf("  %s ya existe: se conserva su contraseña, solo se le da el rol.\n", email)
	} else if password, err = c.askPassword(); err != nil {
		return err
	}

	retire, err := c.askRetire(email)
	if err != nil {
		return err
	}

	// Primero el nuevo, luego los retiros: nunca se queda la plataforma sin superadmin.
	u, err := c.ops.Ensure(ctx, email, password, fullName)
	if err != nil {
		return err
	}
	c.printf("✓ %s es superadmin (id=%s).\n", u.Email, u.ID)
	for _, r := range retire {
		found, err := c.ops.Retire(ctx, r)
		if err != nil {
			return fmt.Errorf("retirar %s: %w", r, err)
		}
		if found {
			c.printf("  retirado: %s\n", r)
		} else {
			c.printf("  no existe: %s\n", r)
		}
	}
	c.printf("  Entra por /platform/login. Recomendado: activar MFA en GoTrue.\n")
	return nil
}

// askEmail pide el correo. Si no viene por variable, se confirma escribiéndolo
// otra vez: un superadmin es la cuenta con más poder, y un error de dominio
// (gmil.com) la deja sin recuperación.
func (c *superadminCLI) askEmail() (string, error) {
	fromEnv := c.getenv("SUPERADMIN_EMAIL")
	raw := fromEnv
	if raw == "" {
		var err error
		if raw, err = c.ask("Correo del superadmin: "); err != nil {
			return "", err
		}
	}
	email, err := validate.Email("correo", raw)
	if err != nil {
		return "", fmt.Errorf("correo inválido: %w", err)
	}
	if fromEnv == "" {
		again, err := c.ask(fmt.Sprintf("Vas a crear/promover a superadmin: %s\nEscribe el correo otra vez: ", email))
		if err != nil {
			return "", err
		}
		if strings.ToLower(strings.TrimSpace(again)) != email {
			return "", errors.New("los correos no coinciden; no se hizo ningún cambio")
		}
	}
	return email, nil
}

func (c *superadminCLI) askPassword() (string, error) {
	pw := c.getenv("SUPERADMIN_PASSWORD")
	if pw == "" {
		var err error
		if pw, err = c.readPassword(fmt.Sprintf("Contraseña (mínimo %d caracteres): ", users.MinSuperadminPasswordLength)); err != nil {
			return "", err
		}
		again, err := c.readPassword("Repite la contraseña: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("las contraseñas no coinciden; no se hizo ningún cambio")
		}
	}
	if len(pw) < users.MinSuperadminPasswordLength {
		return "", fmt.Errorf("la contraseña debe tener al menos %d caracteres", users.MinSuperadminPasswordLength)
	}
	return pw, nil
}

// askRetire devuelve los correos a retirar, sin incluir al que se está creando.
func (c *superadminCLI) askRetire(self string) ([]string, error) {
	raw := c.getenv("SUPERADMIN_RETIRE")
	if raw == "" {
		var err error
		if raw, err = c.ask("Correos a retirar como superadmin (coma, opcional): "); err != nil {
			return nil, err
		}
	}
	var out []string
	for _, e := range strings.Split(raw, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && e != self {
			out = append(out, e)
		}
	}
	return out, nil
}

func (c *superadminCLI) ask(prompt string) (string, error) {
	c.printf("%s", prompt)
	line, err := c.in.ReadString('\n')
	// El fin de la entrada es una respuesta vacía, no un fallo: los campos
	// opcionales (nombre, retiros) simplemente quedan en blanco, y los
	// obligatorios (el correo) los rechaza la validación.
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("leer la respuesta: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func (c *superadminCLI) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.out, format, args...)
}

// terminalPassword lee una contraseña sin eco. Sin terminal (entrada por tubería)
// no hay forma segura de ocultarla: se rechaza en vez de pedirla a la vista.
func terminalPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd()) //nolint:gosec // los descriptores de archivo caben en int
	if !term.IsTerminal(fd) {
		return "", errors.New("no hay terminal para pedir la contraseña sin eco: usa `docker compose run -it` o define SUPERADMIN_PASSWORD")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("leer la contraseña: %w", err)
	}
	return string(b), nil
}
