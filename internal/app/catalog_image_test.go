package app_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// fakeMediaUploader sustituye a azblob.Client en las pruebas: IMDS solo responde dentro de una VM
// de Azure, así que aquí se prueba el flujo completo (permisos, validación, persistencia) sin red
// real — ver app.Deps.MediaUploader.
type fakeMediaUploader struct {
	lastBlobName    string
	lastContentType string
	lastSize        int64
}

func (f *fakeMediaUploader) Upload(_ context.Context, blobName string, content io.Reader, size int64, contentType string) (string, error) {
	n, _ := io.Copy(io.Discard, content)
	f.lastBlobName, f.lastContentType, f.lastSize = blobName, contentType, n
	return "https://fake.blob.core.windows.net/product-media/" + blobName, nil
}

// tinyPNG arma el PNG más chico posible: lo que importa es que http.DetectContentType lo
// reconozca como image/png, no su tamaño.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// uploadFile manda un multipart/form-data con un solo campo de archivo — h.do solo sabe mandar
// JSON, por eso esto no lo reusa.
func (h *harness) uploadFile(path, fieldName, filename string, content []byte, as *person) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(fieldName, filename)
	must(h.t, err)
	_, err = part.Write(content)
	must(h.t, err)
	must(h.t, w.Close())

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if as != nil {
		req.Header.Set("Authorization", "Bearer "+h.bearer(*as))
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	h.checkContract(req, rec)
	return rec
}

func productImageURL(o organization, productID string) string {
	return productsURL(o, "/"+productID+"/image")
}

func TestSubirFotoDeProducto(t *testing.T) {
	media := &fakeMediaUploader{}
	h := newHarnessWithMedia(t, media)
	admin, o := orgAdmin(h, "Acme")
	productID, _ := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	rec := h.uploadFile(productImageURL(o, productID), "image", "foto.png", tinyPNG(t), &admin)
	want(t, rec, http.StatusOK)
	p := jsonMap(t, rec)
	if url, _ := p["image_url"].(string); url == "" {
		t.Fatalf("producto sin image_url: %v", p)
	}
	if media.lastContentType != "image/png" || media.lastSize == 0 {
		t.Fatalf("subida registrada = %+v", media)
	}

	t.Run("sin org.catalog.manage, 403", func(t *testing.T) {
		empleado := h.user()
		h.join(empleado, o, "employee")
		want(t, h.uploadFile(productImageURL(o, productID), "image", "foto.png", tinyPNG(t), &empleado), http.StatusForbidden)
	})

	t.Run("un archivo que no es una imagen real, 422 (se detecta por contenido, no por el nombre)", func(t *testing.T) {
		rec := h.uploadFile(productImageURL(o, productID), "image", "foto.png", []byte("esto no es una imagen"), &admin)
		want(t, rec, http.StatusUnprocessableEntity)
	})

	t.Run("sin el campo \"image\", 422", func(t *testing.T) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		must(t, w.Close())
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, productImageURL(o, productID), &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+h.bearer(admin))
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		want(t, rec, http.StatusUnprocessableEntity)
	})

	t.Run("un producto que no existe, 404", func(t *testing.T) {
		want(t, h.uploadFile(productImageURL(o, uuid.NewString()), "image", "foto.png", tinyPNG(t), &admin), http.StatusNotFound)
	})

	t.Run("una organización ajena no puede subir aquí", func(t *testing.T) {
		adminOtra, otra := orgAdmin(h, "Otra")
		want(t, h.uploadFile(productImageURL(o, productID), "image", "foto.png", tinyPNG(t), &adminOtra), http.StatusForbidden)
		want(t, h.uploadFile(productImageURL(otra, productID), "image", "foto.png", tinyPNG(t), &adminOtra), http.StatusNotFound)
	})
}

func TestSubirFotoSinAlmacenamientoConfigurado(t *testing.T) {
	// newHarness (sin newHarnessWithMedia): mismo criterio que la pasarela de pago sin
	// CORE_EPAYCO_* — la función responde 503 en vez de intentar IMDS, que ahí no respondería.
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	productID, _ := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)

	want(t, h.uploadFile(productImageURL(o, productID), "image", "foto.png", tinyPNG(t), &admin), http.StatusServiceUnavailable)
}
