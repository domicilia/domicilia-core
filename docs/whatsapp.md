# Módulo WhatsApp (multi-inquilino): modelo de datos, plantillas, envíos masivos y monetización

Estado: **propuesta para revisión (2026-09-26), sin implementar**. Se apoya en:

- el modelo de datos de Chatwoot (`chatwoot/db/schema.rb`, `app/services/whatsapp/*`, `campaign.rb`);
- la base de la V1 (Supabase, un solo negocio) y la V2 multi-inquilino ya diseñada en
  `docs/organizaciones.md` (planes, funciones y `plans.Service.Require`);
- los módulos por plan de `docs/whatsapp-modulos/` (Starter, Pro, Outreach, Enterprise).

Toda tabla lleva `organization_id`. Todo endpoint premium pasa por `plans.Require`.
Los datos de Meta (límites, precios, estados) **deben verificarse contra su documentación
vigente antes de fijar precios**: cambian por trimestre.

## 0. Estado de la implementación

**Hecho (bandeja base, 2026-09-26)** — migración `00004_whatsapp_inbox`, paquetes `inboxes`, `contacts`, `conversations`,
`whatsapp`, `meta`, `tenant`, `platform/secretbox`, y el contrato `openapi/openapi.yaml`:

- Conexión de un número por **credenciales pegadas** (validadas contra Meta), token cifrado, rotación y desconexión.
- Webhook con firma por número, deduplicación y estados que solo avanzan.
- Contactos por organización, conversaciones (número por organización, ventana de 24 h guardada, no leídos, responsable,
  bot/persona) y mensajes con cursor.
- Envío de texto en cola con entrega en orden, reintentos y sin duplicar mensajes.
- Integridad entre organizaciones **en la base** (claves foráneas compuestas) además del código.

**Decisiones**: contrato limpio (uuid, cursor, estados claros) y no la forma de Chatwoot; el frontend lo adapta en su capa
de acceso. Los mensajes de plantilla y las campañas llegan en las siguientes partes (sección 5).

**Aún NO existe**: plantillas, campañas, consentimiento, segmentos, envío/descarga de archivos, libro de uso y billetera,
Embedded Signup, tiempo real y etiquetas/equipos/macros/CSAT.

## 1. Qué hace bien Chatwoot y qué no copiamos

| Chatwoot | Decisión |
| :--- | :--- |
| `inboxes` + `channel_whatsapp` (polimórfico por `channel_type`) | Conservamos `inboxes` como concepto (saludo, horario, asignación, CSAT), con detalle 1:1 `whatsapp_channels`. **Sin polimorfismo**: solo WhatsApp. |
| Ventana de 24 h calculada al vuelo (`MessageWindowService`) | Se **guarda** `conversations.last_customer_message_at`; la ventana se lee sin consultar mensajes. |
| Plantillas = un `jsonb` que se sobrescribe entero al sincronizar (`channel_whatsapp.message_templates`) | Tabla `message_templates` propia: estado, categoría, calidad, motivo de rechazo, versiones y uso. **Se crean y editan desde Domicilia**, no solo se leen. |
| Estado de plantilla por sondeo | Por webhook (`message_template_status_update`) más un sondeo de respaldo. |
| Campaña = un `jsonb` de audiencia y un `each` síncrono; se marca *completed* **antes** de enviar; un fallo se registra en el log y se pierde | `campaign_recipients`: una fila por destinatario con su estado. Reanudable, reintentable, auditable y conciliable con la factura. |
| Audiencia solo por etiquetas | Segmentos (estáticos y dinámicos) con **instantánea** al lanzar. |
| Sin consentimiento, sin baja, sin tope de frecuencia | `contact_consents`, baja automática por palabra clave, tope de frecuencia de marketing. |
| Sin límite de ritmo ni de plan de Meta | Limitador por número (rendimiento) y por nivel diario de mensajería. |
| Sin costos | Libro de uso (`usage_events`), billetera, paquetes y reglas de margen: es lo que se monetiza. |
| El webhook procesa **solo el primer** mensaje o estado de cada petición (`.first`); deduplica con un bloqueo en Redis | Se procesan todos los eventos; la deduplicación es una restricción única en Postgres (no hay Redis en el stack). |
| Token de Meta en `provider_config` (jsonb) | Cifrado en reposo (AES-GCM con llave del Key Vault), nunca en JSON de configuración. |
| Estados de mensaje `sent, delivered, read, failed` | Se añade `queued` (aceptado por nosotros, aún no enviado) y una marca de tiempo por estado. |

