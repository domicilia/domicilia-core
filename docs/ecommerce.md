# Módulo de e-commerce: catálogo, pedidos, pagos y promociones

Diseño hecho antes de escribir migraciones (2026-09-28), siguiendo el mismo orden que
`organizaciones.md` y `whatsapp.md`.

**Estado: las cuatro piezas del módulo — `internal/catalog` (§1), `internal/orders` (§2),
`internal/payments` (§3) e `internal/promotions` (§4) — construidas y probadas el 2026-09-28** —
migraciones 00005 a 00008, endpoints, contrato OpenAPI, suite verde (`go test ./...`, `go vet`,
`golangci-lint` sin hallazgos), aplicado a la base de desarrollo. Ver §6 para las decisiones que
salieron al implementar cada una, algunas distintas de lo que este documento decía originalmente.

Canal: la ruta `/cliente/*` del frontend (ver `project_frontend_migration_gap` en memoria — Fase A
ya construida: shell, directorio de organizaciones). Es un canal DISTINTO de WhatsApp: el cliente
compra desde la app, no desde un chat. Ambos canales deben terminar creando el mismo tipo de
pedido en el mismo dominio — el catálogo y el pedido no le pertenecen a un canal.

## 0. Decisiones ya tomadas

- **Arquitectura**: monolito modular, no microservicios. Nuevos paquetes de dominio dentro de
  `domicilia-core` (`internal/catalog`, `internal/orders`, `internal/payments`,
  `internal/promotions`), mismo Echo + pgx + sqlc + goose + testcontainers de siempre. Razón:
  equipo de una persona, una sola VM, una sola base de datos compartida ya — separar en procesos
  distintos hoy compra la complejidad de lo distribuido (transacciones cruzadas, coordinación,
  más despliegues) sin ninguno de sus beneficios reales (equipos separados, escalado
  independiente, bases de datos separadas). La única pieza con un perfil de carga realmente
  distinto — tracking en vivo de domiciliarios (escritura geoespacial frecuente, WebSocket/SSE) —
  no es parte de esta ronda; si algún día se extrae algo, es esa.
- **Orquestador de pago**: paquete `internal/payments` con una interfaz `PaymentGateway`, no un
  servicio aparte. Mismo patrón que ya funciona en `internal/whatsapp/dispatcher.go`: cola propia
  (outbox), lease, reintentos con backoff solo en fallos transitorios, idempotencia por
  `(source, event_key)`. "Cobrar" y "marcar el pedido como pagado" siguen siendo una sola
  transacción de Postgres.
- **Modelo de custodia del pago (v2)**: el cliente paga en la pasarela; la pasarela liquida a
  Domicilia (una sola cuenta comerciante). Cuenta agregadora propia del restaurante y cuentas
  gateway propias (para Domicilia o para el restaurante) quedan **fuera de esta ronda** — anotadas
  como feature futura. La interfaz `PaymentGateway` se diseña para que esa variante entre después
  sin tocar el flujo de pedidos (ver §3).
- **Catálogo**: con variantes y modificadores desde el arranque (no la versión plana). Decisión
  del usuario, sabiendo que es más diseño antes de tener algo pedible.

## 1. Catálogo

### 1.1 Modelo

```
categories        (id, organization_id, name, position, is_active)
products          (id, organization_id, category_id, name, description, image_url, is_active)
product_variants  (id, product_id, organization_id, name, price_cents, is_default, is_active, position)
modifier_groups   (id, organization_id, name, selection_type, min_select, max_select, required)
modifier_options  (id, modifier_group_id, name, price_delta_cents, is_active, position)
product_modifier_groups (product_id, modifier_group_id, position)  -- N:M
```

Reglas:

1. **El precio SIEMPRE vive en la variante, nunca en el producto.** Un producto sin tamaños
   ("Coca-Cola") tiene una sola variante marcada `is_default = true` ("Regular"). Así hay un único
   camino para calcular un precio — nunca dos (`product.price` vs `variant.price`) que puedan
   desincronizarse.
2. **Los grupos de modificadores se reutilizan entre productos** (`product_modifier_groups` es la
   tabla puente): "Extras de pizza" se define una vez y se asocia a cada pizza, no se copia.
