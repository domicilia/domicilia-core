package app_test

import (
	"net/http"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/config"
)

// placeSimpleOrder arma un carrito de una gaseosa y lo confirma; devuelve el id del pedido.
func placeSimpleOrder(h *harness, admin person, o organization, cliente person) string {
	h.t.Helper()
	productID, variantID := seedSimpleProduct(h, admin, o, "Gaseosa", 3000)
	want(h.t, h.do(http.MethodPost, cartURL(o, "/items"), map[string]any{
		"product_id": productID, "variant_id": variantID, "quantity": 1,
	}, &cliente), http.StatusCreated)
	rec := h.do(http.MethodPost, cartURL(o, "/place"), nil, &cliente)
	want(h.t, rec, http.StatusOK)
	return jsonMap(h.t, rec)["id"].(string)
}

func TestIniciarUnPago(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	orderID := placeSimpleOrder(h, admin, o, cliente)

	rec := h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente)
	want(t, rec, http.StatusCreated)
	pay := jsonMap(t, rec)
	if pay["gateway"] != "epayco" || pay["status"] != "pending" || pay["amount_cents"] != float64(3000) {
		t.Fatalf("pago iniciado = %v", pay)
	}
	if url, _ := pay["checkout_url"].(string); url == "" {
		t.Fatal("checkout_url vacío")
	}

	t.Run("un carrito (draft) no se puede pagar", func(t *testing.T) {
		// Mismo criterio que GetMyOrder (ver TestAislamientoYPermisosDePedidos): un draft no es
		// un pedido propio todavía, así que "pagarlo" da 404, no 409.
		cart := jsonMap(t, h.do(http.MethodGet, cartURL(o, ""), nil, &cliente))
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+cart["id"].(string)+"/pay"), nil, &cliente), http.StatusNotFound)
	})

	t.Run("solo el dueño del pedido puede iniciar su pago", func(t *testing.T) {
		extrano := h.user()
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &extrano), http.StatusNotFound)
	})

	t.Run("un pedido ya pagado no admite un cobro nuevo", func(t *testing.T) {
		want(t, h.postPaymentWebhook(pay["id"].(string), "ref-y", "tx-y", "30.00", "Aceptada", nil), http.StatusOK)
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente), http.StatusConflict)
	})
}

func TestPasarelaNoConfigurada(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.EpaycoPublicKey = "" })
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	orderID := placeSimpleOrder(h, admin, o, cliente)

	want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente), http.StatusServiceUnavailable)
}

func TestWebhookDePagos(t *testing.T) {
	t.Run("una firma inválida se rechaza y no toca el pedido", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		orderID := placeSimpleOrder(h, admin, o, cliente)
		pay := jsonMap(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente))
		paymentID := pay["id"].(string)

		mala := "0000000000000000000000000000000000000000000000000000000000000000"
		want(t, h.postPaymentWebhook(paymentID, "ref-x", "tx-x", "30.00", "Aceptada", &mala), http.StatusUnauthorized)

		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "placed" {
			t.Fatalf("un webhook con firma inválida cambió el pedido: status = %v", got)
		}
	})

	t.Run("un pago rechazado se puede reintentar sobre el MISMO pedido", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		orderID := placeSimpleOrder(h, admin, o, cliente)
		pay := jsonMap(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente))
		firstPaymentID := pay["id"].(string)

		want(t, h.postPaymentWebhook(firstPaymentID, "ref-r", "tx-r", "30.00", "Rechazada", nil), http.StatusOK)
		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "payment_failed" {
			t.Fatalf("status = %v, quería payment_failed", got)
		}

		// Sin reintentar el pago, no se puede pagar de nuevo: sigue sin estar en placed.
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente), http.StatusConflict)

		want(t, h.do(http.MethodPost, "/v1/orders/"+orderID+"/retry-payment", nil, &cliente), http.StatusOK)
		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "placed" {
			t.Fatalf("tras reintentar, status = %v, quería placed", got)
		}

		rec := h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente)
		want(t, rec, http.StatusCreated)
		secondPaymentID := jsonMap(t, rec)["id"].(string)
		if secondPaymentID == firstPaymentID {
			t.Fatal("el reintento reutilizó el mismo intento de pago en vez de crear uno nuevo")
		}

		want(t, h.postPaymentWebhook(secondPaymentID, "ref-r2", "tx-r2", "30.00", "Aceptada", nil), http.StatusOK)
		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "confirmed" {
			t.Fatalf("status final = %v, quería confirmed", got)
		}

		// Ambos intentos quedan en el historial del pedido, uno rechazado y otro exitoso — no
		// pagina, es una lista de a lo sumo un puñado de intentos por pedido.
		rec = h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &admin)
		want(t, rec, http.StatusOK)
		list := decode[[]any](t, rec)
		if len(list) != 2 {
			t.Fatalf("intentos de pago = %v, quería 2", list)
		}
	})
}

func TestListarPagosDeUnPedido(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	adminOtra, otra := orgAdmin(h, "Otra")
	empleado := h.user()
	h.join(empleado, o, "employee")
	cliente := h.user()
	orderID := placeSimpleOrder(h, admin, o, cliente)
	h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), nil, &cliente)

	// org.payments.read no es delegable y no se lo dimos a employee: es información contable.
	want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &empleado), http.StatusForbidden)
	want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &admin), http.StatusOK)

	// El staff de otra organización no ve los pagos de este pedido.
	want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &adminOtra), http.StatusForbidden)
	want(t, h.do(http.MethodGet, ordersOrgURL(otra, "/"+orderID+"/payments"), nil, &adminOtra), http.StatusNotFound)
}
