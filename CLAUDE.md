# CLAUDE.md

Guía para trabajar en este repositorio.

## Qué es

`domicilia-core` es el core API de Domicilia, en **Go + Echo v5**. Es **todo el backend**
(APIs para el frontend, WhatsApp y el agente). `domicilia-api` (FastAPI) queda solo con
el agente de IA (LangChain/LangGraph) y pasará a llamarse `domicilia-agent`. La lógica de
negocio de Python ya está portada. **El devstack ya usa el core** (FastAPI fuera del arranque);
el servidor está preparado pero sin desplegar. **`00002_rbac` no puede coexistir con Python**
(elimina columnas que este lee): en el servidor se aplica junto con el desvío.
Ver el `README.md` para el estado.

## Comandos

```bash
make check     # vet + lint + test: lo mismo que corre el CI. Correrlo antes de subir.
make test      # pruebas (necesitan Docker para las de base de datos; sin -race: Windows no tiene gcc)
make race      # pruebas con detector de carreras (CI/Docker, no Windows)
make lint      # golangci-lint
make fmt       # formatea
make sqlc      # regenera internal/store (hazlo al tocar db/queries o db/migrations)
make migrate   # aplica migraciones pendientes
api seed       # (subcomando) datos de demo de desarrollo; se niega en staging/producción
make dev       # recarga en caliente con air
make run       # arranca sin recarga
```

No hay nada que instalar aparte de Go (y Docker para las pruebas): `sqlc`, `air` y
`golangci-lint` son directivas `tool` de `go.mod` y se ejecutan con `go tool
<nombre>`. La primera compilación de `golangci-lint` tarda unos 7 min y la de `sqlc`
unos 3; las siguientes, segundos. En Windows conviene fijar `GOCACHE` en un disco
con espacio.

En el devstack (`domicilia-devstack/`): `make up` lo levanta con todo el stack;
`make up-core`, `make logs-core` y `make down-core` lo manejan solo. Su `.env`
se genera solo desde `domicilia-agent/.env`.

## Estructura

```
cmd/api/                 main (solo cablea), migrate, create-superadmin, healthcheck
internal/app/            arma la aplicación (servicios+handlers+rutas); main y las pruebas lo usan
internal/access/         modelo de permisos (catálogo, Set): puro, sin base de datos ni HTTP
internal/identity/       JWT validado -> Principal con sus roles y permisos; RequireUser, Require(perm)
internal/audit/          auditoría (roles, miembros, estado, plan, invitaciones), en la misma transacción que el cambio
internal/<dominio>/      users · customers · organizations · plans · roles · drivers · saas ·
                         inboxes · contacts · conversations · whatsapp (webhook + envío) · tenant (puerta común):
                         handler · service · repository
internal/store/          código GENERADO por sqlc (no se edita a mano)
internal/meta/          cliente de la Graph API de Meta y análisis/firma de webhooks (sin base de datos)
internal/platform/       lo transversal: config, logging, db (pool, tx, migrate), httpserver (cursor, Field),
                         auth (JWT), apperr, validate, gotrue (admin API), mail (SMTP), secretbox (AES-GCM)
openapi/openapi.yaml     contrato de la API (fuente de verdad para el frontend y el agente)
internal/testutil/pgtest Postgres desechable para las pruebas
db/migrations/           migraciones de goose, embebidas en el binario
db/queries/              SQL de sqlc, un archivo por dominio
```

**Por dominio, no por capa.** `internal/organizations/` contiene su handler, servicio
y repositorio juntos; nunca un `internal/handlers/` con todos los handlers.
La dependencia va siempre en un sentido: `handler → service → repository`. El
servicio **no importa Echo**: devuelve `apperr` y el manejador central lo traduce a
HTTP. El repositorio traduce los errores de Postgres (UNIQUE, FK, sin filas) a
errores del dominio.

Dependencias entre dominios (sin ciclos): `roles` y `customers` son **hojas** (no
importan a ningún otro dominio); `plans` también es una hoja; `users` → {customers, roles};
`organizations` → {roles, plans, audit}; `saas` → {organizations, plans, users}. `internal/saas` (rutas `/v1/platform`) no tiene
repositorio: orquesta servicios. Se llama así para no chocar con `internal/platform`.

## Reglas de WhatsApp (bandeja)

- **Todo lo que se hace dentro de una organización pasa por `tenant.Gate.Open`** (permiso → organización existe → suspendida
  403 / archivada 404). Un servicio nuevo que no lo llame es un fallo de aislamiento.
- **Toda tabla hija lleva `organization_id` y su clave foránea es COMPUESTA** `(id, organization_id)` hacia el padre: la base
  rechaza una fila que apunte a datos de otra organización aunque el código falle (probado en `conversations_test.go`).
- **El token de Meta nunca sale en claro**: se guarda con `secretbox` (AES-GCM, atado al id de la cuenta), no aparece en
  respuestas, auditoría ni logs, y viaja a Meta solo en la cabecera `Authorization`.
