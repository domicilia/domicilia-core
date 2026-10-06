package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/users"
)

// fakeOps registra lo que el comando le pide al servicio.
type fakeOps struct {
	exists  bool
	ensured []string // "email|password|nombre"
	retired []string
	missing map[string]bool // correos que Retire no encuentra
	err     error
}

func (f *fakeOps) Exists(context.Context, string) (bool, error) { return f.exists, nil }

func (f *fakeOps) Ensure(_ context.Context, email, password string, name *string) (users.User, error) {
	if f.err != nil {
		return users.User{}, f.err
	}
	n := ""
	if name != nil {
		n = *name
	}
	f.ensured = append(f.ensured, email+"|"+password+"|"+n)
	return users.User{ID: uuid.Nil, Email: email, IsGeneralAdmin: true}, nil
}

func (f *fakeOps) Retire(_ context.Context, email string) (bool, error) {
	f.retired = append(f.retired, email)
	return !f.missing[email], nil
}

// cli arma el comando con `stdin` como respuestas a las preguntas y `env` como
// variables de entorno. Devuelve también lo que imprimió.
func cli(ops *fakeOps, stdin string, env map[string]string, passwords ...string) (*superadminCLI, *bytes.Buffer) {
	var out bytes.Buffer
	return &superadminCLI{
		ops:    ops,
		in:     bufio.NewReader(strings.NewReader(stdin)),
		out:    &out,
		getenv: func(k string) string { return env[k] },
		readPassword: func(string) (string, error) {
			if len(passwords) == 0 {
				return "", errors.New("se pidió una contraseña que la prueba no previó")
			}
			pw := passwords[0]
			passwords = passwords[1:]
			return pw, nil
		},
	}, &out
}

const passwordOK = "una-contrasena-larga-1"

func TestSuperadminPorVariablesNoPreguntaNada(t *testing.T) {
	ops := &fakeOps{}
	c, out := cli(ops, "", map[string]string{
		"SUPERADMIN_EMAIL": "Ana@Ejemplo.com", "SUPERADMIN_NAME": "Ana Pérez",
		"SUPERADMIN_PASSWORD": passwordOK, "SUPERADMIN_RETIRE": "admin@domicilia.dev, otro@ejemplo.com",
	})

	if err := c.run(context.Background()); err != nil {
		t.Fatalf("run: %v\nsalida: %s", err, out)
	}
	if len(ops.ensured) != 1 || ops.ensured[0] != "ana@ejemplo.com|"+passwordOK+"|Ana Pérez" {
		t.Fatalf("ensured = %v (el correo debe ir normalizado)", ops.ensured)
	}
	if strings.Join(ops.retired, ",") != "admin@domicilia.dev,otro@ejemplo.com" {
		t.Fatalf("retired = %v", ops.retired)
	}
	if !strings.Contains(out.String(), "es superadmin") {
		t.Fatalf("salida = %q", out)
	}
}

func TestSuperadminInteractivoConfirmaElCorreo(t *testing.T) {
	ops := &fakeOps{}
	// correo, correo otra vez, nombre, retiros
	c, _ := cli(ops, "ana@ejemplo.com\nana@ejemplo.com\nAna\n\n", nil, passwordOK, passwordOK)

	if err := c.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(ops.ensured) != 1 || ops.ensured[0] != "ana@ejemplo.com|"+passwordOK+"|Ana" {
		t.Fatalf("ensured = %v", ops.ensured)
	}
	if len(ops.retired) != 0 {
		t.Fatalf("no se pidió retirar a nadie: %v", ops.retired)
	}
}

// Un error de dominio (gmil.com) dejaría la cuenta más poderosa sin recuperación.
func TestSuperadminSiElCorreoNoCoincideNoHaceNada(t *testing.T) {
	ops := &fakeOps{}
	c, _ := cli(ops, "ana@ejemplo.com\nana@gmil.com\n", nil)

	err := c.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no coinciden") {
		t.Fatalf("err = %v, quería que abortara por correos distintos", err)
	}
	if len(ops.ensured) != 0 || len(ops.retired) != 0 {
		t.Fatal("no debía hacerse ningún cambio")
	}
}

