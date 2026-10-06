package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	defaultPort        = 8080
	healthcheckTimeout = 3 * time.Second
)

// runHealthcheck es el subcomando `api healthcheck`: la imagen de producción
// es distroless (sin shell, curl ni wget), así que el HEALTHCHECK de Docker
// tiene que ser el propio binario. Sale con 0 si /healthz responde 200.
func runHealthcheck() int {
	if err := probe(context.Background(), portFromAddr(os.Getenv("CORE_HTTP_ADDR"))); err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	return 0
}

// portFromAddr extrae el puerto de ":8080" o "0.0.0.0:8080". Lo que venga del
// entorno se convierte en un ENTERO validado y nada más: un valor raro cae al
// puerto por defecto. Así la sonda nunca maneja texto del entorno como parte
// de una URL.
func portFromAddr(addr string) int {
	_, raw, err := net.SplitHostPort(addr)
	if err != nil {
		return defaultPort
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return defaultPort
	}
	return port
}

// healthURL es una CONSTANTE: el host es ficticio a propósito. A dónde se
// conecta de verdad lo decide loopbackClient, no la URL.
const healthURL = "http://healthcheck/healthz"

// loopbackClient devuelve un cliente HTTP cuyo Transport marca SIEMPRE a
// 127.0.0.1:port, sea cual sea el host de la URL. La garantía "esta sonda
// nunca sale del contenedor" vive en el Transport, no en cómo se arme un string.
func loopbackClient(port int) *http.Client {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := net.Dialer{Timeout: healthcheckTimeout}
	return &http.Client{
		Timeout: healthcheckTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, target)
			},
		},
	}
}

func probe(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	resp, err := loopbackClient(port).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("estado %d", resp.StatusCode)
	}
	return nil
}