3. `selection_type` de un grupo es `single` (radio — ej. "Tamaño de bebida") o `multiple`
   (checkboxes — ej. "Extras"), con `min_select`/`max_select` para las reglas ("mínimo 1, máximo
   3"). `required = true` bloquea el checkout si no se eligió nada de ese grupo.
4. Todo lleva `organization_id` con la misma FK compuesta `(id, organization_id)` que ya usan
   inboxes/contacts/conversations — aislamiento en la base, no solo en el código.
5. Moneda: se hereda de `organization_settings.currency` (ya existe desde la Fase de
   organizaciones) — no se repite por producto.
6. Borrado: nunca DELETE real sobre `product_variants`/`modifier_options` una vez que algún pedido
   los referenció — se desactivan (`is_active = false`). El pedido histórico no puede quedar
   apuntando a una fila que ya no existe (ver snapshot en §2.2).

### 1.2 Permisos nuevos

`org.catalog.manage` (crear/editar productos, variantes, modificadores). Lectura del catálogo por
un cliente es pública dentro del contexto de "estoy viendo el menú de esta organización" — no
necesita permiso, igual que `GET /v1/public/organizations/{slug}`.

## 2. Pedidos

### 2.1 Máquina de estados

Mismo patrón que `organizations.Operation`: cada transición dice explícitamente desde qué estados
se permite, y quién la dispara.

```
draft ──place──▶ placed ──(pasarela ok)──▶ confirmed ──accept──▶ accepted ──startPreparing──▶ preparing
  │                 │                          │                                                  │
  │              (pasarela falla)              └──reject──▶ rejected                       dispatch│
  │                 ▼                                                                              ▼
  │           payment_failed                                                              out_for_delivery
  │                                                                                                 │
  └──cancel (desde draft/placed/confirmed/accepted, antes de preparing)──▶ cancelled       markDelivered
                                                                                                     ▼
                                                                                                delivered
```

| Transición | Estados de origen permitidos | Quién la dispara |
|---|---|---|
| `place` (confirmar carrito) | `draft` | cliente |
| `markPaid` | `placed` | webhook de la pasarela |
| `markPaymentFailed` | `placed` | webhook de la pasarela (el cliente puede reintentar el pago sobre el MISMO pedido: vuelve a `placed`, no crea uno nuevo) |
| `accept` | `confirmed` | negocio |
| `reject` | `confirmed` | negocio |
| `startPreparing` | `accepted` | negocio (o automático al aceptar, a decidir en implementación) |
| `dispatch` (sale a reparto) | `preparing` | negocio o el futuro módulo de asignación de domis |
| `markDelivered` | `out_for_delivery` | domi o negocio |
| `cancel` | `draft`, `placed`, `confirmed`, `accepted` | cliente o negocio — nunca desde `preparing` en adelante (ya hay comida hecha) |

Terminales: `delivered`, `rejected`, `cancelled`, `payment_failed` (si no se reintenta).

Deliberadamente NO se colapsa `confirmed` (pagado) con `accepted` (el negocio lo aceptó): son
hechos distintos — uno lo confirma la pasarela, el otro lo confirma el negocio — y mezclarlos
impediría después cobrar automáticamente sin esperar al negocio si algún día se quiere ese modo.

### 2.2 Pedido y líneas — snapshot de precio

```
orders       (id, organization_id, customer_id, status, subtotal_cents, discount_cents,
              delivery_fee_cents, total_cents, promotion_id?, placed_at, ...timestamps por transición)
order_items  (id, order_id, organization_id, product_variant_id,
              name_snapshot, unit_price_cents_snapshot, modifiers_snapshot jsonb, quantity)
```

**El nombre y el precio de cada línea se copian (`_snapshot`) al momento de `place`, no se leen en
vivo del catálogo.** Si el negocio sube el precio de una pizza mañana, el pedido de hoy no cambia
— exactamente el mismo problema que ya resolvimos con `PublicOrganization` vs. datos internos: el
pedido es un documento histórico, no una vista live. El FK a `product_variant_id` se mantiene (para
reordenar fácil / analítica), pero nunca se usa para mostrar precio ni para el total.

`draft` (el carrito) SÍ lee el catálogo en vivo — mientras el cliente compra, los precios que ve
son los actuales. El snapshot se toma una sola vez, en `place`.

### 2.3 Carrito = pedido en `draft`

No hay una tabla `carts` aparte: el carrito es un `order` en estado `draft`, mutable
(agregar/quitar líneas, cambiar cantidad). Mismo principio que "las conversaciones no tienen una
tabla de borradores aparte". Se necesita una limpieza periódica de `draft` abandonados (a definir
en implementación: TTL + job, o simplemente no limpiarlos y filtrarlos de reportes).

