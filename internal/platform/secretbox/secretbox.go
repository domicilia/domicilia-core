// Package secretbox cifra secretos (tokens de terceros) antes de guardarlos en la base:
// AES-256-GCM con la llave del servicio, un nonce aleatorio por cifrado y datos
// asociados (AAD) que atan el texto cifrado a su dueño. Quien lea la tabla sin la llave
// no obtiene nada, y un secreto copiado a otra fila (otra organización) no se descifra.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeySize es el largo de la llave en bytes (AES-256).
const KeySize = 32

// prefix identifica el formato: deja cambiar de algoritmo o de llave sin ambigüedad.
const prefix = "v1:"

// ErrDecrypt agrupa todo fallo al abrir: llave equivocada, AAD distinto o dato alterado.
// No dice cuál, a propósito.
var ErrDecrypt = errors.New("secretbox: no se pudo descifrar")

// Box cifra y descifra con una llave.
type Box struct{ aead cipher.AEAD }

// New crea un Box. key son 32 bytes.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secretbox: la llave debe tener %d bytes, tiene %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	return &Box{aead: aead}, nil
}

// ParseKey lee una llave en base64 (estándar o URL, con o sin relleno).
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != KeySize {
				return nil, fmt.Errorf("secretbox: la llave decodificada debe tener %d bytes, tiene %d", KeySize, len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("secretbox: la llave no es base64 válido")
}

// Seal cifra plaintext. aad ata el resultado a su contexto (p. ej. el id de la cuenta):
// hay que dar el mismo aad para abrirlo.
func (b *Box) Seal(plaintext, aad string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretbox: nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad)) // nonce || texto || etiqueta
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open descifra lo que devolvió Seal con el mismo aad.
func (b *Box) Open(sealed, aad string) (string, error) {
	raw, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return "", ErrDecrypt
	}
	data, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(data) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", ErrDecrypt
	}
	nonce, ct := data[:b.aead.NonceSize()], data[b.aead.NonceSize():]
	pt, err := b.aead.Open(nil, nonce, ct, []byte(aad))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(pt), nil
}