## 2. Modelo de datos

### 2.1 Canal

```
whatsapp_accounts          una cuenta de WhatsApp Business (WABA) por organización
  organization_id, waba_id (único), business_id, name, status,
  access_token_enc (bytea), token_expires_at, connected_by, created_at

inboxes                    la "bandeja": lo que ve el equipo
  organization_id, name, greeting_enabled/message, timezone, business_hours (jsonb),
  out_of_office_message, csat_enabled, auto_assignment, allow_after_resolved, archived_at

whatsapp_channels          1:1 con inboxes
  inbox_id (PK), whatsapp_account_id, phone_number_id (ÚNICO global: es la llave de
  enrutamiento del webhook), display_phone_e164, verified_name,
  quality_rating (green|yellow|red), messaging_tier (250|1k|10k|100k|unlimited),
  throughput_mps, status, webhook_subscribed_at
inbox_members / teams / team_members          quién atiende qué (como Chatwoot)
```

`phone_number_id` único en toda la plataforma es la clave: el webhook de Meta **no dice** de qué
organización es un mensaje; se resuelve `phone_number_id → whatsapp_channels → organization_id`.

### 2.2 Contactos y consentimiento

```
contacts                   personas que escriben o reciben, POR organización
  organization_id, phone_e164 (ÚNICO por organización), name, email,
  customer_user_id (nullable: enlace al cliente B2C de la plataforma),
  custom_attributes (jsonb), blocked, last_activity_at, source (inbound|import|order|api)
contact_consents           auditoría del consentimiento (Ley 1581 / política de WhatsApp)
  contact_id, purpose (service|utility|marketing), status (opted_in|opted_out),
  source (inbound_message|checkout|import|keyword_stop|manual), evidence, recorded_by, created_at
labels / contact_labels / conversation_labels   etiquetas (tablas de unión, no polimórficas)
contact_notes
segments                   audiencias guardadas
  organization_id, name, kind (static|dynamic), filter (jsonb), member_count_cached
segment_members            solo para los estáticos
```

`contacts` es por organización y `customers` (plataforma) es la identidad del cliente final. La V1
tenía un solo `customers` global por `phone_e164`; aquí el mismo teléfono puede ser contacto de varios
negocios sin que uno vea los datos del otro. El consentimiento es un **historial**, no un booleano: hay
que poder demostrar cuándo y cómo se dio.

### 2.3 Conversaciones y mensajes

```
conversations
  organization_id, inbox_id, contact_id, display_id (secuencia por organización),
  status (open|pending|resolved|snoozed), assignee_id, team_id, priority,
  handled_by (bot|human), last_customer_message_at,   -- ventana de 24 h
  first_response_at, waiting_since, last_activity_at, snoozed_until, campaign_id
messages
  organization_id, conversation_id, direction (inbound|outbound), kind
  (text|media|template|interactive|system|note), body, template_id, wamid,
  status (queued|sent|delivered|read|failed), error_code, error_detail,
  sent_at, delivered_at, read_at, failed_at, sender_user_id, campaign_recipient_id,
  reply_to_message_id, created_at
message_attachments        tipo, mime, tamaño, url interna, wa_media_id, media_expires_at
webhook_events            carga cruda, `event_key` ÚNICO (idempotencia), estado de proceso, retención 30 días
```

- `wamid` es único por número: garantiza que el mismo mensaje entrante procesado dos veces no se
  duplica. Se probará con dos entregas simultáneas.
- Las URL de medios de Meta caducan (la V1 ya tenía `whatsapp_media_cache` por eso): se descargan y
  guardan en almacenamiento propio.
- Mensajes no se particionan al principio; se indexan `(conversation_id, created_at)` y
  `(organization_id, created_at)`. La partición mensual entra cuando el volumen la justifique.

### 2.4 Plantillas