### 2.4 Permisos nuevos

`org.orders.manage` (aceptar/rechazar/cambiar estado). Lectura de sus propios pedidos por el
cliente: por `customer_id`, sin permiso de organización de por medio.

## 3. Pagos — **hecho** (`internal/payments`, `internal/epayco`)

```go
type Gateway interface {
    Name() string
    BuildCheckout(req ChargeRequest) (checkoutURL string, err error)
    ParseWebhook(r *http.Request) (WebhookEvent, error)
}
```

Dos diferencias con lo que este documento decía antes de construirlo:

1. **Sin cola de reintento para crear el cobro.** ePayco (y la mayoría de pasarelas
   latinoamericanas) usa checkout por redirección: `BuildCheckout` arma una URL firmada con la
   llave pública del comercio, sin llamar a ningún endpoint de ePayco. No hay nada que reintentar
   porque no hay ninguna llamada de red que pueda fallar por una caída de la pasarela — a
   diferencia de enviar un mensaje de WhatsApp, que sí llama a Meta. La cola de reintento con
   lease/backoff SÍ sigue aplicando, pero solo del lado del webhook (idempotencia), no de la
   creación del cobro.
2. **La interfaz no recibe el `Order` completo**, recibe un `ChargeRequest` armado por
   `payments.Service` a partir de lo que necesita (monto, moneda, referencia propia). Evita que
   `internal/payments` dependa del tipo `orders.Order` completo en la frontera de la pasarela —
   sigue dependiendo de él puertas adentro, a través de `OrdersGateway` (ver §6).

Resto según lo diseñado: `payment_events(source, event_key)` único para idempotencia del webhook,
mismo patrón que `webhook_events` de WhatsApp; a quién se le liquida (Domicilia hoy; cuenta
agregadora del restaurante o cuentas gateway propias, features futuras de §0) es un detalle de qué
`Gateway` está configurado, nunca un cambio en `internal/orders`.

**VERIFICAR ANTES DE PRODUCCIÓN**: `internal/epayco` se escribió contra la documentación pública
de ePayco (checkout por redirección + firma SHA-256 de la confirmación), no contra una cuenta de
pruebas real — mismo tipo de advertencia que el riesgo de Meta en `whatsapp.md`. Antes de mover
dinero real: confirmar con una cuenta sandbox de ePayco la URL exacta del checkout, los nombres de
los parámetros y el orden de la firma.

## 4. Promociones — **hecho** (`internal/promotions`)

```
promotions            (id, organization_id, code, discount_type, value, min_order_cents, starts_at,
                        ends_at, max_uses, per_customer_limit, is_active)
promotion_redemptions (id, promotion_id, organization_id, order_id, customer_id, discount_cents,
                        applied_at)
orders.promotion_id   -- columna nueva en orders, FK a promotions (SET NULL)
```

Tres diferencias con lo que este documento decía antes de construirlo:

1. **Se aplica al CARRITO, no en `place`.** `POST .../cart/promotion` valida el código contra el
   subtotal real y lo graba de inmediato en el carrito (`discount_cents`/`promotion_id`) — el
   cliente ve el descuento antes de confirmar, no como sorpresa al pagar. Cualquier cambio al
   carrito después (agregar/quitar una línea, cambiar cantidad) BORRA el código aplicado: un
   descuento calculado contra un subtotal que ya no existe no se puede dejar vivo. `place()` no
   recalcula el descuento, solo reconfirma `subtotal_cents` desde las líneas (mismo principio del
   snapshot de §2.2, pero del lado del descuento: si sigue aplicado al llegar a `place`, es porque
   nada cambió desde que se validó).
