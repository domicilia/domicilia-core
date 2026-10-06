package app_test

// Contrato OpenAPI: openapi/openapi.yaml es la fuente de verdad para los clientes, así que se comprueba de dos
// formas. (1) Lo documentado y lo que el servidor sirve son EXACTAMENTE las mismas rutas. (2) Toda petición que
// las pruebas de contrato hacen a una ruta documentada valida su respuesta REAL contra el esquema: si el código
// cambia una forma y el contrato no, alguna prueba falla.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/labstack/echo/v5"
)

var (
	specOnce   sync.Once
	specDoc    *openapi3.T
	specRouter routers.Router
	specErr    error
)

func loadSpec() (*openapi3.T, routers.Router, error) {
	specOnce.Do(func() {
		path, err := filepath.Abs(filepath.Join("..", "..", "openapi", "openapi.yaml"))
		if err != nil {
			specErr = err
			return
		}
		specDoc, specErr = openapi3.NewLoader().LoadFromFile(path)
		if specErr != nil {
			return
		}
		if specErr = specDoc.Validate(context.Background()); specErr != nil {
			return
		}
		specRouter, specErr = legacy.NewRouter(specDoc)
	})
	return specDoc, specRouter, specErr
}

// checkContract valida la respuesta real de una petición contra el contrato, si la ruta está documentada.
func (h *harness) checkContract(req *http.Request, rec *httptest.ResponseRecorder) {
	h.t.Helper()
	_, router, err := loadSpec()
	if err != nil {
		h.t.Fatalf("el contrato OpenAPI no carga o no es válido: %v", err)
	}
	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		return // ruta no documentada (todavía): nada que validar
	}
	in := &openapi3filter.RequestValidationInput{
		Request: req, PathParams: pathParams, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, ExcludeRequestBody: true},
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: rec.Code, Header: rec.Header(),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	out.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), out); err != nil {
		h.t.Errorf("la respuesta de %s %s no cumple el contrato OpenAPI: %v\ncuerpo: %s", req.Method, req.URL.Path, err, rec.Body.String())
	}
}

// documented son los prefijos de las rutas que el contrato ya cubre.
var documented = regexp.MustCompile(`/(inboxes|contacts|conversations|categories|products|modifier-groups|cart|carts|orders|promotions)(/|$)|^/webhooks/(whatsapp|payments)$`)

func TestElContratoOpenAPIEsExactamenteLoQueElServidorSirve(t *testing.T) {
	h := newHarness(t)
	doc, _, err := loadSpec()
	if err != nil {
		t.Fatalf("el contrato OpenAPI no carga o no es válido: %v", err)
	}

	served := map[string]bool{}
	for _, r := range h.handler.Router().Routes() {
		if r.Method == echo.RouteNotFound || !documented.MatchString(r.Path) {
			continue
		}
		served[r.Method+" "+regexp.MustCompile(`:([a-z_]+)`).ReplaceAllString(r.Path, "{$1}")] = true
	}
	specced := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			specced[method+" "+path] = true
		}
	}
	for key := range served {
		if !specced[key] {
			t.Errorf("el servidor sirve %s y el contrato no lo documenta", key)
		}
	}
	for key := range specced {
		if !served[key] {
			t.Errorf("el contrato documenta %s y el servidor no lo sirve", key)
		}
	}
	if len(served) < 15 {
		t.Fatalf("solo se encontraron %d rutas documentables: ¿cambió la forma de leer el router?", len(served))
	}
}

func TestElContratoDescribeCadaOperacionConSuIdYEtiqueta(t *testing.T) {
	doc, _, err := loadSpec()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			key := method + " " + path
			if op.OperationID == "" || len(op.Tags) == 0 || strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s: falta operationId, etiqueta o resumen", key)
			}
			if other, dup := ids[op.OperationID]; dup {
				t.Errorf("operationId %q repetido en %s y %s", op.OperationID, other, key)
			}
			ids[op.OperationID] = key
			if op.Responses.Default() == nil {
				t.Errorf("%s: falta la respuesta por omisión (errores)", key)
			}
		}
	}
}