```
message_templates
  organization_id, whatsapp_account_id, meta_template_id, name, language,
  category (marketing|utility|authentication), parameter_format (positional|named),
  components (jsonb, estructura canónica de Meta), status
  (draft|pending|approved|rejected|paused|disabled), quality, rejection_reason,
  submitted_at, approved_at, last_synced_at, created_by
  UNIQUE (whatsapp_account_id, name, language)
template_gallery           plantillas base de la PLATAFORMA (pedido confirmado, en camino, entregado,
                           promoción...) que cada organización instala con un clic
```

Constructor de plantillas en Domicilia: validaciones **antes** de enviar a Meta (nombre
`[a-z0-9_]`, variables consecutivas, ejemplos obligatorios, límites de botones y de longitud), envío por
la API, y estado por webhook. La galería es palanca de incorporación y de venta: un restaurante nuevo
arranca con plantillas ya aprobadas.

### 2.5 Campañas y envíos masivos

```
campaigns
  organization_id, inbox_id, template_id, name,
  variable_mapping (jsonb: variable → campo del contacto | atributo | texto fijo),
  segment_id, audience_snapshot_at,
  status (draft|scheduled|queued|running|paused|completed|cancelled|failed),
  scheduled_at, send_window (jsonb: horario y zona horaria), throttle_mps,
  budget_cap, estimated_cost, counters (total, sent, delivered, read, failed, replied, opted_out)
campaign_recipients        LA COLA Y EL LIBRO DE CADA ENVÍO
  campaign_id, contact_id, phone_e164 (instantánea), variables (jsonb resuelto),
  status (pending|queued|sent|delivered|read|failed|skipped),
  skip_reason (no_consent|opted_out|invalid_number|duplicate|blocked|frequency_cap|no_balance),
  message_id, wamid, error_code, timestamps
  UNIQUE (campaign_id, contact_id)
```

Flujo: **borrador → validar → estimar costo → programar/lanzar**. Al lanzar se congela la audiencia
(instantánea), se calculan omitidos (sin consentimiento, dados de baja, número inválido, tope de
frecuencia) y se reserva el presupuesto. Los trabajadores toman destinatarios con
`FOR UPDATE SKIP LOCKED`, respetan el ritmo por número y el tope diario del nivel de mensajería de Meta,
y pueden morir y reanudarse sin duplicar ni perder envíos. Los estados llegan por webhook y actualizan
el destinatario y los contadores. Una respuesta del contacto dentro de N horas se atribuye a la campaña
(`replied`).

### 2.6 Uso, billetera y facturación

```
meta_pricing_rates         tarifas de Meta por categoría y país, con vigencia (las actualiza la plataforma)
usage_events               LIBRO APPEND-ONLY
  organization_id, kind (wa_marketing|wa_utility|wa_authentication|wa_service|ai_tokens),
  quantity, meta_cost, price_charged, currency, ref (message|campaign),
  occurred_at, idempotency_key (ÚNICA)
wallets                    saldo por organización y moneda
wallet_transactions        top_up (ePayco) | usage | refund | adjustment | reservation | release
```

En el modo B (principal) `meta_cost` es el costo real que Domicilia paga y `price_charged` lo que se cobra al negocio (costo × tasa de cambio + margen, o el consumo de un paquete). En el modo A `meta_cost` es solo una **estimación** para presupuesto y reportes y `price_charged` lleva únicamente la tarifa de la plataforma (sección 3).

`llm_token_usage` de la V1 ya medía tokens por ejecución: se generaliza a `usage_events`
(`kind = ai_tokens`) para cobrar también al agente. El libro es de solo inserción con clave de
idempotencia: un webhook de estado repetido no cobra dos veces.

## 3. Conexión con Meta y quién le paga a Meta (decide cómo se cobra)

Decisión tomada: **Cloud API de Meta directo**, sin intermediario, en **Go** (la API es HTTPS + JSON +
webhooks, el trabajo es de entrada/salida y no de cómputo: no hay razón para otro lenguaje ni otro
servicio, y el core ya es Go).

**Dos formas de conectar un negocio, misma base de código:**

| Fase | Cómo | Quién lo hace |
| :--- | :--- | :--- |
| 1 (primeros clientes, ya) | **Credenciales pegadas**: ID del número, ID de la cuenta WABA y token permanente del negocio, con una prueba de conexión antes de guardar | El administrador del negocio desde un asistente en Domicilia, o el superadmin en su nombre |
| 2 (cuando Domicilia sea Tech Provider) | **Embedded Signup**: el negocio inicia sesión con Facebook y autoriza; Domicilia recibe la cuenta y el número sin que nadie copie tokens | El propio negocio, con un botón |

