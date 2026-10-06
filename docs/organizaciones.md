# Organizaciones (inquilinos): ciclo de vida, planes e invitaciones

Diseño del módulo `internal/organizations` y `internal/plans` de domicilia-core.
Reemplaza al flujo de FastAPI (Fases 4 y 7 de V2) y prepara el terreno para el módulo
WhatsApp (bandejas, plantillas, campañas), que se construye en una segunda ronda.

## 1. Modelo

Una **organización** es un inquilino (un restaurante o negocio). Todo dato de negocio
futuro (productos, pedidos, conversaciones) lleva `organization_id`.

```
organizations ──1:1── organization_settings        perfil comercial y horarios
      │ ──1:N── organization_subscriptions         historial de planes (uno vigente)
      │ ──1:N── organization_feature_overrides     excepciones por función
      │ ──1:N── organization_invitations           invitaciones con token
      │ ──1:N── user_organizations ── roles        miembros y su rol
      └──1:N── audit_log                            quién cambió qué
```

### Estados

| Estado | Miembros | Superadmin | Efecto |
| :--- | :--- | :--- | :--- |
| `active` | operan normal | | |
| `suspended` | solo lectura de la organización (403 al operar) | ve y opera | impago o revisión |
| `archived` | la organización "no existe" (404) | la ve, filtrada por estado | baja lógica; el slug y el nombre siguen reservados |

Cada cambio es una **operación con sus estados de origen** (no basta validar el destino:
«suspender» una archivada la sacaría del archivo sin que nadie lo decida, y «restaurar»
una activa la suspendería):

| Operación | Desde | Hacia |
| :--- | :--- | :--- |
| suspender | `active` | `suspended` |
| reactivar | `suspended` | `active` |
| archivar | `active`, `suspended` | `archived` |
| restaurar | `archived` | `suspended` (reactivarla es otra decisión) |

Suspender y archivar exigen un motivo; todas quedan en la auditoría. Pedir una operación
desde un estado que no la permite es un 409 y no deja rastro. `is_active` se conserva en
el JSON como `status == active` para no romper el contrato con el frontend; la base ya no
tiene esa columna (`status` es la única fuente de verdad).

### Identidad del inquilino

- El **slug** es la clave pública (URL, subdominio futuro): minúsculas, dígitos y
  guiones, 3 a 63 caracteres, sin acentos. Se deriva del nombre si no se envía.
  **No cambia nunca**: cambiarlo rompe enlaces y webhooks. No puede ser una palabra
  reservada del frontend (`platform`, `api`, `login`...).
- El nombre es único (sin distinguir mayúsculas) y editable.

### Planes y funciones

- Los planes (`starter`, `pro`, `outreach`, `enterprise`), sus funciones y sus límites
  viven **en código** (`internal/plans`), igual que los permisos. Las claves de las
  funciones son las de `WA_FEATURES` del frontend.
- `organizations.plan_tier` es el plan vigente (desnormalizado a propósito: se lee en
  cada listado). `organization_subscriptions` guarda el historial; ambos cambian en
  la misma transacción y una prueba comprueba que coinciden.
- Una **excepción por función** (`organization_feature_overrides`) enciende o apaga una
  función para una organización concreta (una prueba comercial, un acuerdo especial).
- El servidor decide: `plans.Service.Require(org, función)` responde **402** si el
  plan no la incluye. El frontend solo oculta la interfaz.
- Límite de miembros por plan (cuenta miembros e invitaciones pendientes): 402.
  **Los números de cada plan son valores iniciales a confirmar con negocio.**

## 2. Reglas de negocio

1. Solo el operador (`platform.organizations.create`) crea organizaciones. Al crearla
   se crean, en una transacción, la organización, sus ajustes, su suscripción inicial
   y, si se envía `admin_email`, la invitación del primer administrador. Nunca queda
   una organización a medias.
2. **Una organización nunca se queda sin administrador**: no se puede quitar ni
   degradar al último miembro con `org.members.manage` (409). Se serializa con un
   bloqueo consultivo por organización para que dos peticiones simultáneas no lo
   eludan.