func TestSuperadminRechazaUnCorreoInvalido(t *testing.T) {
	ops := &fakeOps{}
	c, _ := cli(ops, "", map[string]string{"SUPERADMIN_EMAIL": "no-es-correo"})
	if err := c.run(context.Background()); err == nil || !strings.Contains(err.Error(), "correo inválido") {
		t.Fatalf("err = %v", err)
	}
	if len(ops.ensured) != 0 {
		t.Fatal("no debía hacerse ningún cambio")
	}
}

func TestSuperadminExistenteConservaSuContrasena(t *testing.T) {
	ops := &fakeOps{exists: true}
	// No se le da ninguna contraseña a readPassword: si el comando la pidiera, la prueba falla.
	c, out := cli(ops, "", map[string]string{"SUPERADMIN_EMAIL": "ana@ejemplo.com"})

	if err := c.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(ops.ensured) != 1 || !strings.HasPrefix(ops.ensured[0], "ana@ejemplo.com||") {
		t.Fatalf("ensured = %v: para un existente la contraseña va vacía", ops.ensured)
	}
	if !strings.Contains(out.String(), "se conserva su contraseña") {
		t.Fatalf("debía avisar que conserva la contraseña: %q", out)
	}
}

func TestSuperadminContrasenaCortaOQueNoCoincide(t *testing.T) {
	env := map[string]string{"SUPERADMIN_EMAIL": "ana@ejemplo.com"}

	t.Run("demasiado corta, por variable", func(t *testing.T) {
		ops := &fakeOps{}
		e := map[string]string{"SUPERADMIN_PASSWORD": "corta"}
		for k, v := range env {
			e[k] = v
		}
		c, _ := cli(ops, "", e)
		if err := c.run(context.Background()); err == nil || !strings.Contains(err.Error(), "al menos") {
			t.Fatalf("err = %v", err)
		}
		if len(ops.ensured) != 0 {
			t.Fatal("no debía crearse con una contraseña corta")
		}
	})

	t.Run("las dos escrituras no coinciden", func(t *testing.T) {
		ops := &fakeOps{}
		c, _ := cli(ops, "", env, passwordOK, passwordOK+"x")
		if err := c.run(context.Background()); err == nil || !strings.Contains(err.Error(), "no coinciden") {
			t.Fatalf("err = %v", err)
		}
		if len(ops.ensured) != 0 {
			t.Fatal("no debía crearse")
		}
	})
}

func TestSuperadminNoSeRetiraASiMismo(t *testing.T) {
	// Retirarse a sí mismo dejaría la plataforma sin superadmin.
	ops := &fakeOps{}
	c, _ := cli(ops, "", map[string]string{
		"SUPERADMIN_EMAIL": "ana@ejemplo.com", "SUPERADMIN_PASSWORD": passwordOK,
		"SUPERADMIN_RETIRE": "ANA@ejemplo.com, viejo@ejemplo.com",
	})
	if err := c.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ops.retired, ",") != "viejo@ejemplo.com" {
		t.Fatalf("retired = %v, quería solo viejo@ejemplo.com", ops.retired)
	}
}

func TestSuperadminInformaLosRetirosQueNoExisten(t *testing.T) {
	ops := &fakeOps{missing: map[string]bool{"fantasma@ejemplo.com": true}}
	c, out := cli(ops, "", map[string]string{
		"SUPERADMIN_EMAIL": "ana@ejemplo.com", "SUPERADMIN_PASSWORD": passwordOK,
		"SUPERADMIN_RETIRE": "fantasma@ejemplo.com,viejo@ejemplo.com",
	})
	if err := c.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no existe: fantasma@ejemplo.com") || !strings.Contains(out.String(), "retirado: viejo@ejemplo.com") {
		t.Fatalf("salida = %q", out)
	}
}

func TestSuperadminSiFallaElAltaNoRetiraANadie(t *testing.T) {
	// Primero el nuevo, luego los retiros: si crear falla, los viejos siguen ahí
	// y la plataforma no se queda sin superadmin.
	ops := &fakeOps{err: errors.New("auth-domicilia caído")}
	c, _ := cli(ops, "", map[string]string{
		"SUPERADMIN_EMAIL": "ana@ejemplo.com", "SUPERADMIN_PASSWORD": passwordOK,
		"SUPERADMIN_RETIRE": "viejo@ejemplo.com",
	})
	if err := c.run(context.Background()); err == nil {
		t.Fatal("debía propagar el error")
	}
	if len(ops.retired) != 0 {
		t.Fatalf("se retiró a %v pese a que el alta falló", ops.retired)
	}
}