Ambas fases usan los mismos endpoints y tablas (`whatsapp_accounts`, `whatsapp_channels`); solo cambia
cómo llega la credencial. Empezar por la fase 1 permite vender **antes** de terminar la revisión de Meta.

**Qué exige Meta para ser proveedor de tecnología** (verificar en su documentación vigente; no es
una lista oficial): verificación de la empresa (documentos legales, NIT y sitio web propio), una app
de Meta de tipo negocio con el producto WhatsApp, política de privacidad y términos publicados, y
la **revisión de la app** para los permisos de gestión y mensajería con acceso avanzado (videos de
demostración del flujo). No pide un respaldo económico millonario ni pago de inscripción; sí toma
**semanas** y una empresa constituida. Ser *Solution Partner* (línea de crédito propia con Meta) es
otro nivel, con requisitos de volumen: no es necesario para arrancar.

### Quién le paga a Meta: modo principal = Domicilia paga y revende

Decisión (2026-09-26): **el modo principal es B: la organización le paga a Domicilia y Domicilia le
paga a Meta por todos los servicios**, con un porcentaje de ganancia o un paquete prepagado. Los otros
dos modos existen como opción por organización (`organization_billing_profiles.billing_mode`):

| Modo | Cuenta de WhatsApp | Quién le paga a Meta | Qué cobra Domicilia |
| :--- | :--- | :--- | :--- |
| **B. `domicilia_billed` (principal)** | La crea y opera Domicilia | Domicilia | Costo de Meta **más margen**, o un **paquete**; más suscripción e IA |
| **A. `client_billed`** | La crea el negocio (Embedded Signup) con su propio método de pago | El negocio, directo | Suscripción, IA medida y una tarifa de plataforma por mensaje; el costo de Meta solo se estima |
| **C. `managed_premium`** | La crea Domicilia a nombre del negocio y la opera | Domicilia o el negocio, según contrato | Suscripción o uso, como servicio gestionado |

**Condición que hay que confirmar con Meta ANTES de construir el cobro (riesgo abierto):** que Domicilia
pueda ser quien paga y revende. Según lo que conozco, con el programa de proveedor de tecnología cada
negocio agrega su propio método de pago a su cuenta; que un tercero pague por varias cuentas de negocios
distintos suele requerir ser *Solution Partner* con línea de crédito. Rutas posibles para el modo B:

1. **Solution Partner** con línea de crédito compartida (requisitos de volumen que hay que consultar).
2. **Cuentas de WhatsApp de Domicilia** con su método de pago (revisar las políticas de Meta sobre operar
   números de otros negocios en una misma cuenta).
3. **Proveedor (360dialog, Twilio)**: le paga a Meta, nos factura al por mayor y Domicilia revende. Es la
   salida si Meta no habilita las dos anteriores.

Por eso el envío sale por una **interfaz de proveedor** (`ChannelProvider`): hoy `MetaCloud`, mañana
`BSP`, sin tocar conversaciones, campañas ni cobro. Además la reventa trae obligaciones propias
(factura electrónica DIAN, IVA sobre el servicio revendido, riesgo de impago): la billetera **prepago** las
cubre y la factura a crédito queda solo para cuentas premium aprobadas.

### Cómo se cobra en el modo B

```
organization_billing_profiles   organización, billing_mode, moneda, condiciones (prepago | crédito aprobado),
                                datos fiscales
pricing_rules                   margen por categoría de mensaje y país, y por plan:
                                {porcentaje sobre el costo de Meta} o {precio fijo por mensaje}
message_packages                catálogo de paquetes: "1.000 mensajes de utilidad", precio, vigencia, categoría
organization_packages           saldo de cada paquete comprado: restante, vence
fx_rates                        tasa de cambio con vigencia (Meta tarifa en USD; el negocio paga en COP)
```

Orden de consumo al enviar un mensaje de plantilla: **1) paquete vigente de esa categoría, 2) billetera**.
El precio de cada mensaje se **congela al enviarlo** (costo de Meta, tasa de cambio, margen aplicado) en
`usage_events`, así una tarifa nueva no reescribe el pasado. Sin saldo ni paquete, la campaña no se lanza
y el envío suelto responde 402. El margen debe absorber la variación cambiaria: Meta cobra en dólares.