2. **`max_uses`/`per_customer_limit` cuentan CANJES REALES, no aplicaciones al carrito.** Un canje
   (`promotion_redemptions`) se graba al aplicar el código, pero solo CUENTA contra los límites si
   el pedido dejó de ser un borrador (`status <> 'draft'`) Y sigue siendo esa la promoción vigente
   en él (`orders.promotion_id` coincide) — un `JOIN` en `CountPromotionRedemptions` /
   `CountCustomerPromotionRedemptions`, sin que `internal/orders` necesite saber que
   `internal/promotions` existe. Así, aplicar un código y seguir editando el carrito (lo que borra
   `orders.promotion_id`) nunca gasta un cupo de verdad — evita que alguien agote un `max_uses`
   limitado sin llegar a confirmar un solo pedido.
3. **Dependencia angosta hacia `internal/orders`, nunca al revés** — mismo patrón que
   `payments.OrdersGateway` (§3, punto 2): `promotions.OrdersGateway` declara solo tres métodos
   (`GetCart`, `ApplyPromotion`, `RemovePromotion`) que `*orders.Service` satisface sin saberlo.

`org.promotions.read`/`org.promotions.manage` son delegables (un admin puede crear un rol de
"mercadeo"), pero a diferencia de `org.catalog.*`/`org.orders.*` NO se le dan a `employee` por
defecto: administrar cupones es una decisión de mercadeo, no rutina de atención al cliente.

## 5. Riesgos abiertos

- **Custodia de dinero de terceros en Colombia.** Domicilia cobrando y liquidando después al
  restaurante puede rozar regulación de intermediación de pagos. No bloquea el diseño ni la
  implementación de esta ronda, pero hay que confirmarlo con un contador/abogado antes de mover
  dinero real en producción — mismo tipo de riesgo abierto que ya existe con "¿puede Domicilia ser
  pagador de Meta?" en `whatsapp.md`.
- **Actualizaciones en vivo del estado del pedido al cliente** (SSE/WebSocket) — ya estaba en la
  lista de "siguientes slices" de WhatsApp; un pedido en `preparing`/`out_for_delivery` es
  justamente el caso de uso que lo justifica. No entra en esta ronda.
- **Asignación de domiciliarios** — `internal/drivers` hoy es solo alta/aprobación. `dispatch` en
  la máquina de estados de arriba asume que ALGO decide qué domi toma el pedido; ese ALGO no
  existe todavía y es su propio diseño, no un detalle menor.

## 6. Orden de construcción propuesto

1. ~~`internal/catalog`~~ — **hecho.** Dos decisiones que el diseño no fijaba y salieron al
   implementar: `org.catalog.manage` quedó delegable (administrar el menú es rutina de negocio,
   no tan sensible como conectar un número de WhatsApp — mismo trato que `org.contacts.manage`);
   y un grupo `single` exige `max_select = 1` (si no, "single" y "multiple" podrían decir cosas
   distintas al mismo tiempo).
2. ~~`internal/orders`~~ — **hecho.** El carrito (`GetOrCreateDraft`, un índice único parcial
   `WHERE status = 'draft'` evita duplicados) y la máquina de estados completa de §2.1.
   `internal/orders` depende de `internal/catalog` a través de una interfaz propia y angosta
   (`CatalogReader`, dos métodos), nunca de su interfaz completa — así puede evolucionar el
   catálogo sin arrastrar a pedidos. Dos rutas nuevas fuera de `/organizations/{org_id}` a
   propósito: `/v1/orders/mine` y `/v1/orders/{id}` — el cliente ve y cancela SUS pedidos sin
   depender de un permiso de organización que nunca va a tener. `mark-paid`/`mark-payment-failed`
   existieron un momento como sustituto temporal del webhook (exigían ser operador de plataforma)
   y ya NO existen: `internal/payments` (§3) los reemplazó por el flujo real.
3. ~~`internal/payments`~~ — **hecho.** Ver §3.
4. ~~`internal/promotions`~~ — **hecho.** Ver §4 — construido al final, como estaba previsto,
   porque depende de que el carrito (`internal/orders`) ya exista para poder aplicarle un código.

