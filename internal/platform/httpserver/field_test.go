package httpserver_test

import (
	"encoding/json"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

func TestFieldDistingueAusenteNuloYValor(t *testing.T) {
	type body struct {
		Name httpserver.Field[string] `json:"name"`
		N    httpserver.Field[int]    `json:"n"`
	}
	tests := []struct {
		in      string
		nameSet bool
		name    *string
	}{
		{`{}`, false, nil},
		{`{"name":null}`, true, nil},
		{`{"name":"Ana"}`, true, ptr("Ana")},
		{`{"name":""}`, true, ptr("")},
	}
	for _, tc := range tests {
		var b body
		if err := json.Unmarshal([]byte(tc.in), &b); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if b.Name.Set != tc.nameSet || (b.Name.Value == nil) != (tc.name == nil) || (tc.name != nil && *b.Name.Value != *tc.name) {
			t.Errorf("%s: %+v", tc.in, b.Name)
		}
	}
	var b body
	if err := json.Unmarshal([]byte(`{"n":"no es número"}`), &b); err == nil {
		t.Error("un tipo equivocado debía fallar")
	}
}

func ptr[T any](v T) *T { return &v }
