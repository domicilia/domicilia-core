// Package azblob sube blobs a Azure Blob Storage autenticándose con la identidad administrada del
// VM a través de IMDS — sin SDK de Azure ni ninguna credencial en disco. Mismos endpoints y mismo
// patrón que domicilia-infra/deploy/backup-postgres.sh (ya en producción para los respaldos de
// Postgres): un token de IMDS para el recurso storage.azure.com, y un PUT Blob directo por REST.
//
// IMDS (169.254.169.254) solo responde desde DENTRO de una VM de Azure: en desarrollo local,
// Upload siempre falla. Por eso quien use este paquete (internal/catalog) solo lo construye si
// hay cuenta configurada (CORE_MEDIA_STORAGE_ACCOUNT) — mismo criterio que payments.Gateway sin
// CORE_EPAYCO_* o WhatsApp sin CORE_SECRETS_KEY: sin configurar, la función responde 503 en vez
// de intentar una llamada que no puede funcionar ahí.
package azblob

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	imdsTokenURL    = "http://169.254.169.254/metadata/identity/oauth2/token" //nolint:gosec // endpoint fijo de IMDS, no una credencial
	storageResource = "https://storage.azure.com/"
	blobAPIVersion  = "2021-08-06"
	requestTimeout  = 30 * time.Second
)

// Client sube blobs a un contenedor fijo de una storage account.
type Client struct {
	account   string
	container string
	http      *http.Client
}

// New crea el cliente. account y container nunca llegan vacíos: Config.validate ya lo exige
// antes de que exista uno.
func New(account, container string) *Client {
	return &Client{account: account, container: container, http: &http.Client{Timeout: requestTimeout}}
}

// PublicURL devuelve la URL pública de un blob de este contenedor. La lectura es pública (ver el
// módulo de Terraform modules/storage: container_access_type = "blob") — solo la ESCRITURA exige
// el token de la identidad administrada, por eso esto no necesita contexto ni puede fallar.
func (c *Client) PublicURL(blobName string) string {
	return fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", c.account, c.container, blobName)
}

// Upload sube content (size bytes, con ese Content-Type) al blob indicado — lo reemplaza si ya
// existe — y devuelve su URL pública.
func (c *Client) Upload(ctx context.Context, blobName string, content io.Reader, size int64, contentType string) (string, error) {
	token, err := c.fetchToken(ctx)
	if err != nil {
		return "", fmt.Errorf("azblob: token de IMDS: %w", err)
	}

	dest := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", c.account, c.container, url.PathEscape(blobName))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, dest, content)
	if err != nil {
		return "", fmt.Errorf("azblob: armar la petición: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-ms-version", blobAPIVersion)
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("azblob: subir el blob: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) //nolint:errcheck // solo para el mensaje de error
		return "", fmt.Errorf("azblob: la subida devolvió %d: %s", resp.StatusCode, body)
	}
	return c.PublicURL(blobName), nil
}

type imdsTokenResponse struct {
	AccessToken string `json:"access_token"`
}

// fetchToken pide un token de la identidad administrada del VM para el recurso
// storage.azure.com — mismo endpoint y mismos parámetros que backup-postgres.sh.
func (c *Client) fetchToken(ctx context.Context) (string, error) {
	q := url.Values{"api-version": {"2018-02-01"}, "resource": {storageResource}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imdsTokenURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata", "true")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) //nolint:errcheck // solo para el mensaje de error
		return "", fmt.Errorf("IMDS devolvió %d: %s", resp.StatusCode, body)
	}
	var out imdsTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decodificar la respuesta de IMDS: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("IMDS no devolvió access_token")
	}
	return out.AccessToken, nil
}