Contrato OpenAPI desde el primer paquete (`internal/catalog`), no al final — a diferencia de
`GET /v1/public/organizations`, que se sumó a un módulo ya existente sin contrato, esto es
superficie nueva de cara al cliente pagando: aplica el estándar acordado.

## 7. Siguiente ronda (en curso, 2026-10-05): feed multi-restaurante y fotos de producto

Pedida por el usuario sobre `/cliente`: una página de Inicio con tarjetas de producto estilo
Rappi/UberEats/DiDi (varias organizaciones a la vez, categorías, filtros), renombrar
"Organizaciones" a "Restaurantes" en la navegación del cliente, un carrito visible en todas las
rutas, y una interfaz de organización para crear/publicar productos con foto, ingredientes y
adicionales.

**Decisiones tomadas:**
- **Varios carritos simultáneos (uno por restaurante) está bien** — comprar pizza en uno y sushi
  en otro no es un conflicto. No hizo falta ningún cambio de backend: `GetOrCreateDraftOrder` (§2)
  ya aísla el carrito por `(organization_id, customer_id)`, así que esto ya funcionaba.
- **`domicilia-agent` no tiene ningún concepto de producto/menú/catálogo** (verificado
  directamente, cero coincidencias) — nada que reconciliar hoy. `internal/catalog` sigue siendo la
  única fuente; cuando el agente de WhatsApp venda productos, va a llamarlo a él, nunca a
  duplicar su propio modelo.
- **Fotos de producto: subida real de archivos**, no pegar una URL (a diferencia del logo de la
  organización) — decisión explícita del usuario. Ver `domicilia-infra/modules/storage`: cuenta
  de Azure Blob Storage DEDICADA (`stdomiciliamediark0d4`, container `product-media`,
  `AllowBlobPublicAccess: true` SOLO en esta cuenta — nunca en la de respaldos de Postgres, que
  comparte ese interruptor a nivel de cuenta completa, no por contenedor). Primera storage account
  del proyecto gestionada por OpenTofu (las otras dos, tfstate y respaldos, son manuales por
  precedente histórico, no por necesidad).

**`internal/platform/azblob` — hecho.** Sube blobs autenticándose con la identidad administrada
del VM vía IMDS (token para el recurso `storage.azure.com`, luego un `PUT` Blob por REST) — cero
SDK de Azure, cero credenciales en disco, mismos endpoints que `backup-postgres.sh` (ya en
producción). `catalog.MediaUploader` es la interfaz angosta de siempre (mismo patrón que
`payments.Gateway`): `nil` sin `CORE_MEDIA_STORAGE_ACCOUNT` configurada responde 503. El tipo de
archivo se valida por contenido real (`http.DetectContentType` sobre los primeros 512 bytes;
jpeg/png/webp/gif), nunca por el nombre o el Content-Type que mande el cliente.

**Gotcha de alcance general, no solo de este endpoint**: subir una foto real necesita más de 1 MiB
por petición, pero `middleware.BodyLimit` de Echo se conecta UNA sola vez en la raíz (`e.Use`) —
no hay forma limpia de darle a una sola ruta un límite más alto que el global. Subir
`CORE_MAX_BODY_BYTES` (1 MiB → 8 MiB) para que las fotos entren rompió sin avisar la prueba del
webhook de WhatsApp que esperaba que un cuerpo "enorme" (2 MiB) se cortara — dejó de serlo contra
el nuevo techo. Cualquier cambio futuro a este límite global debe revisar pruebas de tamaño de
cuerpo en rutas que no tienen nada que ver con la nueva razón del cambio.

**Backend del feed — hecho (mismo día).** Tres piezas nuevas, sin ninguna migración (ninguna
columna nueva, solo consultas sobre lo que ya existía):

- `GET /v1/public/products` — el feed mezclado, sin sesión. Solo organizaciones `active` y
  productos con al menos una variante activa (`JOIN`, no `LEFT JOIN`, con `product_variants`: un
  producto sin nada que vender no entra). Filtros opcionales: `organization_id` (la vitrina de UN
  restaurante reusa esta misma consulta), `q` (nombre), `category` (nombre de categoría, sin
  distinguir mayúsculas — las categorías son por organización, no una taxonomía compartida, así
  que es una coincidencia de texto a propósito, no un id). Devuelve `catalog.FeedProduct`:
  liviano, sin variantes ni modificadores, con `min_price_cents` (la variante activa más barata)
  para el "desde $X" de la tarjeta.