3. Nadie otorga más de lo que tiene (regla de `roles`): solo `platform.roles.manage`
   nombra administradores, también por invitación.
4. Con la organización suspendida, sus miembros no pueden administrarla (miembros,
   ajustes, invitaciones). El operador sí.
5. Todo cambio (creación, perfil, ajustes, estado, plan, excepciones, invitaciones,
   miembros) se audita **en la misma transacción** que el cambio.
6. Las respuestas a quien no tiene permiso no revelan datos de la organización.

## 3. Invitaciones

Reemplazan al alta con contraseña temporal (`members/invite`, que se conserva solo
mientras no haya correo, marcada como obsoleta).

1. Un administrador invita a un correo con un rol → se guarda `sha256(token)`; el
   token en claro solo existe en la respuesta y en el correo. Vigencia 7 días.
2. Reinvitar al mismo correo reemplaza el token anterior (reenvío).
3. La persona abre el enlace: `POST /v1/invitations/preview {token}` (público) le
   muestra organización y rol.
4. Se registra o inicia sesión en auth-domicilia y llama a
   `POST /v1/invitations/accept {token}` con su JWT. El correo del JWT debe coincidir
   con el de la invitación. Se crea el perfil (si no tiene) y la membresía, y la
   invitación queda aceptada; todo en una transacción.
5. El token es de un solo uso; caducado, revocado o aceptado responde 404 (mismo
   mensaje: no se distingue).

El enlace se devuelve a quien invita para que pueda compartirlo (por WhatsApp, p. ej.)
mientras el core no envíe correo; el envío es un puerto (`mail.Sender`) sin implementación real
todavía (GoTrue sí envía correo en staging, por otro canal).

## 4. API

Público: `GET /v1/public/organizations` (directorio: solo activas, paginado con `q`/`limit`/`offset`,
para la app de cliente eligiendo con quién pedir — sin horario ni estado, eso es el detalle),
`GET /v1/public/organizations/{slug}`, `POST /v1/invitations/preview`.
Autenticado: `POST /v1/invitations/accept`.
De negocio (permiso entre paréntesis; pertenecer basta si no se indica):

| Ruta | Permiso |
| :--- | :--- |
| `GET /v1/plans` | cualquiera |
| `POST /v1/organizations` | `platform.organizations.create` |
| `PATCH /v1/organizations/{id}` | `org.settings.manage` |
| `GET, PATCH /v1/organizations/{id}/settings` | `org.settings.read` / `manage` |
| `GET /v1/organizations/{id}/entitlements` | miembro |
| `GET /v1/organizations/{id}/subscription` | `org.billing.read` |
| `GET, POST /v1/organizations/{id}/invitations` | `org.members.manage` |
| `POST …/invitations/{inv}/resend`, `DELETE …/invitations/{inv}` | `org.members.manage` |
| `GET /v1/platform/organizations` (`q`, `status`, `plan_tier`, paginado) | `platform.organizations.access_all` |
| `POST /v1/platform/organizations/{id}/suspend \| reactivate \| archive \| restore` | `platform.organizations.manage` |
| `PUT /v1/platform/organizations/{id}/plan` | `platform.organizations.manage` |
| `PUT, DELETE /v1/platform/organizations/{id}/feature-overrides/{feature}` | `platform.organizations.manage` |

`PATCH /v1/platform/organizations/{id}` (`is_active`, `plan_tier`) se mantiene como
compatibilidad con el frontend actual y delega en lo anterior.

## 5. Fuera de alcance de esta ronda

- Módulo WhatsApp (bandejas, plantillas, contactos, conversaciones, campañas): usará
  `plans.Require` y `organization_id` desde el primer día.
- Cobro (ePayco): la tabla de suscripciones deja el sitio; el cobro no existe.
- Row Level Security de Postgres (tema B de la Fase 4).
- Rate limiting de las rutas públicas (`GET /v1/public/...`, `preview`, postulaciones).
- Envío real de correo (SMTP).