- **Webhook**: cada evento se autoriza con la firma de SU número (secreto propio o el de la plataforma); una firma válida de un
  negocio nunca autoriza eventos de otro. Es idempotente (clave del cuerpo + `(inbox_id, wamid)` único) y tolera desorden
  (los estados solo avanzan).
- **Envío**: el mensaje nace `queued` y lo entrega `whatsapp.Dispatcher`: en orden por conversación, con arrendamiento,
  reintentos con espera para fallos transitorios, y **sin reintentar nunca un fallo ambiguo** (duplicaría el mensaje).
- **Contrato**: toda ruta nueva de estos módulos se documenta en `openapi/openapi.yaml`; una prueba exige que lo documentado
  y lo servido coincidan y valida las respuestas reales contra los esquemas.
- Listados que crecen: paginación por **cursor** (`httpserver.CursorParams`), no `offset`.

## Reglas

- **Echo v5, no v4.** Los handlers son `func(c *echo.Context) error` (puntero).
  Al dudar de la API, léela en `~/go/pkg/mod/github.com/labstack/echo/v5@*/`.
- **Errores:** los servicios devuelven `apperr.NotFound/Forbidden/Conflict/Invalid/...`
  con un mensaje en español para el cliente; el manejador central los convierte a
  RFC 9457 (`application/problem+json`). Cualquier otro error es un 500 y **nunca**
  expone el detalle (SQL, rutas): se registra en el log con el `request_id`. Una
  prueba lo verifica provocando un fallo real de Postgres.
- **Tres niveles de acceso bajo `/v1`** (ver `internal/app/app.go`): público (solo la
  postulación de domiciliarios), autenticado (JWT válido, aunque no haya perfil: solo
  `POST /users`) y de negocio (JWT + usuario activo en `public.users`). **El nivel por
  omisión es el de negocio**: una ruta nueva debe justificar bajar de él. Una prueba
  recorre todas las rutas sin credenciales y falla si alguna no es 401.
- **Identidad y permisos:** `identity.Current(c)` da el `Principal` en el handler; los
  servicios lo reciben como argumento (no leen Echo). El Principal trae, ya cargados, los
  roles de plataforma, sus permisos y el rol/permisos en cada organización, así que decidir
  es una consulta en memoria: `p.Can(perm)` (plataforma), `p.CanInOrg(org, perm)`,
  `p.IsMember(org)`. Los permisos de **plataforma** se exigen en la ruta con
  `identity.Require(access.X)`; los de **organización** los comprueba el servicio, porque
  dependen de la organización tocada. Nunca preguntes por un rol por nombre para decidir
  (`HasRole("superadmin")`): pregunta por el permiso.
- **Nadie otorga más de lo que tiene** (`roles.Service`): al crear/editar un rol o al
  asignarlo, el actor debe tener todos sus permisos; en organizaciones, además, solo
  permisos `Delegable` y roles `org_assignable`. Es lo que impide fabricar un "admin"
  falso. Si agregas un permiso de administración, **no lo marques delegable**.
- **Catálogo de permisos:** lo define `internal/access` (constantes + `Catalog`) y la tabla
  `permissions` lo espeja. Para agregar uno: constante + entrada en `Catalog` + una
  migración que lo inserte **y se lo otorgue a `superadmin`** (y a `admin` si es de
  organización). Tres pruebas (`rbac_invariants_test.go`) fallan si algo no cuadra.
- **La plataforma no puede quedarse sin un superadmin activo:** quitar el rol y desactivar
  la cuenta toman `LockSuperadmins` (advisory lock) y verifican después de tocar.
- **Auditoría:** todo cambio de rol escribe su `audit.Entry` **dentro de la misma
  transacción** (`Entry.Insert(ctx, q)` con las consultas de la tx). Lo que no cambia nada
  (asignar un rol que ya se tenía) no se audita.
- **Autorización por organización y RLS:** las reglas viven en los servicios (vía
  permisos). Esto **contradice** la intención de que el aislamiento lo garantice RLS en
  Postgres, que sigue pendiente (tema B, bloqueado por decisiones). Cuando RLS exista
  debe sumarse como defensa en profundidad; no quitar estas comprobaciones sin decidirlo.
- **Variables `CORE_*`:** el `.env` de cada entorno lo comparten varios servicios
  y ya trae `DATABASE_URL` (la de GoTrue, con otro usuario) y `PORT=9999`. No
  reutilices esos nombres. `CORE_AUTH_URL` es la dirección interna de GoTrue.
- **SQL:** con `sqlc` + `pgx`. Nada de SQL armado con `fmt.Sprintf` (gosec lo marca).
  Las fechas se escriben con `timezone('utc', now())` (las columnas son `timestamp`
  sin zona, y así todo queda en UTC). Las búsquedas por correo no distinguen
  mayúsculas: hay correos históricos guardados como se escribieron.
