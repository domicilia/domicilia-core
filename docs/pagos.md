# Módulo de pagos — diseño v3 (multipasarela, link de cobro y widget)

> **Estado: propuesta para revisión (2026-10-07).** Nada de esto está implementado todavía salvo lo
> que §1 marca como existente. Las decisiones de §3 **bloquean** partes concretas del diseño y
> están marcadas donde aplican con **⏳ PENDIENTE**.
>
> Este documento reemplaza el "modelo de custodia v2" de `ecommerce.md` §0 y amplía su §3. El resto
> de `ecommerce.md` (catálogo, pedidos, promociones) sigue vigente.

## Índice

0. [Resumen](#0-resumen)
1. [Lo que ya existe](#1-lo-que-ya-existe-no-se-reescribe)
2. [Decisiones tomadas](#2-decisiones-tomadas)
3. [Decisiones pendientes (bloqueantes)](#3-decisiones-pendientes-bloqueantes)
4. [Vocabulario](#4-vocabulario)
5. [Modelo de datos](#5-modelo-de-datos)
6. [Interfaz `Gateway` v2](#6-interfaz-gateway-v2)
7. [Flujos](#7-flujos)
8. [Cálculo de montos](#8-cálculo-de-montos)
9. [API](#9-api)
10. [Permisos](#10-permisos)
11. [Seguridad](#11-seguridad)
12. [Pasarelas: capacidades y verificaciones](#12-pasarelas-capacidades-y-verificaciones)
13. [Frontend](#13-frontend)
14. [Fases de construcción](#14-fases-de-construcción)
15. [Pruebas](#15-pruebas)
16. [Fuera de alcance y descartado](#16-fuera-de-alcance-y-descartado)

---

## 0. Resumen

Un solo módulo de pagos **dentro del core** (`internal/payments`), con varias pasarelas detrás de
una interfaz común, que cobra los pedidos de **dos canales**:

| Canal | Cómo empieza el pago | Cómo termina |
|---|---|---|
| **E-commerce** (web) | Widget / checkout web de la pasarela, abierto desde nuestra página con datos **firmados por el core** | Webhook de la pasarela → el core marca pago y pedido |
| **WhatsApp** | **Link de cobro** (`domicilia.com.co/pagar/{token}`) que se envía en el chat | El mismo webhook |

Primeras pasarelas: **ePayco** (ya integrada) y **Wompi**. Después, según lo pidan los restaurantes:
Bold y Mercado Pago. Nunca se manejan datos de tarjeta: el cliente siempre los escribe en la
interfaz de la pasarela.

## 1. Lo que ya existe (no se reescribe)

| Pieza | Dónde | Qué hace |
|---|---|---|
| Tabla `payments` | `db/migrations/00007_payments.sql` | Intentos de cobro de un pedido (`pending`/`succeeded`/`failed`), en centavos enteros. Un pedido puede tener varios intentos |
| Tabla `payment_events` | misma migración | Idempotencia del webhook: `UNIQUE (source, event_key)` |
| `payments.Service` | `internal/payments/service.go` | `Initiate` (solo pedidos `placed`), `HandleWebhook` (registra el evento, `Settle` solo desde `pending`, avanza el pedido), `ListForOrder` |
| Interfaz `Gateway` v1 | `internal/payments/payments.go` | `Name`, `BuildCheckout`, `ParseWebhook` |
| Adaptador ePayco | `internal/epayco/epayco.go` | Checkout por redirección firmado + validación de la firma SHA-256 de la confirmación |
| Webhook | `POST /webhooks/payments` | Caddy envía `/webhooks/*` directo al core (sin pasar por el BFF: no se puede reserializar el cuerpo firmado) |
| Permiso | `org.payments.read` | Solo admin, no delegable |
| Cifrado de secretos | `internal/platform/secretbox` | Ya cifra los tokens de WhatsApp con `CORE_SECRETS_KEY` |
| Máquina de estados del pedido | `ecommerce.md` §2.1 | `placed → confirmed` (pagado) / `payment_failed`, con reintento sobre el mismo pedido |

**Limitaciones actuales que este diseño resuelve:**

- Una sola pasarela y **una sola cuenta global** (`CORE_EPAYCO_*`): todo el dinero llega a Domicilia.
- `BuildCheckout` no hace llamadas de red: sirve para ePayco, pero no para pasarelas que crean el
  link por API (Wompi, Bold, Mercado Pago).
- No hay link de cobro para WhatsApp, ni expiración, ni conciliación, ni reembolsos.
- `payments_gateway_check` solo admite `'epayco'`.

## 2. Decisiones tomadas

1. **Módulo dentro del core, no microservicio ni orquestador externo.** Misma razón que
   `ecommerce.md` §0: una persona, una VM, una base. Hyperswitch se evaluó y se descartó para esta
   fase: no tiene conectores de ePayco, Wompi, Mercado Pago, PSE ni Nequi, y su stack son ~8
   servicios. `internal/payments` queda listo para extraerse si algún día hace falta: la interfaz
   `Gateway` ya marca el corte.
2. **Solo checkouts alojados por la pasarela** (widget, checkout web, link). **Nunca API directa**
   ni tokenización de tarjetas en nuestro servidor: el número de tarjeta no pasa nunca por
   Domicilia (alcance PCI mínimo). Los SDK de servidor de las pasarelas (p. ej. `epayco-sdk-node`)
   **no se usan**; sirven solo como referencia de sus endpoints.
3. **Dos formas de empezar, un solo cierre.** Web = widget con datos firmados; WhatsApp = link de
   cobro. Webhook, estados, conciliación y reembolsos son comunes.
4. **El dinero siempre en centavos enteros** (`int32`/`integer`, como hoy). Nunca `float`.
5. **El precio nunca viene del cliente.** El monto sale del pedido ya calculado en el servidor
   (`orders.total_cents`, snapshot de precios en `place`).
6. **Cada pago guarda una copia de sus tarifas** (comisión de plataforma, tarifa estimada de la
   pasarela, impuestos). Cambiar las tarifas no altera pagos ya creados.
7. **Webhooks bajo `/webhooks/*`**, que es lo que Caddy enruta al core.
8. **Credenciales de pasarela cifradas** con `secretbox` (`CORE_SECRETS_KEY`), de solo escritura:
   la API nunca las devuelve.
9. **No se usa la palabra "plan"** para el modelo de cobro: `internal/plans` ya son los planes
   comerciales del SaaS (Starter, Pro…). Aquí se dice **modelo de recaudo**.

## 3. Decisiones pendientes (bloqueantes)

| # | Pregunta | Quién la responde | Qué bloquea |
|---|---|---|---|
| D1 | **¿Quién recauda?** A) cada restaurante con su cuenta; B) Domicilia con una cuenta y liquida después; C) **split**: la pasarela reparte en el cobro entre restaurante y Domicilia | Contador / abogado + tú | §5 `collection_model`, §8, fase 4 |
| D2 | La comisión de Domicilia (10 %), ¿**se le descuenta al restaurante** o **se le suma al cliente**? | Tú + contador | §8 |
| D3 | IVA de la comisión de Domicilia (servicio gravado) e impuesto al consumo de los restaurantes: ¿cómo se factura y quién emite cada factura? | Contador | §8, reportes |
| D4 | ¿Se puede **trasladar la comisión de la pasarela al cliente**? (reglas de franquicias y de protección al consumidor; mostrarlo antes de pagar) | Contador / abogado | §8 |
| D5 | El valor del domicilio, ¿cómo le llega al domiciliario? (¿otro receptor del split? ¿liquidación aparte?) | Tú | §8, módulo de domis |
| D6 | **ePayco split**: requisitos para que Domicilia sea "aplicación" y cada restaurante "comercio" | Comercial de ePayco | fase 4 |
| D7 | **Wompi**: ¿tiene reparto automático en la transacción, o solo "pagos a terceros" (dispersión posterior)? | Comercial de Wompi | D1 si se usa Wompi con modelo C |
| D8 | **ePayco checkout web actual**: ¿se abre solo con la llave pública o exige crear una sesión desde el servidor (`sessionId`)? | Documentación + sandbox | adaptador ePayco, fase 1 |
| D9 | Autenticación del **agente de IA** ante el core (hoy no existe cuenta de servicio) | Diseño aparte | fase 6 |

**Recomendación técnica** (no sustituye a D1–D4): **C (split) cuando la pasarela lo permita, A
mientras tanto.** B es la única que mete dinero de terceros en la cuenta de Domicilia; queda
soportada solo para compatibilidad con lo que existe hoy y para pruebas.

## 4. Vocabulario

| Término | Significado aquí |
|---|---|
| **Pasarela** (`gateway`) | El proveedor: `epayco`, `wompi`, `bold`, `mercadopago` |
| **Conexión** (`payment_connection`) | Una cuenta concreta en una pasarela, con sus credenciales cifradas. De un restaurante o de la plataforma |
| **Modelo de recaudo** (`collection_model`) | Quién recibe el dinero: `platform` (B), `organization` (A), `split` (C) |
| **Canal** (`channel`) | Por dónde se originó el cobro: `web`, `whatsapp` |
| **Intento de pago** (`payment`) | Un cobro concreto de un pedido. Un pedido puede tener varios |
| **Link de cobro** (`payment_link`) | URL nuestra (`/pagar/{token}`) que lleva a la pasarela; caduca |

## 5. Modelo de datos

Migración nueva (`00010_payments_multigateway.sql`, número a confirmar al implementar).

### 5.1 `payment_connections`

```sql
CREATE TABLE payment_connections (
    id                 uuid        NOT NULL DEFAULT gen_random_uuid(),
    -- NULL = conexión de la PLATAFORMA (modelo B, o la "aplicación" del split).
    organization_id    uuid        REFERENCES organizations (id) ON DELETE RESTRICT,
    gateway            text        NOT NULL,          -- 'epayco' | 'wompi' | ...
    mode               text        NOT NULL,          -- 'test' | 'live'
    -- Datos NO secretos que el frontend sí necesita (llave pública, id de comercio).
    public_config      jsonb       NOT NULL DEFAULT '{}',
    -- Llave privada, secreto de integridad, secreto de eventos... cifrados con secretbox.
    secrets_ciphertext bytea       NOT NULL,
    status             text        NOT NULL DEFAULT 'active',  -- 'active' | 'disabled'
    created_by         uuid        NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_connections_pkey PRIMARY KEY (id),
    CONSTRAINT payment_connections_gateway_check CHECK (gateway IN ('epayco', 'wompi')),
    CONSTRAINT payment_connections_mode_check CHECK (mode IN ('test', 'live')),
    CONSTRAINT payment_connections_status_check CHECK (status IN ('active', 'disabled'))
);
-- Una sola conexión ACTIVA por organización y pasarela (y una por pasarela para la plataforma).
CREATE UNIQUE INDEX ux_payment_connections_active
    ON payment_connections (coalesce(organization_id, '00000000-0000-0000-0000-000000000000'), gateway)
    WHERE status = 'active';
```

### 5.2 `organization_payment_settings`

Cómo cobra cada restaurante. Lo edita el **superadmin** (tarifas, modelo) y en parte el admin del
restaurante (qué conexión usa).

```sql
CREATE TABLE organization_payment_settings (
    organization_id     uuid    NOT NULL REFERENCES organizations (id) ON DELETE RESTRICT,
    collection_model    text    NOT NULL DEFAULT 'organization', -- ⏳ D1
    connection_id       uuid    REFERENCES payment_connections (id),
    -- Comisión de Domicilia en puntos básicos: 1000 = 10,00 %. ⏳ D2 decide a quién se aplica.
    platform_fee_bps    integer NOT NULL DEFAULT 1000,
    platform_fee_payer  text    NOT NULL DEFAULT 'organization', -- 'organization' | 'customer' ⏳ D2
    updated_by          uuid    NOT NULL,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT organization_payment_settings_pkey PRIMARY KEY (organization_id),
    CONSTRAINT ops_model_check CHECK (collection_model IN ('platform', 'organization', 'split')),
    CONSTRAINT ops_fee_check CHECK (platform_fee_bps BETWEEN 0 AND 10000)
);
```

Las **tarifas de cada pasarela** (porcentaje + fijo) no van en tabla: se cobran según el contrato
de cada conexión y la cifra real llega en la conciliación. Lo que sí se guarda es una **estimación
por defecto** para mostrar el desglose, en una tabla de la plataforma (`gateway_fee_defaults`:
`gateway`, `percent_bps`, `fixed_cents`, `vat_bps`) editable por el superadmin y auditada.

### 5.3 Cambios en `payments`

```sql
ALTER TABLE payments
    ADD COLUMN connection_id      uuid REFERENCES payment_connections (id),
    ADD COLUMN channel            text NOT NULL DEFAULT 'web',      -- 'web' | 'whatsapp'
    ADD COLUMN flow               text NOT NULL DEFAULT 'checkout', -- 'checkout' | 'link'
    ADD COLUMN external_ref       text,          -- id de la transacción o del link en la pasarela
    ADD COLUMN link_token         text UNIQUE,   -- solo flow = 'link': el {token} de /pagar/{token}
    ADD COLUMN expires_at         timestamptz,
    ADD COLUMN collection_model   text,          -- copia de la configuración al crear el pago
    -- Copia de tarifas al crear el pago (decisión 6). Estimadas; la conciliación guarda las reales.
    ADD COLUMN platform_fee_cents integer NOT NULL DEFAULT 0,
    ADD COLUMN gateway_fee_estimated_cents integer NOT NULL DEFAULT 0,
    ADD COLUMN breakdown          jsonb   NOT NULL DEFAULT '{}';  -- desglose completo (§8)
-- Estados nuevos: 'expired' (link vencido sin pagar) y, en la fase de reembolsos, 'refunded'.
-- gateway_check pasa a ('epayco', 'wompi').
```

Pagos existentes: `connection_id` NULL = la conexión global de `CORE_EPAYCO_*` (compatibilidad,
§14 fase 1).

### 5.4 Más adelante

- `payment_refunds` (fase 5): reembolsos totales o parciales, con su propio estado.
- `payment_settlements` (fase 5): lo que la pasarela reporta como liquidado (tarifa real, neto).

## 6. Interfaz `Gateway` v2

```go
// Gateway es UNA conexión ya resuelta (pasarela + credenciales descifradas). Se construye por
// petición con Registry.For(connection); nunca se guarda con secretos en memoria global.
type Gateway interface {
    Name() string
    Capabilities() Capabilities

    // Web: datos firmados para que el frontend abra el widget/checkout. Sin llamada de red en
    // Wompi (firma de integridad). En ePayco puede requerir crear una sesión (⏳ D8).
    PrepareCheckout(ctx context.Context, req ChargeRequest) (CheckoutParams, error)

    // WhatsApp: crea el link en la pasarela (Wompi, Bold, MP llaman a su API). ePayco devuelve
    // la URL firmada que ya genera hoy.
    CreatePaymentLink(ctx context.Context, req ChargeRequest) (GatewayLink, error)

    // Valida la autenticidad del webhook con los secretos de ESTA conexión.
    ParseWebhook(r *http.Request) (WebhookEvent, error)

    // Conciliación: consulta el estado real de un cobro cuando el webhook no llegó.
    QueryStatus(ctx context.Context, p Payment) (StatusResult, error)
}

type Capabilities struct {
    Widget, PaymentLinkAPI, QueryStatus, Refunds, Split bool
}

type CheckoutParams struct {
    Gateway   string            // el frontend elige el script según esto
    PublicKey string
    Fields    map[string]string // referencia, monto, moneda, firma, URLs, sessionId...
}

type GatewayLink struct {
    URL         string    // URL de la pasarela (o la nuestra para PayU-like, ver §16)
    ExternalRef string    // id del link en la pasarela: con él se cruza el webhook
    ExpiresAt   time.Time
}
```

`Registry` mapea `gateway` → constructor (`epayco.New`, `wompi.New`) y descifra los secretos con
`secretbox` justo al construir. Un paquete por pasarela (`internal/epayco`, `internal/wompi`), igual
que hoy.

**Cruce del webhook con nuestro pago:**

- Checkout web: la referencia que mandamos es **nuestro `payment.id`** y vuelve en el webhook.
- Link: algunas pasarelas generan su propia referencia por transacción. Se cruza por `external_ref`
  (id del link guardado al crearlo). *Verificar en cada pasarela (§12).*

## 7. Flujos

### 7.1 E-commerce (widget)

```
Cliente            Frontend                 Core                         Pasarela
  │  Pagar  ─────────▶│  POST /pay {web} ──────▶│ pedido placed? conexión?   │
  │                   │                         │ crea payment (pending)     │
  │                   │                         │ copia tarifas              │
  │                   │◀── CheckoutParams ──────│ firma (o sessionId ⏳D8)   │
  │◀── abre widget ───│                                                      │
  │  paga en la interfaz de la pasarela  ───────────────────────────────────▶│
  │◀── vuelve a /cliente/pedidos/{id} (solo muestra "procesando")            │
  │                                             │◀── webhook /webhooks/payments/{gw}/{conn}
  │                                             │ valida firma, idempotencia │
  │                                             │ Settle + pedido confirmed  │
```

La página de vuelta **no** decide si se pagó: solo consulta el estado. La verdad llega por
webhook o por conciliación.

### 7.2 WhatsApp (link de cobro)

```
Staff/agente         Core                                  Cliente (WhatsApp)     Pasarela
  │ crear link ──────▶│ pedido placed, conexión              │                       │
  │                   │ payment (flow=link, expira)          │                       │
  │                   │ CreatePaymentLink ───────────────────────────────────────────▶│
  │                   │ guarda external_ref + token          │                       │
  │◀─ /pagar/{token} ─│                                      │                       │
  │  envía el link por el inbox ────────────────────────────▶│                       │
  │                                                          │ abre /pagar/{token}   │
  │                   │◀─ GET /v1/public/pay/{token} ────────│ (frontend redirige)   │
  │                   │ válido y no vencido → URL pasarela ─▶│ ─────────────────────▶│
  │                   │◀─────────────────────── webhook ─────────────────────────────│
  │                   │ Settle + pedido confirmed + mensaje "pago recibido" por WhatsApp
```

- **Fase 3:** lo crea el **staff** desde el inbox (botón "Enviar link de pago").
- **Fase 6:** lo pide el **agente de IA** por la API del core (⏳ D9). El agente nunca habla con la
  pasarela.
- **Pedido de WhatsApp:** hoy un pedido lo crea un cliente con sesión. Para WhatsApp hace falta
  crear pedidos a nombre de un contacto identificado por teléfono. Es un cambio en
  `internal/orders`, prerequisito de la fase 3.
- Expiración por defecto: 60 min (configurable). Vencido → `expired`; el pedido sigue en `placed` y
  se puede generar otro link.

### 7.3 Webhook

- Ruta nueva: `POST /webhooks/payments/{gateway}/{connection_id}`. El `connection_id` dice con qué
  secretos validar la firma **antes** de confiar en nada del cuerpo.
- `POST /webhooks/payments` se mantiene durante la transición para la conexión global de ePayco.
- Sin cambios en la lógica de `HandleWebhook`: idempotencia por `(source, event_key)`, `Settle`
  solo desde `pending`, avanzar el pedido. `source` pasa a ser `gateway:connection_id`.

### 7.4 Conciliación

Tarea periódica en el core (mismo patrón de lease que el dispatcher de WhatsApp): cada 5 min toma
pagos `pending` con más de 15 min y llama a `QueryStatus`. Resultado final → el mismo camino que
el webhook (`HandleWebhook` con un evento sintético `reconcile:{payment_id}:{estado}`). Links
vencidos sin pago → `expired`.

### 7.5 Reembolsos (fase 5)

Solo admin, auditado, contra la pasarela que cobró. PSE y algunos medios no se reembolsan por API:
la capacidad `Refunds` lo indica y la UI lo explica.

## 8. Cálculo de montos

⏳ Las fórmulas dependen de D2–D5. Reglas fijas:

- Todo en centavos enteros. Cada componente se redondea **una vez**, al centavo, mitad hacia arriba.
- El cálculo vive en una función pura (`payments.Breakdown(...)`), con pruebas de tabla.
- El resultado se guarda en `payments.breakdown` al crear el pago.
- La tarifa de la pasarela es una **estimación**: la real llega en la conciliación (fase 5).

Ejemplo ilustrativo para un pedido de **$50.000** de comida + **$5.000** de domicilio, suponiendo
comisión de Domicilia 10 % **descontada al restaurante** (D2) y tarifa estimada de pasarela
2,65 % + $700 + IVA 19 % pagada por el restaurante:

| Concepto | Cálculo | Valor |
|---|---|---|
| Comida (subtotal) | | $50.000 |
| Domicilio (no comisiona, ⏳ D5) | | $5.000 |
| **Paga el cliente** | | **$55.000** |
| Comisión Domicilia | 10 % × 50.000 | $5.000 |
| IVA de la comisión (⏳ D3) | 19 % × 5.000 | $950 |
| Tarifa pasarela | 2,65 % × 55.000 + 700 | $2.158 (redondeado) |
| IVA tarifa pasarela | 19 % × 2.158 | $410 |
| **Neto restaurante** | 50.000 − 5.000 − 950 − 2.158 − 410 | **$41.482** |
| **Domiciliario** | | $5.000 |

En este ejemplo el restaurante asume también la tarifa de la pasarela sobre los $5.000 del
domicilio (la pasarela cobra sobre el total). Quién asume esa parte lo decide D5.

Si D2 decide que el cliente paga la comisión, o D4 permite trasladar la tarifa de la pasarela, la
misma función cambia de parámetros, no de estructura.

## 9. API

| Método y ruta | Quién | Qué hace |
|---|---|---|
| `POST /v1/organizations/{org}/orders/{id}/pay` | Cliente dueño | **Existe.** Pasa a devolver `CheckoutParams` (widget). Cuerpo opcional `{ "channel": "web" }` |
| `POST /v1/organizations/{org}/orders/{id}/payment-links` | Staff con `org.payment_links.create` | Crea un link de cobro. Devuelve `{ url, expires_at }` |
| `GET /v1/public/pay/{token}` | Público | Valida el token (existe, `pending`, no vencido) y devuelve la URL de la pasarela. La página `/pagar/{token}` del frontend redirige |
| `GET /v1/organizations/{org}/orders/{id}/payments` | `org.payments.read` | **Existe** |
| `GET/POST/DELETE /v1/organizations/{org}/payment-connections` | `org.payments.manage` | Conectar o desactivar una pasarela del restaurante. Los secretos son de solo escritura |
| `GET/PUT /v1/platform/organizations/{org}/payment-settings` | Superadmin | Modelo de recaudo y comisión del restaurante |
| `GET/PUT /v1/platform/payment-fee-defaults` | Superadmin | Tarifas estimadas por pasarela (para el desglose) |
| `POST /webhooks/payments/{gateway}/{connection_id}` | Pasarela | Webhook (§7.3) |

## 10. Permisos

| Código | Alcance | Delegable | Quién lo tiene por defecto |
|---|---|---|---|
| `org.payments.read` | organización | no | admin (**existe**) |
| `org.payments.manage` | organización | no | admin: conectar la pasarela del restaurante |
| `org.payment_links.create` | organización | sí | admin y call center: enviar links por WhatsApp |
| `platform.payments.manage` | plataforma | no | superadmin: modelo de recaudo, comisiones, tarifas |

Todo cambio de conexión, configuración o tarifa queda en `internal/audit`.

## 11. Seguridad

- **Sin datos de tarjeta** en ningún sistema de Domicilia (decisión 2).
- **Secretos cifrados** (`secretbox`), de solo escritura, nunca en logs ni en respuestas.
  Separación `test`/`live` por conexión: un webhook de pruebas no puede confirmar un pago real.
- **Firma de cada webhook** validada con los secretos de su conexión antes de leer el cuerpo.
- **El monto lo pone el servidor**: la firma de integridad (Wompi) y la sesión (ePayco) se calculan
  con el total del pedido en el core.
- **Token del link**: 128 bits aleatorios (no el `payment.id`), un solo pago, con expiración.
  Rate limit en `GET /v1/public/pay/{token}`.
- **Idempotencia** del webhook y de la creación de links (clave = `payment.id`).
- **Montos del webhook comparados** con los del pago: si no coinciden, el pago no se acepta y se
  alerta.

## 12. Pasarelas: capacidades y verificaciones

| Capacidad | ePayco | Wompi |
|---|---|---|
| Widget / checkout web | `checkout.js` (⏳ D8: ¿sesión desde el servidor?) | Widget y checkout web: mismo script, **firma de integridad** SHA-256(referencia + monto en centavos + moneda + secreto) |
| Link de cobro por API | URL firmada (la de hoy) | `POST /v1/payment_links` (uso único, expiración, monto fijo) |
| Webhook firmado | SHA-256 (ya implementado) | Evento `transaction.updated` con checksum (`X-Event-Checksum` / `signature.properties`) |
| Consulta de estado | API de transacciones (ver SDK: `charge.get`) | API de transacciones |
| Split en la transacción | **Sí** (`splitpayment`, `split_receivers`…) ⏳ D6 | ⏳ D7 (solo confirmado "pagos a terceros") |
| Reembolsos por API | A verificar | A verificar |
| Medios | Tarjeta, PSE, Nequi, Daviplata, efectivo | Tarjeta, PSE, Nequi, Bancolombia |

**Antes de mover dinero real, con una cuenta sandbox de cada una:** URL exacta, nombres de
parámetros, orden de las firmas y forma del webhook (la advertencia de `ecommerce.md` §3 sigue
vigente para ePayco).

## 13. Frontend

| Pantalla | Ruta | Fase |
|---|---|---|
| Pagar con widget | `/cliente/pedidos/{id}` (botón Pagar existente) | 1–2 |
| Resultado ("procesando…" → estado real) | misma página | 1 |
| Link de cobro | `/pagar/{token}` (pública, con la marca de Domicilia; redirige o muestra "vencido") | 3 |
| Conectar pasarela del restaurante | `/{tenant}/admin/payments` → pestaña Configuración | 1 |
| Enviar link desde el inbox | `/{tenant}/{rol}/messages` → acción en la conversación | 3 |
| Modelo de recaudo, comisión y tarifas | `/platform/billing` (con simulación del desglose, §8) | 1 |

## 14. Fases de construcción

| Fase | Contenido | Depende de |
|---|---|---|
| **0** | Respuestas a D1–D4 (contador) y D6–D8 (pasarelas). Cuentas sandbox de ePayco y Wompi | — |
| **1** | Base multipasarela: tablas §5.1–5.3, `Registry`, `Gateway` v2, ePayco adaptado y **probado contra su sandbox**, webhook por conexión, conciliación, pantallas de conexión y de configuración. La conexión global `CORE_EPAYCO_*` sigue funcionando | 0 (D8) |
| **2** | Adaptador **Wompi**: widget + link + webhook + consulta | 1 |
| **3** | **Link de cobro**: `/pagar/{token}`, expiración, botón en el inbox de WhatsApp, pedidos a nombre de un contacto | 1, 2 |
| **4** | **Split** (si D1 = C) y comisión automática | D1, D6/D7 |
| **5** | Reembolsos, liquidaciones reales, reportes para el restaurante y la plataforma | 1 |
| **6** | El **agente de IA** crea pedidos y links por la API del core | 3, D9 |
| Después | Bold, Mercado Pago (bajo demanda), widget embebido con mejor UX | — |

Cada fase llega con sus pruebas (§15) y su migración; nada pasa a producción sin la prueba real en
staging de esa pasarela.

## 15. Pruebas

- **Función de desglose**: pruebas de tabla con casos de redondeo.
- **Servicio**: testcontainers (Postgres real), igual que el resto del core, con una pasarela
  falsa (`fakegateway`) que implementa `Gateway` v2.
- **Adaptadores**: webhooks de ejemplo firmados (válidos, firma alterada, monto alterado, repetidos)
  y vectores de firma conocidos tomados de la documentación de cada pasarela.
- **Contrato con la pasarela**: pruebas opcionales contra el sandbox real (variable de entorno),
  fuera del CI normal.
- **E2E**: pagar con el sandbox en staging antes de cada pasarela nueva.

## 16. Fuera de alcance y descartado

| Opción | Por qué no |
|---|---|
| Hyperswitch u otro orquestador externo | Sin conectores colombianos clave; ~8 servicios en una VM de 8 GiB |
| Microservicio de pagos aparte | Mismo motivo que `ecommerce.md` §0; la interfaz deja la puerta abierta |
| API directa / tokenización / SDK de servidor | Alcance PCI completo y construir cada medio de pago a mano |
| "Pasarela propia" o código de adquirencia propio (planes 3 y 4 del prompt evaluado) | Contrato con banco/red, requisitos regulatorios; no para esta etapa |
| PayU (WebCheckout es un formulario firmado, no un link) | Posible más adelante: `/pagar/{token}` enviaría el formulario automáticamente |
| GORM | El core usa sqlc + goose |
