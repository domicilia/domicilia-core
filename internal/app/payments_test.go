package app_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/config"
)

// placeSimpleOrder arma un carrito de una gaseosa ($30 local → $33 publicado con el 10 %) y lo
// confirma; devuelve el id del pedido.
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

// quote pide el desglose del pago de un pedido con un medio.
func quote(h *harness, o organization, orderID string, cliente person, method string) map[string]any {
	h.t.Helper()
	rec := h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payment-quote?method="+method), nil, &cliente)
	want(h.t, rec, http.StatusOK)
	return jsonMap(h.t, rec)
}

// startPayment cotiza, acepta el total y paga: devuelve la respuesta del checkout, el pago y el
// monto en la notación decimal que usaría ePayco en su webhook ("880.00").
func startPayment(h *harness, o organization, orderID string, cliente person, method string) (checkout, payment map[string]any, amount string) {
	h.t.Helper()
	q := quote(h, o, orderID, cliente, method)
	total := q["total_cents"].(float64)
	rec := h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), map[string]any{
		"method": method, "expected_total_cents": total,
	}, &cliente)
	want(h.t, rec, http.StatusCreated)
	checkout = jsonMap(h.t, rec)
	payment = checkout["payment"].(map[string]any)
	return checkout, payment, fmt.Sprintf("%.2f", total/100)
}

func TestIniciarUnPago(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	orderID := placeSimpleOrder(h, admin, o, cliente)

	q := quote(h, o, orderID, cliente, "card")
	// $33 de comida, sin domicilio: la base es $33 y el total la "despeja" sobre la tarifa de
	// ePayco (2,64 % + $690 + IVA) — el fijo pesa mucho en un pedido tan chico.
	if q["subtotal_cents"] != float64(3300) || q["base_cents"] != float64(3300) {
		t.Fatalf("cotización = %v", q)
	}
	total := q["total_cents"].(float64)
	if total <= 3300 || q["transaction_fee_cents"] != total-3300 || int(total)%100 != 0 {
		t.Fatalf("total/costo de transacción inconsistentes: %v", q)
	}

	t.Run("el total aceptado debe coincidir con el calculado", func(t *testing.T) {
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), map[string]any{
			"method": "card", "expected_total_cents": total - 100,
		}, &cliente), http.StatusConflict)
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), map[string]any{
			"method": "bitcoin", "expected_total_cents": total,
		}, &cliente), http.StatusUnprocessableEntity)
	})

	checkout, pay, amount := startPayment(h, o, orderID, cliente, "card")
	if checkout["type"] != "onpage" || checkout["session_id"] == "" || checkout["test_mode"] != true {
		t.Fatalf("checkout = %v", checkout)
	}
	if pay["gateway"] != "epayco" || pay["status"] != "pending" || pay["amount_cents"] != total || pay["method"] != "card" {
		t.Fatalf("pago iniciado = %v", pay)
	}
	if url, _ := pay["checkout_url"].(string); url == "" {
		t.Fatal("checkout_url vacío: se necesita como respaldo y para el link de WhatsApp")
	}
	session := h.epayco.lastSession()
	if session["invoice"] != pay["id"] || session["currency"] != "COP" || session["splitPayment"] != nil {
		t.Fatalf("sesión enviada a ePayco = %v", session)
	}
	if fmt.Sprintf("%v", session["amount"]) != fmt.Sprintf("%.0f", total/100) {
		t.Fatalf("monto de la sesión = %v, quería %v", session["amount"], total/100)
	}

	t.Run("un carrito (draft) no se puede pagar", func(t *testing.T) {
		// Mismo criterio que GetMyOrder (ver TestAislamientoYPermisosDePedidos): un draft no es
		// un pedido propio todavía, así que "pagarlo" da 404, no 409.
		cart := jsonMap(t, h.do(http.MethodGet, cartURL(o, ""), nil, &cliente))
		want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+cart["id"].(string)+"/payment-quote?method=card"), nil, &cliente), http.StatusNotFound)
	})

	t.Run("solo el dueño del pedido puede cotizar o pagar", func(t *testing.T) {
		extrano := h.user()
		want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payment-quote?method=card"), nil, &extrano), http.StatusNotFound)
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), map[string]any{
			"method": "card", "expected_total_cents": total,
		}, &extrano), http.StatusNotFound)
	})

	t.Run("un pedido ya pagado no admite un cobro nuevo", func(t *testing.T) {
		want(t, h.postPaymentWebhook(pay["id"].(string), "ref-y", "tx-y", amount, "Aceptada", nil), http.StatusOK)
		want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payment-quote?method=card"), nil, &cliente), http.StatusConflict)
	})
}

func TestSiEpaycoFallaElPagoSaleARedireccion(t *testing.T) {
	h := newHarness(t)
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	orderID := placeSimpleOrder(h, admin, o, cliente)
	h.epayco.setFail(true)

	checkout, pay, _ := startPayment(h, o, orderID, cliente, "pse")
	if checkout["type"] != "redirect" || pay["checkout_url"] == nil || pay["session_id"] != nil {
		t.Fatalf("con ePayco caído el cobro debía quedar con redirección: %v", checkout)
	}
}

