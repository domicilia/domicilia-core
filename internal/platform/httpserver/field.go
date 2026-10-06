package httpserver

import (
	"bytes"
	"encoding/json"
)

// Field distingue en un cambio parcial (PATCH) tres cosas que un puntero no separa:
//
//	{}                     → Set = false: no se toca el campo
//	{"campo": null}        → Set = true, Value = nil: se deja en blanco
//	{"campo": "valor"}     → Set = true, Value = &"valor": se cambia
type Field[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON implementa json.Unmarshaler. Solo se llama si la clave está presente.
func (f *Field[T]) UnmarshalJSON(b []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		f.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	f.Value = &v
	return nil
}