- `GET /v1/public/organizations/{org_id}/products/{product_id}` — el detalle completo (variantes
  + grupos de modificadores con sus opciones) para configurar y agregar al carrito. Mismo gate que
  el carrito (`tenant.Gate.OpenForCustomer`: la organización debe existir y estar activa, sin
  exigir ningún permiso).
- `GET /v1/carts/mine` — "mis carritos", simétrico a `/v1/orders/mine` pero al revés
  (`status = 'draft'` en vez de `<> 'draft'`): los borradores NO vacíos del cliente, cruzando
  organizaciones. Esto es lo que hace posible un indicador de carrito visible en cualquier ruta del
  cliente sin inventar nada nuevo en el modelo — un cliente ya podía tener un carrito abierto por
  restaurante a la vez (§2), solo faltaba una forma de listarlos todos juntos.

Full suite/vet/lint verde, rutas verificadas en vivo contra el contenedor de desarrollo.

**Frontend del feed — hecho (mismo día, domicilia-frontend).** Las tres rutas de arriba ya tienen
quien las consuma:

- **Renombrado completo** `organizaciones` → `restaurantes` en `/cliente/*` (segmento de ruta,
  carpeta, nav, textos) — de cara al cliente son "restaurantes"; "organización" sigue siendo el
  término correcto en el backend y en la consola de negocio, no se tocó ahí.
- **`/cliente/inicio` es enteramente client-side** (sin fetch en el Server Component): búsqueda
  (debounce de 300ms), chips de categoría derivados del propio feed sin filtrar (si se derivaran
  del feed ya filtrado, los chips de las demás categorías desaparecerían al elegir una), y
  tarjetas de producto. "Agregar" primero mira el detalle completo — el feed no trae variantes a
  propósito — y decide ahí: si el producto tiene una sola variante activa y ningún grupo de
  modificadores obligatorio, lo agrega directo; si no, abre un diálogo de configuración
  (`ConfigureProductDialog`). El estado del diálogo no usa ningún `useEffect` para
  inicializarse: MUI desmonta el contenido del `Dialog` al cerrarlo (`keepMounted` es `false` por
  omisión), así que el estado ya nace limpio en cada apertura, y la variante por defecto se
  DERIVA de `detail` en vez de copiarse a un estado propio.
- **Indicador de carrito en `ClienteShell`** (header, visible en cualquier ruta de `/cliente/*`):
  ícono con el total de productos cruzando restaurantes (`GET /v1/carts/mine`) y un menú con cada
  carrito abierto. React Query (ya era una dependencia del proyecto, con `QueryProvider` ya
  envolviendo toda la app desde el layout raíz) invalida esa consulta apenas se agrega algo, así
  el número del header queda al día sin recargar nada.
- **Gotcha real, no solo teórico esta vez**: `lib/api/catalog.ts` mezcló en un mismo módulo
  `fetchProductFeed` (usa `serverApiFetch`, que depende de `next/headers`) con las variantes para
  Client Components — y como `ProductCard.tsx` ("use client") importaba ese módulo, el build de
  Next.js fallaba de verdad (no un lint, un error de compilación real, visto en los logs del
  contenedor de desarrollo). Arreglado separando en `catalog.ts` (solo lo seguro para el
  navegador) y `catalog.server.ts` (la variante de servidor, sin uso todavío — queda lista para
  cuando exista una vitrina por restaurante con primer render en servidor) — mismo patrón ya
  existente entre `lib/api/client.ts` y `lib/api/server-fetch.ts`. Cualquier archivo nuevo en
  `lib/api/` que mezcle los dos debe separarse así desde el principio.
- **El proxy `/api/backend` se extendió** para dejar pasar `/v1/public/*` SIN exigir sesión —
  antes cortaba con 401 cualquier petición sin cookie, lo que habría impedido que un visitante sin
  cuenta navegara el feed o viera el detalle de un producto antes de decidir registrarse.