func TestPasarelaNoConfigurada(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.EpaycoPublicKey = "" })
	admin, o := orgAdmin(h, "Acme")
	cliente := h.user()
	orderID := placeSimpleOrder(h, admin, o, cliente)
	total := quote(h, o, orderID, cliente, "card")["total_cents"]

	want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), map[string]any{
		"method": "card", "expected_total_cents": total,
	}, &cliente), http.StatusServiceUnavailable)
}

func TestWebhookDePagos(t *testing.T) {
	t.Run("una firma inválida se rechaza y no toca el pedido", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		orderID := placeSimpleOrder(h, admin, o, cliente)
		_, pay, amount := startPayment(h, o, orderID, cliente, "card")

		mala := "0000000000000000000000000000000000000000000000000000000000000000"
		want(t, h.postPaymentWebhook(pay["id"].(string), "ref-x", "tx-x", amount, "Aceptada", &mala), http.StatusUnauthorized)

		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "placed" {
			t.Fatalf("un webhook con firma inválida cambió el pedido: status = %v", got)
		}
	})

	t.Run("un pago aprobado por OTRO monto no confirma el pedido", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		orderID := placeSimpleOrder(h, admin, o, cliente)
		_, pay, _ := startPayment(h, o, orderID, cliente, "card")

		want(t, h.postPaymentWebhook(pay["id"].(string), "ref-m", "tx-m", "1.00", "Aceptada", nil), http.StatusOK)
		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "payment_failed" {
			t.Fatalf("status = %v, quería payment_failed", got)
		}
		list := decode[[]any](t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &admin))
		if p := list[0].(map[string]any); p["status"] != "failed" || p["failure_reason"] == nil {
			t.Fatalf("pago = %v", p)
		}
	})

	t.Run("pagar con otro medio que el declarado queda marcado para conciliación", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		orderID := placeSimpleOrder(h, admin, o, cliente)
		_, pay, amount := startPayment(h, o, orderID, cliente, "pse")

		// Declaró PSE y pagó con Visa: el cobro igual vale (lo aprobó la pasarela), pero queda
		// marcado — con tarjeta la tarifa es otra y la plataforma pudo quedar corta.
		want(t, h.postPaymentWebhookWith(pay["id"].(string), "ref-f", "tx-f", amount, "Aceptada", nil,
			map[string]string{"x_franchise": "VS"}), http.StatusOK)
		list := decode[[]any](t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &admin))
		p := list[0].(map[string]any)
		if p["status"] != "succeeded" || p["method_used"] != "VS" || p["method_mismatch"] != true {
			t.Fatalf("pago = %v", p)
		}
	})

	t.Run("un pago rechazado se puede reintentar sobre el MISMO pedido", func(t *testing.T) {
		h := newHarness(t)
		admin, o := orgAdmin(h, "Acme")
		cliente := h.user()
		orderID := placeSimpleOrder(h, admin, o, cliente)
		_, pay, amount := startPayment(h, o, orderID, cliente, "card")
		firstPaymentID := pay["id"].(string)

		want(t, h.postPaymentWebhook(firstPaymentID, "ref-r", "tx-r", amount, "Rechazada", nil), http.StatusOK)
		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "payment_failed" {
			t.Fatalf("status = %v, quería payment_failed", got)
		}

		// Sin reintentar el pago, no se puede pagar de nuevo: sigue sin estar en placed.
		want(t, h.do(http.MethodPost, ordersOrgURL(o, "/"+orderID+"/pay"), map[string]any{
			"method": "card", "expected_total_cents": 1,
		}, &cliente), http.StatusConflict)

		want(t, h.do(http.MethodPost, "/v1/orders/"+orderID+"/retry-payment", nil, &cliente), http.StatusOK)
		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "placed" {
			t.Fatalf("tras reintentar, status = %v, quería placed", got)
		}

		_, pay2, amount2 := startPayment(h, o, orderID, cliente, "card")
		secondPaymentID := pay2["id"].(string)
		if secondPaymentID == firstPaymentID {
			t.Fatal("el reintento reutilizó el mismo intento de pago en vez de crear uno nuevo")
		}

		want(t, h.postPaymentWebhook(secondPaymentID, "ref-r2", "tx-r2", amount2, "Aceptada", nil), http.StatusOK)
		if got := jsonMap(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID), nil, &admin))["status"]; got != "confirmed" {
			t.Fatalf("status final = %v, quería confirmed", got)
		}

		// Ambos intentos quedan en el historial del pedido, uno rechazado y otro exitoso — no
		// pagina, es una lista de a lo sumo un puñado de intentos por pedido.
		rec := h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &admin)
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
	startPayment(h, o, orderID, cliente, "card")

	// org.payments.read no es delegable y no se lo dimos a employee: es información contable.
	want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &empleado), http.StatusForbidden)
	want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &admin), http.StatusOK)

	// El staff de otra organización no ve los pagos de este pedido.
	want(t, h.do(http.MethodGet, ordersOrgURL(o, "/"+orderID+"/payments"), nil, &adminOtra), http.StatusForbidden)
	want(t, h.do(http.MethodGet, ordersOrgURL(otra, "/"+orderID+"/payments"), nil, &adminOtra), http.StatusNotFound)
}
