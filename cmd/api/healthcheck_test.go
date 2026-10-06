package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestPortFromAddr(t *testing.T) {
	tests := map[string]int{
		":8080":        8080,
		"0.0.0.0:9000": 9000,
		"127.0.0.1:81": 81,
		"":             defaultPort,
		"sin-puerto":   defaultPort,
		":abc":         defaultPort,
		":0":           defaultPort,
		":99999":       defaultPort,
		":-5":          defaultPort,
		":8080/../x":   defaultPort,
		":80 80":       defaultPort,
	}
	for addr, want := range tests {
		if got := portFromAddr(addr); got != want {
			t.Errorf("portFromAddr(%q) = %d, quería %d", addr, got, want)
		}
	}
}

// portOf devuelve el puerto de un servidor de httptest.
func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url del servidor de prueba: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("puerto del servidor de prueba: %v", err)
	}
	return port
}

func TestProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	caido := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer caido.Close()

	if err := probe(context.Background(), portOf(t, ok)); err != nil {
		t.Fatalf("un 200 debía pasar: %v", err)
	}
	if err := probe(context.Background(), portOf(t, caido)); err == nil {
		t.Fatal("un 503 debía fallar")
	}
	if err := probe(context.Background(), 1); err == nil {
		t.Fatal("un puerto cerrado debía fallar")
	}
}

// La garantía de seguridad: sea cual sea el host de la URL, el cliente solo
// conecta a loopback. Un host ajeno debe terminar en NUESTRO servidor de prueba.
func TestLoopbackClientIgnoraElHostDeLaURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://host-ajeno.invalid/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := loopbackClient(portOf(t, srv)).Do(req)
	if err != nil {
		t.Fatalf("debía llegar al servidor local pese al host ajeno: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("estado = %d", resp.StatusCode)
	}
}