`tsc --noEmit` y `eslint` limpios (un hallazgo real de `react-hooks/set-state-in-effect` en el
diálogo de configuración, corregido con el patrón de arriba). Las tres rutas nuevas y el feed
verificados en vivo contra los contenedores de desarrollo (`core` y `web`) — sin datos de catálogo
sembrados todavía en esa base, así que el feed vacío y el flujo real de "Agregar" (clic en un
navegador) no se ejercieron de punta a punta, solo por las pruebas automáticas del backend y la
verificación de que las rutas cargan sin errores de build/runtime.

## 8. Interfaz de organización + vitrina del restaurante — hecho (mismo día)

Cierra el resto de la ronda: el campo de "canal" (migración 00009), la interfaz de organización
para crear/publicar productos, y la vitrina pública de un restaurante.

**`products.channels`/`products.ingredients` — hecho.** Migración 00009: `channels text[]`
(CHECK contra `ARRAY['ecommerce','whatsapp']`) y `ingredients text[]`, ambos `NOT NULL DEFAULT
'{}'`. `GET /v1/public/products`/`CountPublicProducts` ahora exigen
`'ecommerce' = ANY(p.channels)` además de `is_active` — un producto activo YA NO aparece en el
feed solo por existir, hay que publicarlo a propósito (`PATCH .../products/{id}` con
`channels: ["ecommerce"]`; `channels: []` lo despublica sin desactivarlo). `organization_slug` se
agregó como alternativa a `organization_id` en el mismo feed — la vitrina de un restaurante solo
conoce el slug de su URL, nunca el id.

**Interfaz de organización (`domicilia-frontend`, `/{tenant}/admin/products`) — hecho.** Antes era
un stub de la época de FastAPI (`StaffProductsPage`, tabla vacía estática); ahora resuelve el id
real de la organización vía `GET /v1/organizations/by-slug/{slug}` (autenticado, RBAC real) y
ofrece: crear/editar producto (nombre, descripción, categoría con alta rápida inline, variantes
dinámicas, adicionales = grupos de modificadores existentes con alta rápida en un diálogo aparte,
ingredientes como texto separado por comas, el interruptor "Publicar en el feed de clientes"), y
—solo editando, porque la foto necesita un id— subir la foto con el endpoint de la fase anterior.
Listado con chips de estado (Activo/Inactivo) y de publicación (Publicado/Sin publicar).

**Vitrina del restaurante (`/{tenant}/pedir`) — hecho.** Reemplaza el stub `CustomerPedirPage`.
Server Component: pide en paralelo el perfil público (`GET /v1/public/organizations/{slug}`) y el
menú (`GET /v1/public/products?organization_slug={slug}`), agrupa por categoría, y le pasa todo a
un Client Component con el menú (reusa `ProductCard`/`ConfigureProductDialog` de Inicio, ocultando
el chip de organización — es obvio en qué restaurante está) y un panel de carrito al lado
(cantidad +/-, quitar línea, "Confirmar pedido" → `POST cart/place`). El perfil público NO trae el
id real de la organización a propósito (ver §7) — la página lo toma del primer producto del feed,
que sí lo trae; sin productos no hay carrito que mostrar, tampoco falta.

Verificado de punta a punta con datos reales, no solo con el servidor respondiendo vacío: se creó
una categoría, un grupo de adicionales y un producto publicado por la API autenticada real (JWT
real de GoTrue contra el admin sembrado `orgadmin@domicilia.dev`), y se confirmó que aparece en el
feed público, en el detalle público, y en la página de la vitrina renderizada por Next.js — los
tres con datos reales, no solo contratos vacíos. La página de administración sin sesión real
muestra el error esperado ("credenciales requeridas o inválidas"), no un 500 — no se probó el
flujo autenticado completo en un navegador real (login → clic en la UI), solo la ruta de error y
la ruta de datos reales por API directa.

**Pendiente**: checkout/pago en el frontend (`internal/payments`/ePayco existen en el backend,
nada en el frontend llama `POST .../pay` ni recibe la confirmación); "Mis pedidos"/pagos/
promociones del cliente siguen siendo paneles "próximamente"; el agente de WhatsApp sigue sin
ningún concepto de producto (cuando lo tenga, usará `channels` incluyendo `"whatsapp"`).
