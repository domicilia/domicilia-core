package secretbox_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/secretbox"
)

func newBox(t *testing.T) (*secretbox.Box, []byte) {
	t.Helper()
	key := make([]byte, secretbox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	b, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b, key
}

func TestCifraYDescifra(t *testing.T) {
	b, _ := newBox(t)
	const token = "EAAG-token-de-meta_con.simbolos/+="
	sealed, err := b.Seal(token, "cuenta-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, token) || strings.Contains(sealed, "EAAG") {
		t.Fatalf("el secreto aparece en claro: %s", sealed)
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Errorf("sin prefijo de versión: %s", sealed)
	}
	got, err := b.Open(sealed, "cuenta-1")
	if err != nil || got != token {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestDosCifradosDelMismoTextoSonDistintos(t *testing.T) {
	b, _ := newBox(t)
	a, _ := b.Seal("x", "ctx")
	c, _ := b.Seal("x", "ctx")
	if a == c {
		t.Fatal("el nonce debe ser distinto en cada cifrado")
	}
}

func TestNoSeAbreConOtroContexto(t *testing.T) {
	// El secreto de una cuenta copiado a otra fila (otra organización) no se descifra.
	b, _ := newBox(t)
	sealed, _ := b.Seal("secreto", "cuenta-A")
	if _, err := b.Open(sealed, "cuenta-B"); !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("err = %v, quería ErrDecrypt", err)
	}
}

func TestNoSeAbreConOtraLlave(t *testing.T) {
	a, _ := newBox(t)
	other, _ := newBox(t)
	sealed, _ := a.Seal("secreto", "ctx")
	if _, err := other.Open(sealed, "ctx"); !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("err = %v", err)
	}
}

func TestDetectaAlteraciones(t *testing.T) {
	b, _ := newBox(t)
	sealed, _ := b.Seal("secreto", "ctx")
	raw, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	for i := range raw {
		mut := bytes.Clone(raw)
		mut[i] ^= 0x01
		if _, err := b.Open("v1:"+base64.RawStdEncoding.EncodeToString(mut), "ctx"); err == nil {
			t.Fatalf("alterar el byte %d no se detectó", i)
		}
	}
}

func TestRechazaEntradasMalformadas(t *testing.T) {
	b, _ := newBox(t)
	for name, in := range map[string]string{
		"vacío": "", "sin prefijo": "abcd", "otra versión": "v2:abcd", "base64 inválido": "v1:!!!", "demasiado corto": "v1:AAAA",
	} {
		if _, err := b.Open(in, "ctx"); !errors.Is(err, secretbox.ErrDecrypt) {
			t.Errorf("%s: err = %v, quería ErrDecrypt", name, err)
		}
	}
}

func TestLlaveInvalida(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := secretbox.New(make([]byte, n)); err == nil {
			t.Errorf("una llave de %d bytes debía rechazarse", n)
		}
	}
}

func TestParseKey(t *testing.T) {
	key := make([]byte, secretbox.KeySize)
	_, _ = rand.Read(key)
	for name, enc := range map[string]string{
		"estándar": base64.StdEncoding.EncodeToString(key), "sin relleno": base64.RawStdEncoding.EncodeToString(key),
		"url": base64.URLEncoding.EncodeToString(key), "con espacios": "  " + base64.StdEncoding.EncodeToString(key) + "\n",
	} {
		got, err := secretbox.ParseKey(enc)
		if err != nil || !bytes.Equal(got, key) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, enc := range map[string]string{"corta": base64.StdEncoding.EncodeToString(key[:16]), "no base64": "no es base64 !!", "vacía": ""} {
		if _, err := secretbox.ParseKey(enc); err == nil {
			t.Errorf("%s: debía rechazarse", name)
		}
	}
}
