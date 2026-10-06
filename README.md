# domicilia-core

Core API de Domicilia — **Go + Echo v5 + pgx + sqlc**.

Repositorio: `github.com/domicilia/domicilia-core`

## Por qué existe

`domicilia-api` (FastAPI) nació como prototipo. El core transaccional
(organizaciones, catálogo, pedidos, clientes, domiciliarios) pasa a Go por
concurrencia (webhooks de WhatsApp), despliegue liviano (~25 MB) y porque
`auth-domicilia` ya es Go. **FastAPI se conserva solo para IA**
(LangChain/LangGraph), que llama a este core mediante *tools*:

```
WhatsApp/Meta ─► domicilia-core (Go) ──HTTP interno──► FastAPI (LangChain/LangGraph)
Frontend web  ─►        │                                        │
                        ▼                                        ▼
                  Postgres (RLS)  ◄────────── tools = endpoints del core
```

Reglas de esa frontera: el core es la única fuente de verdad del negocio; el
agente actúa por sus endpoints (así el RLS y las reglas se aplican una vez);
el webhook de WhatsApp entra por Go, responde 200 rápido y encola.

## Arranque rápido

**En el devstack** (lo normal): `make up` ya lo levanta junto con API, frontend y auth.
Su `.env` se genera solo desde `domicilia-agent/.env`.

```bash
cd ../domicilia-devstack
make up           # todo el stack, core incluido
make up-core      # solo el core (reconstruye y levanta)
make logs-core
curl localhost:8080/healthz
```

**Sin Docker** (necesita Postgres accesible y las variables de `.env.example`):

```bash
cp .env.example .env    # y rellénalo; con CORE_DATABASE_URL apuntando a localhost
make migrate            # crea/actualiza el esquema (auth-domicilia debe haber arrancado antes)
make dev                # recarga en caliente
```

## Endpoints