- **Nunca dejar una cuenta huérfana en GoTrue.** Crear una cuenta (invitar, aprobar)
  no es transaccional con la base: si guardar el perfil falla después, el servicio
  borra la cuenta recién creada. Hay pruebas deterministas de ese camino.
- **Sin secretos en el repo.** Todo entra por variables de entorno. `.env` está
  en `.gitignore`. Los valores falsos de prueba llevan `// gitleaks:allow`.
- **Pruebas:** tabla de casos con nombres en español que digan qué se garantiza, y de
  preferencia lo que debe **rechazarse**. Contra Postgres real (testcontainers), no
  mocks: las de contrato viven en `internal/app` y ejercitan la aplicación real
  (`app.New`); solo GoTrue está simulado (`fakeIDP`). Tras escribir una regla de
  seguridad, rómpela a propósito y comprueba que alguna prueba falla.

## Migraciones de base de datos

**Este repo es el dueño del esquema**, con [goose](https://github.com/pressly/goose).
Alembic (en `domicilia-api`) queda **congelado** en su última revisión
(`b9f4efb43ad7`) y no recibe más migraciones. La migración base
(`db/migrations/00001_baseline.sql`) reproduce ese esquema y se verificó contra él.

- Las migraciones viven en `db/migrations/` y van **embebidas en el binario**
  (`go:embed`); se aplican con `api migrate up`. La imagen de producción es
  distroless (sin shell), así que no hay un binario `goose` aparte.
- En una base que ya creó Alembic (devstack con datos, staging, producción) se
  adopta **una vez** con `api migrate adopt`, que no ejecuta el SQL de la base.
- `api migrate down` se niega en staging/producción (la base revertida borra todo).
- Una migración ya aplicada en staging/producción **nunca se edita**: se agrega otra.
- Cada migración es reversible (`-- +goose Down`) salvo que se justifique.
- Las tablas de Alembic (`users`, `organizations`, `user_organizations`, `customers`,
  `driver_applications`) no llevan `DEFAULT` en sus columnas y guardan `timestamp` sin
  zona: las fechas las pone la aplicación con `timezone('utc', now())`. Las tablas
  **nuevas** (`00002` en adelante) usan `timestamptz NOT NULL DEFAULT now()`.
- `00002_rbac` reemplaza `users.is_general_admin`, `users.is_delivery` y el enum
  `user_organizations.role` por `roles`, `permissions`, `role_permissions`,
  `user_platform_roles`, `user_organizations.role_id` y `role_audit_log`. Traspasa los datos
  existentes (probado en `migrate_rbac_test.go`). Los roles de sistema (`superadmin`,
  `delivery`, `customer`, `admin`, `employee`) son datos semilla: **los tests no deben
  vaciar esas tablas** (`pgtest.Reset` las conserva; nunca `TRUNCATE organizations CASCADE`,
  arrastraría `roles`).
- `00003_organizations_lifecycle` reemplaza `organizations.is_active` por `status`
  (`active | suspended | archived`), normaliza `plan_tier` a los cuatro planes, renombra
  `role_audit_log` a `audit_log` y crea `organization_settings`, `organization_subscriptions`,
  `organization_feature_overrides` y `organization_invitations` (probado en
  `migrate_lifecycle_test.go`). Diseño y reglas en `docs/organizaciones.md`. El JSON de la API
  sigue trayendo `is_active`, calculado desde `status`.
- **Todo permiso nuevo** exige una migración que lo inserte en `permissions` y lo otorgue al rol
  que corresponda (`admin` para los de organización, `superadmin` para los de plataforma): una
  prueba compara la tabla con `internal/access`.
- La tabla de control es `goose_db_version`. Convive con la `alembic_version`
  histórica, que no se toca.
- `auth.users` es de GoTrue, no nuestra: `db/sqlc/auth_stub.sql` la declara solo para
  que sqlc entienda la FK de `public.users`, y no se ejecuta contra ninguna base.

## Añadir un dominio

1. `internal/<dominio>/` con `handler.go`, `service.go`, `repository.go` y sus pruebas.
2. Si hay tablas nuevas: migración en `db/migrations/`. Consultas en
   `db/queries/<dominio>.sql` y `make sqlc`. Nombres de parámetros con `@nombre` o
   `sqlc.narg('x')` (sqlc no admite mezclar `$1` con nombrados).
3. Registrar las rutas desde `internal/app/app.go` sobre el grupo que corresponda
   (por omisión, `business`).
4. Pruebas de contrato en `internal/app/` (rechazos primero) y `make check`.

## Docker

- `Dockerfile.dev`: alpine + `air`, con el código por bind mount (devstack).
- `Dockerfile.prod`: multi-stage, binario estático en `distroless/static:nonroot`
  (~25 MB). Sin shell: el `HEALTHCHECK` es el propio binario (`/api healthcheck`).