## 3.1 Cómo se decide el precio y el plan de cada cliente

El proceso se llama **segmentación de clientes con precios por valor** (*pricing & packaging*): se elige una
**métrica de valor** (lo que el cliente percibe y crece con su éxito: pedidos al mes, mensajes de
plantilla, números conectados, miembros) y se arma un esquema **híbrido**: cuota fija por plan más
consumo medido. Para decidir quién puede pagar a crédito (factura mensual, para clientes *premium*) se
hace una **evaluación de riesgo crediticio** (KYC, antigüedad, historial de pagos). La plataforma ya
puede medir por organización lo que ayuda a decidirlo (miembros, mensajes, campañas, pedidos):
`organization_usage_monthly` alimenta sugerencias de *upgrade* y reportes comerciales.

Palancas de monetización, sin fijar precios (los decide negocio):

1. **Suscripción por plan** (ya construida): habilita módulos y topes (números, miembros, mensajes).
2. **Costo de Meta con margen** o **paquetes prepagados** de mensajes (modo B, principal); en el modo A,
   una **tarifa de plataforma por mensaje**.
3. **Cupo incluido por plan** y paquetes de excedente.
4. **Galería de plantillas y onboarding asistido** como servicio de alta.
5. **IA medida** (tokens del agente) contra la misma billetera.
6. **Reportes** (entregabilidad, respuesta, costo por campaña, ventas atribuidas) en Pro/Outreach.
7. **Factura mensual por consumo** solo para cuentas premium aprobadas (evaluación crediticia).

## 4. Arquitectura

- **Un solo webhook de la app**: `GET/POST /webhooks/whatsapp` (la ruta `/webhooks/*` ya llega al
  core por Caddy). Se valida `X-Hub-Signature-256` con el secreto de la app, se persiste el evento crudo
  con su clave única y se responde 200 de inmediato; un trabajador lo procesa.
- **Envío asíncrono**: los mensajes salientes nacen en `queued` y los envía un trabajador (mismo
  mecanismo que las campañas): un fallo de Meta no rompe la petición del agente humano.
- **Tiempo real al frontend**: SSE por organización alimentado con `LISTEN/NOTIFY` de Postgres.
- **Agente de IA**: usa las mismas operaciones del core (`handled_by` bot|human, traspaso a un humano).
- **Onboarding**: credenciales pegadas primero (asistente para el negocio o gestión del superadmin) y
  Embedded Signup después; el token se cifra en reposo y nunca se devuelve por la API.
- **Permisos nuevos**: `org.inbox.read|reply|manage`, `org.contacts.read|manage`,
  `org.templates.read|manage`, `org.campaigns.read|manage|launch`, `org.wallet.read|manage`, y de
  plataforma `platform.billing.manage`, `platform.templates_gallery.manage`.
- **Funciones por plan**: las 19 de `plans.Features` mapean 1:1 a estos endpoints; se añaden los topes
  `max_inboxes` y `max_campaign_messages_month`.

## 5. Orden de construcción propuesto

1. **Canal + contactos + conversaciones + mensajes + webhook + envío de texto** (bandeja base, Starter):
   **primera parte elegida.**
2. **Plantillas** (constructor, galería, sincronización por webhook, envío de plantilla y ventana de 24 h).
3. **Consentimiento y segmentos.**
4. **Campañas** (cola por destinatario, ritmo, presupuesto).
5. **Billetera y libro de uso** (recarga con ePayco) y reportes.
6. Etiquetas, equipos, macros, CSAT, acciones masivas, notas (Pro).

## 6. Deuda que este módulo hace obligatoria

- **Limitación de frecuencia** en las rutas públicas y en el webhook (no pueden quedar sin protección).
- **Cifrado de secretos** en reposo (tokens de Meta).
- **Correo saliente desde el core** (invitaciones, avisos de saldo bajo o de plantilla rechazada).
- **RLS** de Postgres: con datos de conversaciones, una consulta sin filtro de organización filtraría
  chats ajenos; la Fase 4 ya recomendaba RLS y aquí deja de ser opcional.
