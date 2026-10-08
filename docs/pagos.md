# Módulo de pagos — v2.1 (comisiones, checkout onpage de ePayco y split)

> **Estado (2026-10-08): fase 2.1 IMPLEMENTADA y probada en local, sin desplegar.** §1 dice qué
> existe y dónde; §14–§17 lo que falta, los vacíos, las preguntas abiertas y la deuda técnica.
> Lo marcado **⏳** depende de alguien externo (contador, abogado, ePayco).
>
> Reemplaza el diseño v3 de este mismo archivo (commit 4a24089) y el "modelo de custodia v2" de
> `ecommerce.md` §0. El resto de `ecommerce.md` (catálogo, pedidos, promociones) sigue vigente.
> Diseños relacionados: [`domicilios.md`](domicilios.md) (GPS, rutas y precio del domicilio).

## Índice

0. [Resumen](#0-resumen)
1. [Qué se implementó en la fase 2.1](#1-qué-se-implementó-en-la-fase-21)
2. [Decisiones tomadas](#2-decisiones-tomadas)
3. [Vocabulario](#3-vocabulario)
4. [Algoritmo de cobro](#4-algoritmo-de-cobro)
5. [Ejemplo completo](#5-ejemplo-completo)
6. [Modelo de datos](#6-modelo-de-datos)
7. [Flujos](#7-flujos)
8. [API](#8-api)
9. [Interfaz del superadmin](#9-interfaz-del-superadmin)
10. [ePayco: checkout onpage y split](#10-epayco-checkout-onpage-y-split)
11. [Seguridad](#11-seguridad)
12. [Pruebas](#12-pruebas)
13. [Cómo probarlo en local](#13-cómo-probarlo-en-local)
14. [Lo que falta (siguientes fases)](#14-lo-que-falta-siguientes-fases)
15. [Vacíos conocidos](#15-vacíos-conocidos)
16. [Preguntas abiertas](#16-preguntas-abiertas)
17. [Deuda técnica](#17-deuda-técnica)
18. [Pagos en efectivo (diseño pendiente)](#18-pagos-en-efectivo-diseño-pendiente)
19. [Evaluado para la próxima versión](#19-evaluado-para-la-próxima-versión)

---

## 0. Resumen

- **Una sola pasarela en esta fase: ePayco**, con su **checkout onpage** (checkout-v2): el pago se
  abre **encima de nuestra página**, el cliente no sale de Domicilia y nunca escribimos ni vemos un
  número de tarjeta.
- **Reparto en el mismo cobro (split de ePayco)**: el restaurante recibe su parte y Domicilia su
  comisión, sin que el dinero del restaurante pase por la cuenta de Domicilia. Está **implementado
  pero apagado** hasta que ePayco lo active en la cuenta (⏳).
- **Nada quemado en el código**: comisiones, planes de tarifa de ePayco y domicilio los edita el
  superadmin desde `/platform/comisiones`, con vista general y por organización, y cada cambio se
  audita.
- **El cliente elige el medio de pago antes de pagar**, ve el **costo de transacción** de ese medio
  y lo **acepta**; el core vuelve a calcular y cobra exactamente lo aceptado.
- Wompi, Mercado Pago, Bold y PayU quedan para después (§19): Mercado Pago no sirve para el reparto
  a tres partes (su split es 1:1) y Wompi solo dispersa después del cobro ("payouts").

## 1. Qué se implementó en la fase 2.1

### Backend (`domicilia-core`)

| Pieza | Dónde | Qué hace |
|---|---|---|
| Algoritmo de cobro | `internal/pricing/pricing.go` | Precio publicado, promoción, despeje del costo de transacción por medio de pago, reparto. Aritmética entera exacta, sin base de datos |
| Pruebas del algoritmo | `internal/pricing/pricing_test.go` | Ejemplo del documento, PSE alrededor del umbral, mínimo garantizado, entradas inválidas |
| Configuración y API | `internal/pricing/service.go`, `handler.go` | Tarifas efectivas por organización, vista general, edición auditada, simulador |
| Migración | `db/migrations/00010_pricing.sql` | `gateway_fee_plans`, `pricing_settings`, `organization_pricing`; columnas nuevas en `products`, `order_items`, `orders`, `payments` |
| Catálogo | `internal/catalog` | `promo_discount_bps` en productos; el feed y el detalle públicos devuelven el **precio publicado** |
| Pedidos | `internal/orders` | Cada línea guarda precio publicado y parte local; el carrito muestra el domicilio; al confirmar se congelan comisión, domicilio y tarifas |
| Pagos | `internal/payments` | Cotización por medio, pago con total aceptado (409 si cambió), sesión onpage, split, verificación de monto, detección de medio distinto |
| ePayco | `internal/epayco/session.go`, `epayco.go` | Login + creación de sesión (apify), `splitPayment`, lectura de `x_amount` y `x_franchise` |
| Auditoría | `internal/audit` | `pricing.settings_changed`, `pricing.gateway_plan_changed`, `organization.pricing_changed` |
| Contrato | `openapi/openapi.yaml` | `payment-quote`, nuevo cuerpo y respuesta de `pay`, campos nuevos de `Payment` |

### Frontend (`domicilia-frontend`)

| Pieza | Dónde | Qué hace |
|---|---|---|
| Panel de pago | `src/lib/components/orders/PaymentPanel.svelte` | Elegir medio, ver desglose y costo de transacción, aceptar, pagar |
| Checkout onpage | `src/lib/payments/epayco.ts` | Carga `checkout-v2.js` y abre la sesión creada por el core |
| Detalle del pedido | `src/routes/cliente/(app)/pedidos/[orderId]` | Usa el panel; tras cerrar el checkout consulta el estado real |
| Comisiones (general) | `src/routes/platform/(consola)/comisiones` | Configuración, planes de ePayco, lista de organizaciones, simulador |
| Comisiones (por organización) | `src/routes/platform/(consola)/comisiones/[orgId]` | Heredar o sobrescribir cada valor, plan por banco, id de receptor del split, simulador |
| Simulador | `src/lib/components/platform/PricingSimulator.svelte` | Mismo algoritmo, calculado por el core |
| Producto del restaurante | `src/lib/components/admin/ProductFormDialog.svelte` | "Precio local", vista previa del publicado, descuento de promoción |
| Tarjeta de producto | `src/lib/components/cliente/ProductCard.svelte` | Precio tachado y % cuando hay promoción |
| API | `src/lib/api/pricing.ts`, `orders.ts` | Tipos y llamadas |

## 2. Decisiones tomadas

1. **Módulo dentro del core, en Go. Sin SDK y sin microservicio** (análisis completo en §19). El
   SDK de ePayco es una capa delgada sobre su API HTTP; el core llama esa API directamente.
2. **Checkout onpage** (forma 2): ePayco aloja el formulario dentro de nuestra página. Nada de API
   directa ni tokenización en esta fase (alcance PCI mínimo).
3. **ePayco primero y único**: es la única con split a varios receptores en el mismo cobro.
4. **Modelo agregador**: Domicilia y cada restaurante con cuenta en ePayco (no código de adquirencia
   propio).
5. **Precio publicado = precio local × (1 + comisión)**. El restaurante escribe su precio local;
   el cliente ve y paga el publicado. La comisión la paga el cliente dentro del precio.
6. **Promoción de producto**: el restaurante descuenta sobre su precio local y la comisión de la
   plataforma baja (5 % por defecto).
7. **El cliente paga el costo de transacción**, calculado con la tarifa del medio que elige,
   informado y aceptado antes de pagar (Estatuto del Consumidor: sin cargos sorpresa).
8. **El plan de ePayco (Davivienda u otro banco) se configura por organización**: con otro banco la
   tarifa es mayor y la asume el cliente, no la plataforma.
9. **Sin redondeo comercial**: se cobra el valor real, redondeado solo al peso (ePayco cobra en
   pesos). El total, siempre hacia arriba para no quedar cortos por una fracción.
10. **Dinero en centavos enteros** y porcentajes en puntos básicos. Nunca `float` en el core.
11. **Cada pedido y cada pago guardan una copia de las tarifas** con que se calcularon.
12. **El domicilio no lleva comisión de la plataforma**; la plataforma puede cobrar un % al
    domiciliario (configurable, 0 % por defecto ⏳ abogado).
13. **Webhooks bajo `/webhooks/*`** (lo que Caddy envía al core).
14. **"Modelo de recaudo", nunca "plan"**: `internal/plans` ya son los planes comerciales del SaaS.

## 3. Vocabulario

| Término | Significado |
|---|---|
| **Precio local** | Lo que el restaurante cobra en su local; es lo que escribe en el catálogo (`price_cents`) |
| **Precio publicado** | Lo que ve y paga el cliente: local + comisión. Se calcula al leer, nunca se guarda en el catálogo |
| **Comisión de la plataforma** | 10 % por defecto (`platform_fee_bps`); 5 % en promoción (`promo_platform_fee_bps`) |
| **Base** | Productos − descuento de código + domicilio: lo que debe quedar después de la pasarela |
| **Costo de transacción** | Lo que se suma a la base para cubrir la tarifa de la pasarela + IVA |
| **Plan de la pasarela** | Tarifas de ePayco según el banco de la cuenta (`gateway_fee_plans`) |
| **Medio de pago** | `card` (tarjeta nacional), `card_international`, `pse`, `wallet` (Nequi/Daviplata) |
| **Split** | Reparto automático del cobro en ePayco entre Domicilia y el restaurante |

## 4. Algoritmo de cobro

Todo vive en `internal/pricing/pricing.go`: catálogo, carrito, pago y simulador usan **las mismas
funciones**.

```
1. Por cada línea (variante + modificadores):
     local     = precio_local × (1 − descuento_promo)          (si el producto está en promoción)
     publicado = local × (1 + comisión)                         comisión = 10 % normal, 5 % promo
     (cada parte se redondea al peso, mitad hacia arriba)
2. subtotal_publicado = Σ publicado × cantidad
   subtotal_local     = Σ local × cantidad                       → restaurante
   comisión           = subtotal_publicado − subtotal_local      → Domicilia
3. domicilio (tarifa plana configurable)                          → domiciliario − su %
4. base = subtotal_publicado − descuento_de_código + domicilio
5. el cliente elige el medio → regla del plan de la organización:
     tarjeta nacional:       pct + fijo
     tarjeta internacional:  (pct + recargo) + fijo
     Nequi/Daviplata:        pct + fijo                          (⏳ confirmar tarifa)
     PSE:                    si total < umbral → fijo pequeño; si no → pct + fijo
6. total = menor peso entero T tal que  T − (T·pct + fijo)·(1 + IVA) ≥ base
     T ≥ (base + fijo·(1+IVA)) / (1 − pct·(1+IVA))
   costo_transacción = total − base
7. se muestra el desglose → el cliente acepta → se cobra exactamente "total"
```

**Por qué "despejar"**: ePayco cobra su porcentaje **sobre el total**, que ya incluye su propia
tarifa. Sumar "2,64 % del subtotal" deja a la plataforma corta. El despeje encuentra el total que,
después de que ePayco descuente su tarifa + IVA, deja exactamente la base.

**Por qué 1,19**: el IVA del 19 % sobre la tarifa de ePayco ("todos los valores son más IVA").

**PSE alrededor de $60.000**: bajo el umbral la tarifa es fija y por encima porcentual; el algoritmo
elige el total cuyo neto, con la regla que **realmente** aplica a ese total, cubre la base (probado
con un barrido de bases en `TestPSEAlrededorDelUmbral`).

**Descuento de código** (promociones existentes, `internal/promotions`): sale de la parte del
restaurante (supuesto, ⏳ §16). Si fuera mayor que esa parte, el exceso sale de la comisión.

## 5. Ejemplo completo

2 hamburguesas (local $20.000) + 1 gaseosa (local $4.000), domicilio $5.000, comisión 10 %,
domiciliario 5 %:

| Paso | Cálculo | Valor |
|---|---|---|
| Precio publicado | 20.000 × 1,10 = 22.000 · 4.000 × 1,10 = 4.400 | |
| Subtotal publicado | 2 × 22.000 + 4.400 | $48.400 |
| Subtotal local (restaurante) | 2 × 20.000 + 4.000 | $44.000 |
| Comisión Domicilia | 48.400 − 44.000 | $4.400 |
| Base | 48.400 + 5.000 | $53.400 |
| **Total, tarjeta, plan Davivienda** | (53.400 + 690 × 1,19) / (1 − 0,0264 × 1,19) | **$55.980** |
| Total, PSE (< $60.000) | 53.400 + 2.200 × 1,19 | $56.018 |
| Total, tarjeta internacional | 3,44 % + $690 | $56.536 |
| Total, tarjeta, plan otros bancos | 3,29 % + $700 | $56.443 |
| Reparto | restaurante $44.000 · Domicilia $4.400 + $250 del domicilio · domiciliario $4.750 · ePayco $2.580 | |

Estos números están fijados en `TestQuoteEjemploDelDocumento` y `TestDomicilioYCostoDeTransaccion`.

## 6. Modelo de datos

Migración `00010_pricing.sql`:

| Tabla / columna | Qué guarda |
|---|---|
| `gateway_fee_plans` | Planes de ePayco: tarjeta, internacional, billeteras, PSE (con umbral), IVA, notas. Sembrados desde epayco.com/tarifas (2026-10-08) |
| `pricing_settings` (una fila) | Comisión, comisión de promoción, % al domiciliario, domicilio, plan por defecto, split activo |
| `organization_pricing` | Lo propio de cada organización (NULL = hereda) + `epayco_merchant_id` (receptor del split) |
| `products.promo_discount_bps` | Descuento de promoción del restaurante (0–90 %) |
| `order_items.unit_local_total_cents`, `platform_fee_bps` | Parte local y comisión de cada línea |
| `orders.subtotal_local_cents`, `platform_fee_cents`, `courier_fee_cents`, `pricing_snapshot` | Congelados al confirmar |
| `payments.method`, `base_cents`, `transaction_fee_cents`, `gateway_plan_code`, `breakdown`, `session_id`, `method_used`, `method_mismatch` | Medio declarado, desglose aceptado, sesión onpage, conciliación del medio |

## 7. Flujos

### 7.1 Pago en el e-commerce (implementado)

```
Cliente         Frontend                         Core                              ePayco
  │ elige medio ─▶ GET payment-quote?method= ──▶ desglose (mismo algoritmo)
  │ ve el costo de transacción y ACEPTA
  │ Pagar ───────▶ POST pay {method, expected} ─▶ ¿total == aceptado? (si no, 409)
  │                                               crea payment (pending) + desglose
  │                                               URL de redirección (respaldo)
  │                                               login + session/create (+ splitPayment) ─▶
  │               ◀─ {type: onpage, session_id} ◀─
  │ checkout-v2.js abre la ventana de ePayco ENCIMA de la página ─────────────────────▶
  │ paga (tarjeta, PSE, Nequi…)                                                        │
  │                                               ◀──────── webhook /webhooks/payments ─┤
  │                                               firma, idempotencia, monto, medio usado
  │                                               pedido → confirmed (o payment_failed)
  │ cierra la ventana → la página consulta el pedido hasta ver el resultado
```

Si ePayco no responde al crear la sesión, el pago **no se pierde**: la respuesta es
`type: redirect` y el cliente va a la página de ePayco con la URL firmada.

### 7.2 Link de cobro por WhatsApp (pendiente, §14)

La URL de redirección que ya se arma en cada pago es la base del link. Falta: token propio
`/pagar/{token}` con expiración, botón en el inbox y pedidos a nombre de un contacto de WhatsApp.

### 7.3 Medio declarado ≠ medio usado

El checkout de ePayco **deja elegir otro medio** dentro de su ventana y no encontramos (en su
documentación pública) cómo restringirlo por sesión. Mitigación implementada:

1. El medio declarado viaja como `extra2` de la sesión.
2. El webhook trae `x_franchise`; si no corresponde al medio declarado, el pago queda con
   `method_mismatch = true` (el cobro vale: lo aprobó ePayco).
3. Una tarjeta internacional no se distingue de una nacional por la franquicia: si se declaró
   internacional y se pagó con tarjeta, no se marca (el cliente pagó de más, no de menos).

Pendiente (⏳ ePayco): restringir el medio en la sesión. Si no se puede, falta el reporte de
discrepancias para medir cuánto cuestan (§14).

### 7.4 Webhook

- Firma SHA-256 de ePayco (igual que antes).
- **Nuevo**: si `x_amount` no coincide con el monto del pago, el pago se marca `failed` con el
  motivo y el pedido **no** se confirma.
- **Nuevo**: `x_franchise` → `method_used` y `method_mismatch`.

## 8. API

| Método y ruta | Quién | Qué hace |
|---|---|---|
| `GET /v1/organizations/{org}/orders/{id}/payment-quote?method=` | Cliente dueño | Desglose con el costo de transacción del medio |
| `POST /v1/organizations/{org}/orders/{id}/pay` | Cliente dueño | Cuerpo `{method, expected_total_cents}`. 409 si el total cambió. Responde `{payment, type, session_id, test_mode, quote}` |
| `GET /v1/organizations/{org}/orders/{id}/payments` | `org.payments.read` | Intentos de cobro (con medio usado y discrepancia) |
| `GET /v1/organizations/{org}/pricing` | `org.catalog.read` | Comisiones de la organización (para la vista previa del precio publicado) |
| `GET /v1/platform/pricing` | `platform.organizations.manage` | Configuración, planes, organizaciones, medios |
| `PUT /v1/platform/pricing/settings` | ídem | Configuración general |
| `PUT /v1/platform/pricing/plans/{code}` | ídem | Tarifas de un plan de ePayco |
| `POST /v1/platform/pricing/simulate` | ídem | Simulación (líneas, domicilio, organización opcional) |
| `GET /v1/platform/organizations/{org}/pricing` | ídem | Lo propio y lo efectivo de una organización |
| `PUT /v1/platform/organizations/{org}/pricing` | ídem | Reemplaza lo propio (cada campo null = hereda) |
| `POST /webhooks/payments` | ePayco | Confirmación firmada |

`payment-quote` y `pay` están en `openapi/openapi.yaml` (la prueba de contrato los exige). Las rutas
de tarifas no (ver §17).

## 9. Interfaz del superadmin

- **`/platform/comisiones`** — vista general:
  - configuración: comisión, comisión de promoción, % al domiciliario, domicilio, plan por defecto,
    interruptor del split (con aviso);
  - planes de ePayco editables (tarjeta, internacional, billeteras, PSE con umbral, IVA, notas);
  - lista de organizaciones con lo que cada una tiene propio;
  - simulador.
- **`/platform/comisiones/{orgId}`** — una organización: cada campo con "propio" o "hereda", plan
  por banco, id de receptor del split y simulador con sus tarifas efectivas.

Cada cambio aplica a **pedidos nuevos** y queda en la auditoría.

## 10. ePayco: checkout onpage y split

Documentación consultada el 2026-10-08:
[implementación del checkout](https://docs.epayco.com/docs/checkout-implementacion) ·
[pagos divididos](https://docs.epayco.com/docs/split-descripcion) ·
[respuesta y confirmación](https://docs.epayco.com/docs/checkout-respuesta-y-confirmacion.md) ·
[tarifas](https://epayco.com/tarifas/).

| Paso | Cómo |
|---|---|
| Token | `POST {apify}/login` con Basic auth (llave pública : llave privada) |
| Sesión | `POST {apify}/payment/session/create` con `Bearer`: monto en pesos, `invoice` = id de nuestro pago, URLs de confirmación y respuesta, `extras`, `splitPayment` |
| Navegador | `checkout-v2.js` → `ePayco.checkout.configure({sessionId, type: "onpage", test}).open()` |
| Split | `splitPayment: {type, receivers: [{merchantId, amount, taxBase, tax, fee}]}`; el restaurante recibe su subtotal local; Domicilia (cuenta principal) el resto |
| Configuración | `CORE_EPAYCO_PUBLIC_KEY`, `CORE_EPAYCO_PRIVATE_KEY`, `CORE_EPAYCO_CUSTOMER_ID`, `CORE_EPAYCO_TEST_MODE`, `CORE_EPAYCO_APIFY_URL` (por defecto `https://apify.epayco.co`) |

**VERIFICAR CONTRA EL SANDBOX antes de producción** (marcado también en el código):
- forma exacta de la respuesta de `login` y `session/create` (se aceptan `{token}`/`{data:{token}}`
  y `{sessionId}`/`{data:{sessionId}}`);
- el valor de `splitPayment.type` para montos fijos (se usa `"fixed"`; la documentación muestra
  `"percentage"`);
- los códigos de `x_franchise` (PSE, NEQUI/NQ, DAVIPLATA/DP; el resto se toma como tarjeta);
- que la confirmación de checkout-v2 use los mismos campos `x_*` y la misma firma que el clásico.

## 11. Seguridad

- **Sin datos de tarjeta** en ningún sistema de Domicilia (checkout alojado por ePayco).
- La sesión la crea el **servidor** (la llave privada nunca llega al navegador).
- **El monto lo pone el servidor** y el cliente debe aceptar exactamente ese total.
- **El monto del webhook se compara** con el del pago: una confirmación por otro monto no confirma
  el pedido.
- Firma del webhook, idempotencia por evento, `Settle` solo desde `pending` (sin cambios).
- Las comisiones las cambia solo el superadmin, y cada cambio se audita.
- Los logs de ePayco no incluyen el cuerpo de la petición (datos del cliente); de la respuesta, solo
  un recorte.

## 12. Pruebas

| Prueba | Qué cubre |
|---|---|
| `internal/pricing/pricing_test.go` | Publicación y redondeo, promoción, ejemplo del documento (4 medios, 2 planes), split, descuento de código, PSE alrededor del umbral, total mínimo garantizado, entradas inválidas |
| `internal/app/pricing_test.go` | Permisos (solo plataforma), precio publicado en catálogo y feed, override y herencia, auditoría, pedido congelado ante cambios de tarifa, promoción, domicilio y costo por medio, plan por organización, split en la sesión, simulador |
| `internal/app/payments_test.go` | Cotizar y pagar, 409 por total distinto, sesión onpage, respaldo por redirección si ePayco falla, monto distinto en el webhook, medio distinto, reintento, permisos |
| `internal/app/contract_test.go` | El OpenAPI coincide con lo servido |
| `src/routes/smoke.e2e.ts` (frontend) | Las páginas públicas siguen sirviéndose |

**Importante en esta máquina**: las pruebas de integración del core se OMITEN en silencio si
testcontainers no arranca. Correrlas siempre así:

```bash
TESTCONTAINERS_RYUK_DISABLED=true TESTS_DB_REQUIRED=1 go test ./internal/... -count=1
```

## 13. Cómo probarlo en local

1. `make migrate-core` (devstack) para aplicar `00010_pricing`.
2. Entrar como superadmin a `http://localhost:3000/platform/comisiones`: revisar la configuración,
   fijar un domicilio, simular.
3. Como admin de un restaurante, editar un producto: ver "Precio local → publicado" y probar un
   descuento de promoción.
4. Como cliente: armar un carrito, confirmar, elegir un medio y ver el costo de transacción.
5. Pagar **requiere credenciales de pruebas de ePayco** (`CORE_EPAYCO_*` en el `.env` del core, modo
   pruebas). Sin ellas el core responde 503 al pagar (la cotización sí funciona).

## 14. Lo que falta (siguientes fases)

| # | Qué | Depende de |
|---|---|---|
| F1 | **Probar contra el sandbox de ePayco**: login, sesión, onpage real, webhook de checkout-v2, códigos de franquicia | Cuenta de pruebas ⏳ |
| F2 | **Activar el split**: ticket a ePayco, registrar a Domicilia como aplicación y a cada restaurante como receptor, probar `splitPayment` | ePayco ⏳ |
| F3 | **Conciliación**: tarea periódica que consulta el estado de los pagos `pending` (si el webhook no llegó) y guarda la tarifa real que cobró ePayco | F1 |
| F4 | **Reporte de discrepancias de medio** (`method_mismatch`) y de diferencias entre tarifa esperada y real | F3 |
| F5 | **Link de cobro por WhatsApp**: `/pagar/{token}` con expiración, botón en el inbox, pedidos a nombre de un contacto | — |
| F6 | **Reembolsos** (cancelación o rechazo de un pedido pagado) y su plazo en los términos | ⏳ D-reembolso |
| F7 | **Pagos en efectivo** (§18) | Decisión de negocio |
| F8 | **Domicilio por ruta** en vez de tarifa plana (`domicilios.md`) | Diseño GPS |
| F9 | **Liquidación al domiciliario** (hoy el domicilio queda en la parte de la plataforma) | F2, `domicilios.md` |
| F10 | **Pantallas de pagos** para el restaurante (lo cobrado, comisiones, discrepancias) | F3 |
| F11 | **Agente de IA** cobrando por WhatsApp (usa F5) | Autenticación de servicio |

## 15. Vacíos conocidos

1. **El medio de pago no se puede restringir** en la ventana de ePayco (según su documentación
   pública): un cliente puede declarar PSE y pagar con tarjeta. Se detecta (§7.3), no se impide.
2. **Tarjeta internacional indetectable** antes de pagar; si se declara nacional y es extranjera,
   la plataforma pierde el recargo (+0,8 %).
3. **El domiciliario no está en el split**: al pagar todavía no hay domiciliario asignado. El
   domicilio queda en la parte de la plataforma y debe liquidarse aparte (F9).
4. **Sin split activado, todo el cobro llega a la cuenta de la plataforma**: es el modelo que se
   quiere evitar por temas fiscales (§16). No usar en producción con dinero real hasta activar F2 o
   decidir otra cosa con el contador.
5. **Tarifas de Nequi/Daviplata y de PSE ≥ $60.000 no están publicadas**: se sembraron con la
   tarifa de tarjeta.
6. **No hay reembolsos**: si el restaurante rechaza un pedido pagado, hoy no se devuelve nada
   automáticamente.
7. **La cotización usa las tarifas vigentes al pagar**, no las del momento de confirmar el pedido:
   si el superadmin cambia un plan entre confirmar y pagar, el costo de transacción cambia (el
   cliente lo ve y lo acepta igual; la comisión y el domicilio sí quedan congelados).
8. **El cliente ve en el JSON del pedido la parte del restaurante y la comisión** (no se muestran en
   la interfaz).

## 16. Preguntas abiertas

| # | Pregunta | Para |
|---|---|---|
| Q1 | ¿Sigo como **no responsable de IVA** (persona natural, ingresos y consignaciones < 3.500 UVT = $183.309.000 en 2026) con este modelo? | Contador |
| Q2 | La comisión del 10 %, ¿lleva IVA? ¿Cómo se factura y a quién (al restaurante o al cliente)? | Contador |
| Q3 | ¿Qué **retenciones** (renta, IVA, ICA) aplican a cada receptor del split? | Contador / ePayco |
| Q4 | ¿Es válido cobrar al cliente el costo de transacción como se muestra (informado y aceptado antes)? | Abogado |
| Q5 | ¿Cobrar 3–5 % al domiciliario? Reforma laboral de 2025 y repartidores de plataformas | Abogado |
| Q6 | En un split, **¿quién paga la tarifa de ePayco y con el plan de qué cuenta?** ¿Hay costo extra por el split o por receptor? | ePayco |
| Q7 | ¿Se puede **restringir el medio de pago por sesión** o bloquear tarjetas internacionales? | ePayco |
| Q8 | Tarifas de **Nequi, Daviplata y PSE ≥ $60.000** | ePayco |
| Q9 | Requisitos para que restaurantes y domiciliarios (personas naturales) sean **receptores** | ePayco |
| Q10 | Las **promociones con código**, ¿las asume el restaurante (supuesto actual) o se reparten? | Tú |
| Q11 | **Plazo de reembolso** a publicar en los términos | Tú + ePayco |

## 17. Deuda técnica

| # | Qué | Por qué se dejó así |
|---|---|---|
| T1 | Las rutas de tarifas usan `platform.organizations.manage`; falta un permiso propio `platform.payments.manage` | Evitar una migración de permisos en esta fase |
| T2 | Las rutas `/platform/pricing*` y `/organizations/{org}/pricing` no están en `openapi.yaml` | La prueba de contrato solo vigila un conjunto de prefijos; ampliar el patrón y documentarlas |
| T3 | La vista previa del precio publicado en el formulario del restaurante repite el redondeo en TypeScript (`previewPublished`) | Evitar una llamada por tecla; el precio real lo calcula el core. Si el algoritmo cambia, cambiar ambos |
| T4 | `RatesFor` hace 2–3 consultas por llamada y el feed la llama una vez por organización | Volumen bajo; si crece, caché corta en memoria |
| T5 | Los campos JSON de la sesión de ePayco no están verificados contra el sandbox (§10) | Sin cuenta de pruebas todavía |
| T6 | `orders.Order` expone `subtotal_local_cents`/`platform_fee_cents` también al cliente | Separar la vista del cliente y la del negocio |
| T7 | El panel de pago no tiene prueba e2e (necesita backend y una sesión de ePayco falsa en el navegador) | Ampliar los e2e con el devstack en el CI |
| T8 | `pgtest.Reset` restaura las tarifas sembradas copiando sus valores: si cambia la siembra de `00010`, hay que cambiar ambos | Simple y explícito |

## 18. Pagos en efectivo (diseño pendiente)

Dos cosas distintas que suelen llamarse "efectivo":

| Opción | Cómo funciona | Implicaciones |
|---|---|---|
| **A. Contraentrega** (el cliente le paga al domiciliario) | El pedido se confirma sin pago en línea; el domi cobra y entrega | El dinero lo tiene el domiciliario: hay que liquidar con el restaurante y con la plataforma (comisión) después. Riesgo de faltantes, cambio, seguridad del domi. Sin costo de pasarela |
| **B. Efectivo vía ePayco** (Efecty, Baloto, Gana, Puntored, SuRed…) | ePayco genera un código/PIN; el cliente paga en un punto físico; ePayco confirma por webhook | El pedido queda esperando hasta que el cliente pague (horas o días): no sirve para comida caliente. Tiene su propia tarifa y vencimiento |

**Recomendación**: B no encaja con domicilios de comida (el pago tarda). A es lo habitual en
Colombia pero es un **módulo de liquidación** aparte. Diseño sugerido para A cuando se decida:

1. Nuevo medio `cash_on_delivery` (sin pasarela, sin costo de transacción).
2. El pedido pasa de `placed` a `confirmed` sin pago (o con un estado `awaiting_cash`), por decisión
   del restaurante (puede no aceptarlo).
3. Al entregar, el domi marca "cobrado" con el monto.
4. **Libro de liquidación**: cuánto debe el domi al restaurante, cuánto el restaurante a la
   plataforma (comisión); cierre diario.
5. Límite de monto por pedido en efectivo y bloqueo de clientes con entregas fallidas.

Preguntas: ¿se acepta contraentrega? ¿quién asume el riesgo de un faltante? ¿la comisión se cobra
igual?

## 19. Evaluado para la próxima versión

### 19.1 ¿SDK de ePayco?

No. Los SDK (Node, PHP, Python, Ruby, C#) son capas delgadas sobre la API HTTP de ePayco, pensadas
para el **método 3** (formulario propio). Go llama la misma API directamente (`session.go`). El SDK
sirve como **documentación** de endpoints y parámetros.

### 19.2 Las tres formas de cobrar

| Forma | Sale de la página | Trabajo | PCI | Estado |
|---|---|---|---|---|
| 1. Redirección | Sí | Mínimo | Mínimo | Respaldo y base del link de WhatsApp |
| 2. **Onpage** | No (ventana de ePayco encima) | Poco | Mínimo | **Implementado** |
| 3. API directa (formulario propio) | No | Mucho | Alto | Futuro, si el negocio lo pide |

### 19.3 Método 3 en el futuro: ¿módulo o microservicio?

- **Se puede hacer en Go, como otro flujo de `internal/epayco`** dentro del core (Echo), sin
  microservicio: crear cliente, cobro con token, PSE, Daviplata (OTP), efectivo, split.
- **Condición innegociable**: la tarjeta se tokeniza **en el navegador** con la librería JavaScript
  de ePayco; al core solo llega el token. Si el número de tarjeta pasara por el core, toda la
  plataforma entraría en el nivel más alto de PCI DSS (auditorías, escaneos trimestrales).
- Aun con tokenización en el navegador, el formulario es nuestro: más requisitos de seguridad en la
  página (CSP estricta, control de scripts, integridad de recursos).
- Hay que construir cada medio: PSE (bancos, tipo de persona, redirección), Daviplata (OTP),
  efectivo (comprobante), 3-D Secure.
- **Microservicio**: solo se justificaría para **aislar** un componente que maneje datos de tarjeta
  y reducir el alcance de la auditoría PCI. Con tokenización en el navegador no hace falta.

### 19.4 ¿Hyperswitch (Rust) u otro orquestador?

Descartado por ahora: de sus 165 conectores, ninguno es ePayco, Wompi, Mercado Pago, PSE ni Nequi
(su `payu` es PayU Europa); su stack son ~8 servicios en una VM de 8 GiB que ya corre dos entornos.
Reconsiderar si llegan a la vez: operación fuera de Colombia con pasarelas que sí tiene, ruteo entre
3+ pasarelas y volumen que justifique operarlo.

### 19.5 ¿Microservicio de pagos en Go?

No por ahora (mismas razones que `ecommerce.md` §0): una persona, una VM, una base; "cobro
confirmado" y "pedido pagado" son una sola transacción de Postgres. La interfaz `Gateway` marca el
corte si algún día se separa.

### 19.6 Otras pasarelas

| Pasarela | Split en el cobro | Uso posible |
|---|---|---|
| Mercado Pago | 1:1 (vendedor + marketplace), por OAuth | No sirve para 3 partes; posible si el domicilio se liquida aparte |
| Wompi | No: "payouts" = dispersión posterior | Pagar a domiciliarios después |
| Bold | No encontrado | Bajo demanda |
| PayU Latam | No encontrado | Bajo demanda |