Todo cuelga de `/v1` y exige JWT de GoTrue **más** un usuario de negocio activo
(`public.users`), salvo lo marcado. Además cada ruta exige un **permiso** (ver "Roles
y permisos"). Los errores salen en RFC 9457 (`application/problem+json`) con `detail`
y `request_id`.

| Ruta | Permiso | Qué hace |
|---|---|---|
| `GET /healthz` · `GET /readyz` | público | Vida del proceso · disponibilidad (comprueba Postgres) |
| `GET /v1/whoami` | JWT | Identidad del token. Prueba de humo de toda la cadena |
| **Sesión y clientes** | | |
| `POST /v1/users` | JWT (sin exigir perfil) | Alta de un cliente ya registrado en GoTrue: crea usuario, perfil de cliente y rol `customer`. Toma id y correo **del token** |
| `GET /v1/users/me` | usuario | Datos, roles y permisos de plataforma, organizaciones (rol y permisos en cada una) y perfil de cliente |
| `PATCH /v1/users/me` | usuario | Edita nombre, teléfono y dirección. Un campo ausente o `null` no se toca |
| `GET` · `PATCH /v1/customers/me` | cliente | El perfil de cliente como recurso propio (404 si no es cliente) |
| **Organizaciones** | | |
| `POST /v1/organizations` | `platform.organizations.create` | Crea una organización completa: slug (derivado o explícito), plan, ajustes, suscripción inicial y, con `admin_email`, la invitación de su primer administrador. Una transacción |
| `GET /v1/organizations` | usuario | Quien accede a todas ve todas (menos archivadas); los demás, solo las suyas |
| `GET /v1/organizations/{id}` · `/by-slug/{slug}` | miembro | 403 si no es tuya, 404 si no existe; suspendida → 403 y archivada → 404 para sus miembros (el operador entra siempre) |
| `PATCH /v1/organizations/{id}` | `org.settings.manage` | Nombre y descripción. El slug no cambia nunca |
| `GET` · `PATCH /v1/organizations/{id}/settings` | `org.settings.read` · `.manage` | Contacto, logo, zona horaria, moneda y horario de atención |
| `GET /v1/organizations/{id}/entitlements` | miembro | Plan, funciones efectivas (plan ± excepciones) y uso frente a los límites |
| `GET /v1/organizations/{id}/subscription` | `org.billing.read` | Historial de planes (la vigente primero) |
| `GET /v1/organizations/{id}/members` | `org.members.read` | Miembros |
| `POST /v1/organizations/{id}/members` | `org.members.manage` | Suma a un usuario existente con un rol (código o id) |
| `POST /v1/organizations/{id}/members/invite` | `org.members.manage` | **Obsoleta.** Crea la cuenta en GoTrue con contraseña temporal; usar las invitaciones |
| `PATCH /v1/organizations/{id}/members/{user_id}` | `org.members.manage` | Cambia el rol de un miembro. No puede dejar la organización sin administrador (409) |
| `DELETE /v1/organizations/{id}/members/{user_id}` | `org.members.manage` | Saca a un miembro. Misma protección del último administrador |
| **Invitaciones** | | |
| `GET` · `POST /v1/organizations/{id}/invitations` | `org.members.manage` | Pendientes · invita a un correo con un rol; devuelve el enlace (`accept_url`). Reinvitar reemplaza el enlace |
| `POST …/invitations/{inv}/resend` · `DELETE …/invitations/{inv}` | `org.members.manage` | Reenvía con enlace nuevo · revoca |
| `POST /v1/invitations/preview` | **público** | Organización, rol y correo de un token (404 igual para inválido, caducado o revocado) |
| `POST /v1/invitations/accept` | JWT (sin exigir perfil) | Acepta con la sesión de la persona invitada: el correo del token debe ser el de la invitación. Crea el perfil si falta |
| **Cara pública del negocio** | | |
| `GET /v1/public/organizations/{slug}` | **público** | Nombre, logo, ciudad, horario y `open_now`. Nada interno; inexistente, suspendida y archivada responden igual |
| `GET /v1/plans` | usuario | Catálogo de planes, funciones y límites |
| **Roles de una organización** | | |
| `GET` · `POST /v1/organizations/{id}/roles` | `org.roles.read` · `org.roles.manage` | Roles disponibles (de sistema + propios) · crea uno propio |
| `PATCH` · `DELETE /v1/organizations/{id}/roles/{role_id}` | `org.roles.manage` | Edita/borra un rol propio (los de sistema no; uno en uso tampoco) |
| `GET /v1/organizations/{id}/audit` | `org.audit.read` | Auditoría de roles de esa organización |
| **WhatsApp: bandejas** (contrato en `openapi/openapi.yaml`) | | |
| `GET /v1/organizations/{id}/inboxes[?include_archived]` | `org.inbox.read` | Bandejas con su número y estado. Nunca trae el token |
| `POST …/inboxes/whatsapp` | `org.inbox.manage` | Conecta un número: valida contra Meta que el token vea el número, guarda el token **cifrado**. 402 si ya hay una bandeja y el plan no es Enterprise; 409 si el número o la cuenta ya están conectados; 503 sin llave de cifrado |
| `GET` · `PATCH` · `DELETE …/inboxes/{inbox_id}` | `.read` · `.manage` | Leer · renombrar · desconectar (libera el número; las conversaciones se conservan) |
| `PUT …/inboxes/{inbox_id}/credentials` | `org.inbox.manage` | Cambia el token (se valida contra Meta) y reactiva un canal en `needs_reauth` |
| `POST …/inboxes/{inbox_id}/refresh` | `org.inbox.manage` | Relee de Meta calidad y nivel del número; si Meta rechaza el token, el canal pasa a `needs_reauth` |
| **WhatsApp: contactos** | | |
| `GET` · `POST …/contacts` | `org.contacts.read` · `.manage` | Lista con cursor y búsqueda (`q`) · crea (teléfono E.164, único por organización) |
| `GET` · `PATCH …/contacts/{contact_id}` | `.read` · `.manage` | Leer · cambio parcial (el teléfono no se edita) |
| **WhatsApp: conversaciones** | | |
| `GET …/conversations` | `org.inbox.read` | Con cursor; filtros `status`, `assignee` (`me`, `unassigned`, id), `inbox_id`, `q` |
| `GET …/conversations/counts` | `org.inbox.read` | Contadores de las pestañas: mías, sin asignar, todas |
| `GET` · `PATCH …/conversations/{id}` | `.read` · `org.inbox.reply` | Leer (trae `can_reply` y `window_expires_at`) · estado, prioridad, responsable, bot/persona |
| `POST …/conversations/{id}/read` | `org.inbox.read` | Pone en cero lo no leído |
| `GET …/conversations/{id}/messages` | `org.inbox.read` | Hilo, del más nuevo al más viejo, con cursor |
| `POST …/conversations/{id}/messages` | `org.inbox.reply` | Encola un texto (202). 409 con el motivo (`window_closed`, `contact_blocked`, `inbox_disconnected`); 402 si el plan no incluye `inbox_24h` |
| **Webhook de Meta** | | |
| `GET` · `POST /webhooks/whatsapp` | **público** (token de verificación / firma HMAC) | Suscripción y eventos. Cada evento se autoriza con la firma de SU número; idempotente |
| **Domiciliarios** | | |
| `POST /v1/driver-applications` | **público** | Postulación. No crea ninguna cuenta: solo deja una solicitud pendiente |
| `GET /v1/driver-applications[?status_filter=]` | `platform.drivers.review` | Lista de solicitudes |
| `POST /v1/driver-applications/{id}/approve` · `/reject` | `platform.drivers.review` | Aprueba (crea la cuenta y da el rol `delivery`) · rechaza |
| **Plataforma: usuarios** | | |
| `GET /v1/platform/users` | `platform.users.read` | Listado paginado (`search`, `role`, `organization_id`, `is_active`, `limit`, `offset`) |
| `GET /v1/platform/users/{id}` | `platform.users.read` | Usuario con roles, organizaciones y perfil de cliente |
| `PATCH /v1/platform/users/{id}/active` | `platform.users.manage` | Activa/desactiva una cuenta (no la propia, no al último superadmin) |
| **Plataforma: roles y permisos** | | |
| `GET /v1/platform/permissions` | `platform.roles.read` | Catálogo de permisos |
| `GET` · `POST /v1/platform/roles` | `platform.roles.read` · `.manage` | Todos los roles (filtros `scope`, `organization_id`) · crea uno de plataforma |
| `GET` · `PATCH` · `DELETE /v1/platform/roles/{id}` | `.read` · `.manage` | Ver / editar / borrar un rol de plataforma a medida |
| `POST /v1/platform/users/{id}/roles` | `platform.roles.manage` | Da un rol de plataforma (por código o id). Idempotente |
| `DELETE /v1/platform/users/{id}/roles/{role}` | `platform.roles.manage` | Quita un rol de plataforma |
| `PATCH /v1/users/{id}/promote` | `platform.roles.manage` | Da el rol `superadmin` (atajo histórico) |
| `PATCH /v1/platform/users/{id}/delivery` | `platform.roles.manage` | Da/quita el rol `delivery` (atajo histórico) |
| `GET /v1/platform/audit` | `platform.audit.read` | Auditoría de roles de toda la plataforma (filtros `user_id`, `organization_id`, `action`) |
| **Plataforma: inquilinos** | | |
| `GET /v1/platform/overview` | `platform.overview.read` | Organizaciones (no archivadas) y contadores |
| `GET /v1/platform/organizations` | `platform.organizations.access_all` | Listado paginado con `q` (nombre o slug), `status`, `plan_tier`, `limit`, `offset` |
| `POST /v1/platform/organizations/{id}/suspend` · `/archive` | `platform.organizations.manage` | Exigen `reason`; quedan auditadas. Suspender solo desde activa; archivar desde activa o suspendida |
| `POST /v1/platform/organizations/{id}/reactivate` · `/restore` | `platform.organizations.manage` | Reactivar solo desde suspendida. Restaurar solo desde archivada y la deja **suspendida** |
| `PUT /v1/platform/organizations/{id}/plan` | `platform.organizations.manage` | Cambia el plan (`plan_tier`, `reason`). 409 si ya lo tiene, si está archivada o si el plan nuevo no admite sus miembros actuales |
| `PUT` · `DELETE /v1/platform/organizations/{id}/feature-overrides/{feature}` | `platform.organizations.manage` | Enciende/apaga una función por encima del plan · la quita |
| `PATCH /v1/platform/organizations/{id}` | `platform.organizations.manage` | **Compatibilidad** con el frontend actual (`is_active`, `plan_tier`): idempotente, delega en lo anterior |

Las rutas públicas de negocio son tres: la postulación de domiciliarios, la cara pública
de un negocio y la vista previa de una invitación (el token es la credencial); quien las usa
todavía no tiene cuenta. Además, el webhook de WhatsApp (`/webhooks/whatsapp`) lo llama Meta y se
autentica con firma, no con JWT. Una prueba lee **todas las rutas del router real** y
falla si alguna, fuera de una lista blanca justificada, responde algo distinto de 401
sin credenciales.

## Roles y permisos

Los roles son **datos** (tablas), no booleanos ni un enum: se pueden crear roles
nuevos sin migrar. Los permisos, en cambio, los define el **código**, porque son los
que las rutas exigen (`internal/access`).

| Rol de sistema | Alcance | Qué es |
|---|---|---|
| `superadmin` | plataforma | Operador del servicio: todos los permisos de plataforma, y acceso total a toda organización (`platform.organizations.access_all`) |
| `delivery` | plataforma | Domiciliario: reparte para cualquier organización |
| `customer` | plataforma | Cliente B2C |
| `admin` | organización | Administra su organización (todos los permisos de organización) |
| `employee` | organización | Call center: ve su organización, no administra |

Un usuario tiene **varios roles de plataforma** y **un rol por organización** (puede
ser admin en una y empleado en otra). Además de los de sistema, el superadmin crea
roles de plataforma a medida y cada organización crea los suyos (`/organizations/{id}/roles`).

**Regla que gobierna todo: nadie otorga más de lo que tiene.** Un admin de organización
solo puede crear y asignar roles cuyos permisos ya tiene *y* que sean **delegables**
(los de administración no lo son); el rol `admin` no es asignable por una organización.
Sin esto se fabricaría un rol equivalente a "admin" y el rol de administrador se
propagaría solo. La misma regla rige al dar o quitar roles de plataforma.

Protecciones: la plataforma no puede quedarse sin un superadmin activo (ni quitando el
rol ni desactivando la cuenta, y con bloqueo para el caso simultáneo); nadie se quita a
sí mismo el rol de superadmin ni desactiva su propia cuenta; los roles de sistema no se
editan ni se borran; un rol en uso no se borra.

**Auditoría:** cada cambio de rol (crear/editar/borrar un rol, dar/quitar un rol de
plataforma, sumar/invitar/cambiar/sacar un miembro, activar/desactivar una cuenta) se
escribe en `role_audit_log` en la **misma transacción** que el cambio.

## Comandos del binario

```bash
api                          # arranca el servidor
api healthcheck              # sondea /healthz (lo usa el HEALTHCHECK de Docker)
api migrate up|down|status   # migraciones de goose (down se niega en staging/producción)
api migrate adopt            # UNA vez en staging/producción: ver "Quién es dueño del esquema"
api create-superadmin        # crea/promueve al superadmin de la plataforma (bootstrap)
api seed                     # SOLO desarrollo: una organización y un usuario por rol (se niega en staging/prod)
```

`create-superadmin` es la única forma de tener el primer superadmin. Pide correo
(escrito dos veces) y contraseña sin eco; también acepta `SUPERADMIN_EMAIL`,
`SUPERADMIN_NAME`, `SUPERADMIN_PASSWORD` y `SUPERADMIN_RETIRE` (correos a retirar,
p. ej. las cuentas semilla). Es idempotente y **nunca cambia la contraseña de
alguien que ya existe**. En el stack:

```bash
docker compose run --rm -it core create-superadmin
```

## Configuración

Todas por variable de entorno; el servicio **no arranca** si falta o es inválida
una obligatoria. Ver `.env.example`.

| Variable | Por defecto | Notas |
|---|---|---|
| `CORE_DATABASE_URL` | — (obligatoria) | No es `DATABASE_URL`: esa es la de GoTrue |
| `GOTRUE_JWT_SECRET` | — (obligatoria) | ≥ 32 caracteres; el mismo secreto que firma GoTrue |
| `GOTRUE_JWT_AUD` | `authenticated` | |
| `CORE_AUTH_URL` | `http://localhost:9999` en desarrollo | Admin API de auth-domicilia. **Obligatoria en staging/producción** |
| `CORS_ORIGINS` | vacío | `*` se rechaza en staging/producción |
| `CORE_SMTP_HOST` · `_PORT` · `_USER` · `_PASS` · `_FROM` · `_FROM_NAME` | vacío (no envía) · `587` · … · `Domicilia` | Correo saliente del core por SMTP con STARTTLS (mismo servidor que auth-domicilia). Sin host, solo registra en el log; con host, usuario, contraseña y remitente son obligatorios. Nunca envía credenciales sin STARTTLS |
| `CORE_SECRETS_KEY` | vacío | 32 bytes en base64 (`openssl rand -base64 32`). Cifra los tokens de Meta en reposo. Sin ella conectar WhatsApp responde 503 (nunca se guarda un token en claro). **Perderla deja los tokens guardados ilegibles: respaldarla** |
| `CORE_WHATSAPP_APP_SECRET` · `CORE_WHATSAPP_VERIFY_TOKEN` | vacío | Secretos de la app de Meta de la plataforma: firma de los webhooks · desafío de suscripción. Sin ellos el webhook no acepta eventos de esa app |
| `CORE_WHATSAPP_API_BASE` · `_API_VERSION` | `https://graph.facebook.com` · `v23.0` | Graph API. `http` solo fuera de staging/producción |
| `CORE_APP_URL` | primer origen de `CORS_ORIGINS`; en desarrollo `http://localhost:3000` | URL pública del frontend, para el enlace de las invitaciones. En staging/producción falla si no hay ni esta ni CORS |
| `CORE_HTTP_ADDR` | `:8080` | |
| `APP_ENV` | `development` | `staging`/`production` activan validaciones estrictas y logs JSON |
| `CORE_REQUEST_TIMEOUT` | `15s` | Solo aplica a `/v1` |
| `CORE_SHUTDOWN_TIMEOUT` | `15s` | Cierre ordenado ante `SIGTERM` |
| `CORE_MAX_BODY_BYTES` | `1048576` | 413 si se excede |
| `CORE_DB_MAX_CONNS` | `10` | |

## Desarrollo

```bash
make check    # vet + lint + test — antes de cada push
make sqlc     # regenera internal/store tras cambiar db/queries o db/migrations
```

**Las pruebas necesitan Docker**: las de contrato levantan un Postgres real con
las migraciones reales (testcontainers), nunca SQLite ni mocks. Sin Docker se
omiten (con un aviso); en el CI `TESTS_DB_REQUIRED=1` convierte esa omisión en
fallo. El primer arranque del contenedor tarda ~40 s; cada prueba, milisegundos.

Ver `CLAUDE.md` para estructura, reglas y cómo añadir un dominio.

## Imágenes

- `Dockerfile.dev` — alpine + `air` (recarga en caliente).
- `Dockerfile.prod` — binario estático en `distroless/static:nonroot`, ~25 MB,
  sin shell. Puerto 8080; el TLS lo termina Caddy. Las migraciones van dentro
  del binario (`api migrate ...`).

## Estado de la migración desde `domicilia-api`

Migración incremental (*strangler*): Caddy enruta por ruta al core o a FastAPI;
ambos usan la misma base y validan el mismo JWT.

| Módulo | Líneas Python | Estado |
|---|---|---|
| Esqueleto (config, http, JWT, salud, Docker, devstack) | — | ✅ hecho |
| Esquema con goose (`00001` base + `00002` RBAC + `00003` ciclo de vida de organizaciones, `api migrate`) | — | ✅ hecho y verificado (ver abajo) |
| `users` (`/me`, alta, gestión por el operador) | 136 | ✅ portado y ampliado |
| `customers` (perfil de cliente) | (dentro de users) | ✅ dominio propio |
| `organizations` (miembros, invitaciones, cambio de rol) | 238 | ✅ portado y ampliado con ciclo de vida, planes, ajustes e invitaciones con token (`docs/organizaciones.md`) |
| `drivers` (postulaciones y aprobación) | 137 | ✅ portado; llama al admin API de GoTrue |
| `platform` → `saas` (panorama, suspender inquilinos) | 74 | ✅ portado |
| **Roles y permisos (RBAC), auditoría** | 0 (no existía) | ✅ nuevo en Go (`roles`, `access`, `audit`) |
| `scripts/create_superadmin.py` | 163 | ✅ portado (`api create-superadmin`) |
| `scripts/seed.py`, `scripts/migrate_users_to_gotrue.py` | 228 | ⏳ no portados: datos de demo y una migración de una sola vez |
| **Desvío del tráfico** (frontend + Caddy + servidor) | — | ⏳ **siguiente**: ver "Contrato HTTP" |
| Tenant + RLS (`SET LOCAL` por transacción, rol de BD limitado) | — | ⏳ bloqueado por decisiones del tema B |
| Catálogo y pedidos | 0 (no existe) | ⏳ nace directamente en Go |

**Desvío del tráfico — dónde está cada pieza:**

| Entorno | Estado |
|---|---|
| **Devstack (local)** | ✅ **Hecho.** El frontend habla con el core; el contenedor FastAPI (`api`) salió del arranque por defecto (perfil `agent`) y está detenido. La base pasó por `adopt` + `00002_rbac` con los datos reales, respaldada antes. |
| **Servidor (staging y producción)** | 🟡 **Preparado, sin desplegar.** El stack (`core` en lugar de `api`, alias `api-*` en `:8080`), Caddy, `fetch-secrets.sh` y los workflows `deploy.yml`/`promote.yml` de este repo están escritos y validados (`docker compose config`, `actionlint`), pero **no se han ejecutado en el servidor**. El runbook está en `domicilia-infra/deploy/README.md`, "Pasar el backend a domicilia-core". |

> **`00002_rbac` y `domicilia-api` no pueden coexistir.** La migración reemplaza
> `users.is_general_admin`, `users.is_delivery` y el enum `user_organizations.role` por
> el modelo de roles, y esas columnas son las que FastAPI todavía lee y escribe. Debe
> aplicarse **junto con el desvío del tráfico** (mismo paso de despliegue), nunca antes:
> con Python sirviendo usuarios y organizaciones, dejaría de funcionar. Por eso el
> runbook es: desplegar core + frontend → `api migrate adopt` (una vez) → `api migrate up`
> → cambiar Caddy → apagar las rutas de Python.
>
> Tras el desvío, `domicilia-api` queda solo con el agente de IA y pasa a llamarse
> **`domicilia-agent`** (la carpeta local ya se llama así; el repo de GitHub, aún no).

### Cómo se verificó

- **Pruebas de contrato** (`internal/app`) contra un Postgres real con las migraciones
  reales. Portan las de pytest de `domicilia-api` (aislamiento entre inquilinos,
  permisos, escalada de roles, invitaciones, postulaciones) y añaden roles a medida,
  asignación, protección del último superadmin (incluido el caso simultáneo),
  gestión de usuarios, auditoría y errores. **Las heredadas de Python pasan sin
  cambios de comportamiento con el modelo de roles nuevo**: solo se adaptaron sus
  fixtures.
- **Traspaso de datos** (`internal/platform/db`): la migración `00002` se aplica
  sobre una base en versión 1 con datos como los de producción (superadmin, domi,
  cliente, admin y empleado) y se verifica que cada uno queda con el rol
  correspondiente y que no se pierde nada; también su reversa.
- **Mutaciones**: las reglas de seguridad se comprobaron rompiéndolas a propósito
  (escalada de privilegios por permisos no delegables, roles no asignables o con más
  permisos, último superadmin, roles de sistema editables, auditoría, acceso total a
  organizaciones); cada una hace fallar al menos una prueba.
- **Comparación en vivo con FastAPI** (hecha antes del modelo de roles, cuando aún
  compartían esquema): las dos APIs sobre la **misma** base, con los mismos tokens,
  respondieron igual en 22 de 22 peticiones. Después de RBAC no se puede repetir: Python
  ya no entiende el esquema, que es precisamente lo que avisa el recuadro de arriba.

### Contrato HTTP: qué cambia para el frontend

Los campos que ya devolvía FastAPI siguen igual (`is_general_admin`, `is_delivery`,
`role` = `admin`/`employee`...); ahora se **derivan** de los roles y la respuesta trae
además campos nuevos (`roles`, `permissions`, y por membresía `role_id`, `role_name`,
`permissions`). `role` puede ser el código de un rol personalizado, no solo
`admin`/`employee`: **`mapApiUserToSession` del frontend** trata todo lo que no sea
`admin` como `callcenter`. Cambia además esto (hay que tenerlo en cuenta en el PR que
desvíe el tráfico):

1. **Prefijo `/v1` y sin barra final**: `/users/me` → `/v1/users/me`;
   `/users/` → `/v1/users`; `/organizations/` → `/v1/organizations`;
   `/driver-applications/` → `/v1/driver-applications`.
2. **Errores en `application/problem+json`**, no `application/json`. `detail` sigue
   siendo un texto. **`lib/api/client.ts` del frontend** decide si lee el JSON con
   `contentType.includes("application/json")`, y eso NO incluye
   `application/problem+json`: hay que aceptar `+json` o dejará de mostrar el
   `detail`. (Los demás `fetch` del frontend llaman a `res.json()` directo y no se
   ven afectados.)
3. **Validación**: un cuerpo inválido da `422` con `detail` en texto (FastAPI
   devolvía una lista de objetos); un JSON mal formado da `400`; un id mal
   formado en la ruta da `422`.
4. **Mensajes en español** (FastAPI los daba en inglés). El frontend solo los
   muestra, no compara textos.
5. **Sin `GET /` ni `/docs`**: la salud es `/healthz` y `/readyz`. El healthcheck
   de Caddy/Compose que apunte a `/` debe cambiarse.
6. **Correos en minúsculas** y textos recortados al guardarse (GoTrue ya guarda los
   correos en minúsculas). Entradas más largas que la columna dan `422` en vez de
   un `500`.
7. `POST /v1/users` exige que el token traiga `email` (GoTrue siempre lo incluye).

Comportamientos heredados a propósito, no arreglados aquí: `403` vs `404` al pedir
una organización ajena permite enumerar qué organizaciones existen (hay una prueba
que lo fija; ver `README_MULTITENANT.md`), y `invite`/`approve` devuelven una
`temporary_password` en la respuesta (solo aceptable mientras GoTrue no tenga
proveedor de correo).

### Quién es dueño del esquema

**Este repo**, con goose. Alembic (en `domicilia-api`) queda **congelado** en su
última revisión (`b9f4efb43ad7`): no se agregan más migraciones allí. Motivo:
catálogo y pedidos nacen en Go, y sus tablas no pueden depender de un repo de
Python que se va a apagar. Dos herramientas escribiendo el mismo esquema es la
forma de romperlo, por eso el traspaso es de una sola vez y no gradual.

`db/migrations/00001_baseline.sql` reproduce el esquema de Alembic. **Verificado**:
se aplicó Alembic (las 7 revisiones) en un Postgres 16 y la migración base en otro,
y `pg_dump --schema-only` de ambos es idéntico.

Para pasar una base a goose:

1. **Base nueva (local, pruebas)**: `api migrate up`. Necesita que exista
   `auth.users` (GoTrue debe haber arrancado); si no, falla diciendo qué falta.
2. **Base existente (devstack con datos, staging, producción)**: el esquema ya lo
   creó Alembic, así que la migración base se **adopta sin ejecutar su SQL**:

   ```bash
   api migrate adopt    # una sola vez
   api migrate up       # a partir de aquí, solo lo nuevo
   ```

   `adopt` se niega (y no toca nada) si Alembic no está exactamente en
   `b9f4efb43ad7`, si falta alguna tabla o si la base ya está en goose.
3. Cambiar el runbook de despliegue: `alembic upgrade head` → `api migrate up`.

Antes de adoptar en staging/producción conviene comparar el esquema real con el
de la migración base (`pg_dump --schema-only` de esa base contra
`db/migrations/00001_baseline.sql`): si alguien tocó el esquema a mano, `adopt`
no lo detecta.

## Pendiente antes de producción

- **Ejecutar el runbook en staging** (`domicilia-infra/deploy/README.md`), con respaldo
  previo, y solo entonces en producción. Incluye crear las credenciales federadas, secretos
  y entornos de GitHub para `domicilia-core` (mismos pasos que los otros repos).
- Rol de aplicación limitado en Postgres (hoy el core usa el usuario dueño de la base,
  superusuario de hecho; RLS **no** se aplica a él).
- **WhatsApp, lo que falta** (ver `docs/whatsapp.md` §5): plantillas (constructor, sincronización, galería), envío
  de archivos y descarga de los entrantes, consentimiento y segmentos, campañas masivas, libro de uso y billetera,
  Embedded Signup, tiempo real (SSE) y etiquetas/equipos/macros. Antes de abrirlo a producción: crear
  `core-secrets-key-<entorno>`, `whatsapp-app-secret-<entorno>` y `whatsapp-verify-token-<entorno>` en Key Vault (el
  despliegue arranca igual sin ellos) y suscribir el webhook en la app de Meta.
- Limitar la frecuencia de las rutas públicas (Caddy o middleware): `POST /v1/driver-applications`
  escribe en la base; `GET /v1/public/organizations/{slug}` y `POST /v1/invitations/preview`
  consultan la base sin credenciales.
- Correo real en **staging**: el core ya sabe enviar por SMTP (`CORE_SMTP_*`, `internal/platform/mail`)
  y `fetch-secrets.sh` le pasa las mismas credenciales de Azure que a GoTrue; falta desplegarlo y
  comprobar una invitación de verdad. Para **producción**: crear `smtp-user-prod` y `smtp-pass-prod`,
  subir la cuota de Azure (hoy 5/min y 10/h) o pasar a Resend, y un dominio propio para el remitente.
  El enlace de una invitación se sigue devolviendo a quien invita, por si el correo no llega.
- Los límites de miembros por plan (`internal/plans`) son valores iniciales a confirmar con negocio.
- Cobro (ePayco): `organization_subscriptions` deja el sitio; no existe el cobro.
- Puerta de servicio para el agente de IA (credenciales de servicio, no el JWT de un
  usuario): se diseña cuando exista la primera *tool*.
- Renombrar en **GitHub** `domicilia-api` a `domicilia-agent` (repo, imagen de GHCR, workflows y
  credenciales) cuando producción lleve estable unos días. La carpeta local ya se llama
  `domicilia-agent`; el remoto todavía apunta a `domicilia-api`.
- Frontend: `mapApiUserToSession` trata todo rol de organización que no sea `admin` como
  `callcenter`, y un usuario con solo un rol de plataforma a medida como `customer`. Con
  roles personalizados conviene decidir la sesión por permisos (`/v1/users/me` ya los trae).
