# Domicilios: GPS, rutas, precio del domicilio y asignación — diseño (propuesta)

> **Estado (2026-10-08): propuesta, NADA implementado** salvo la tarifa plana de domicilio de
> `pagos.md` (`delivery_fee_cents`, configurable por el superadmin). Este documento ordena qué hay
> que construir y en qué orden. Las preguntas abiertas están en §9.

## 0. Qué existe hoy

| Pieza | Estado |
|---|---|
| Alta y aprobación de domiciliarios | Hecho (`internal/drivers`): el domi se postula, el superadmin lo aprueba |
| Precio del domicilio | **Tarifa plana** (general o por organización) en `pricing_settings` / `organization_pricing` |
| Comisión al domiciliario | `courier_fee_bps`, 0 % por defecto (⏳ abogado, ver `pagos.md` §16 Q5) |
| Estados del pedido | `out_for_delivery` / `delivered` existen, pero **nadie asigna** el pedido a un domi |
| App del domi (`/{tenant}/domi/*`) | Perfil real; pedidos, entrega activa e historial son "próximamente" |
| Ubicación / GPS / mapas | Nada |

## 1. Lo que hay que construir

1. **Direcciones geolocalizadas**: el restaurante y el cliente necesitan coordenadas (lat/lng), no
   solo texto. Geocodificación de la dirección + confirmación en un mapa.
2. **Precio del domicilio por ruta** (reemplaza la tarifa plana).
3. **Asignación** del pedido a un domiciliario (automática con aceptación, o manual del restaurante).
4. **Seguimiento en vivo**: la app del domi envía su ubicación; el cliente y el restaurante la ven.
5. **Liquidación al domiciliario** (lo que le corresponde menos la comisión) — ver `pagos.md` F9.

## 2. Algoritmo del precio del domicilio (propuesta)

```
distancia = ruta real por calles (no línea recta) entre restaurante y cliente, en km
tiempo    = duración estimada de la ruta, en minutos

domicilio = max( mínimo,
                 base + por_km × distancia + por_minuto × tiempo )
            × factor_demanda              (1,0 normal; >1 en lluvia / hora pico, opcional)
            redondeado al peso (mismo criterio que pagos: valor real, sin redondeo comercial)

domiciliario recibe = domicilio × (1 − comisión_domiciliario)
```

- Todos los parámetros (mínimo, base, por km, por minuto, factor) en la configuración del
  superadmin, con la misma herencia general → organización que las comisiones.
- **Radio máximo de entrega** por restaurante: fuera de él, no se vende.
- El precio se **cotiza en el carrito** (ya existe el lugar: `delivery_fee_cents` del carrito) y se
  **congela al confirmar** (ya existe: `orders.pricing_snapshot`).
- **Mientras no exista la ruta**: tarifa plana (hoy) o **zonas** (barrios/comunas de Ibagué con un
  precio cada una) — más simple y suficiente para empezar.

## 3. Motor de rutas: opciones

| Opción | Qué es | A favor | En contra |
|---|---|---|---|
| **Google Maps Platform** (Routes / Distance Matrix, Geocoding, Places) | API de pago por consulta | Mejor geocodificación de direcciones colombianas, tráfico en tiempo real, cero operación | Costo por consulta (verificar precios vigentes y cuota gratuita); dependencia externa |
| **Mapbox** | API de pago por consulta | Buen mapa y rutas | Mismo tipo de costo; geocodificación local más débil que Google |
| **OSRM / GraphHopper / Valhalla** (propio, con OpenStreetMap) | Servidor de rutas que corres tú | Sin costo por consulta, sin dependencia | Hay que operarlo y actualizar el mapa; consume memoria (medir con el mapa de Colombia o solo Tolima antes de decidir); OSM tiene menos detalle de direcciones en ciudades intermedias |

**Recomendación para empezar**: Google para **geocodificar** (convertir "Cra 5 # 10-20, Ibagué" en
coordenadas, lo más difícil) y para **la distancia de cada pedido** (una consulta por cotización).
Con el volumen de una ciudad, el costo es acotado y no hay que operar nada. Reevaluar un motor propio
(OSRM con el extracto de Tolima) cuando el número de consultas lo justifique.

## 4. Seguimiento en vivo (GPS)

```
App del domi ──(cada 5–10 s, solo con pedido activo)──▶ POST ubicación
                                                        │
                              guardar la última posición (y un historial corto)
                                                        │
Cliente / restaurante ◀──── SSE o WebSocket ◀── se publica la posición del domi de SU pedido
```

- La app del domi es una **PWA** (el frontend actual) con permiso de ubicación del navegador; una
  app nativa solo si la PWA no basta (ubicación en segundo plano es limitada en navegadores).
- **Privacidad**: la ubicación se registra solo durante una entrega activa, se informa en la
  política de privacidad y se borra el historial detallado tras N días.
- **Almacenamiento**: última posición en memoria/Redis o en una tabla pequeña; historial en Postgres
  (PostGIS si se necesitan consultas geográficas: "domis a menos de 2 km").

## 5. Asignación del pedido

Propuesta inicial, de menos a más automática:

1. **Manual del restaurante**: elige un domi disponible de una lista. Lo más simple para arrancar.
2. **Oferta**: el pedido se ofrece a los domis disponibles cercanos; el primero que acepta lo toma.
3. **Automática**: puntaje por distancia al restaurante, carga actual y calificación.

Estados nuevos sugeridos en el pedido o en una tabla `deliveries`: `searching_courier` →
`courier_assigned` → `picked_up` → `delivered`.

## 6. ¿Módulo del core o microservicio?

| Parte | Dónde | Por qué |
|---|---|---|
| Precio del domicilio, zonas, radio | **Módulo del core** (`internal/delivery`) | Es una regla de negocio que usa el carrito y el pago; misma transacción, mismo algoritmo de `internal/pricing` |
| Asignación y estados de la entrega | **Módulo del core** | Toca pedidos y domiciliarios; transacciones con el pedido |
| Geocodificación y rutas | **Adaptador** en el core (como `internal/epayco`) hacia Google/OSRM | Intercambiable detrás de una interfaz |
| **Seguimiento en vivo** (muchas escrituras de ubicación + conexiones abiertas) | **Candidato a microservicio** (Go) cuando haya carga | Es la única pieza con un perfil de carga distinto (lo anticipaba `ecommerce.md` §0). Empezar dentro del core y separarlo si las conexiones o las escrituras afectan al resto |

El "microservicio de GPS" no hace falta el primer día: con pocos domis activos, un endpoint de
ubicación y SSE en el core bastan. La interfaz se diseña para poder moverlo después.

## 7. Fases sugeridas

| Fase | Contenido |
|---|---|
| D1 | Direcciones con coordenadas (cliente y restaurante), geocodificación y confirmación en mapa |
| D2 | Precio por **zonas** de Ibagué (configurable) en lugar de tarifa plana |
| D3 | Asignación manual + estados de la entrega + app del domi (pedido activo, recogido, entregado) |
| D4 | Precio por **ruta** (distancia y tiempo reales) y radio de entrega |
| D5 | Seguimiento en vivo (ubicación del domi, SSE al cliente) |
| D6 | Oferta/asignación automática; liquidación al domiciliario (con `pagos.md` F9) |
| D7 | Separar el seguimiento en vivo a un servicio aparte, si la carga lo exige |

## 8. Relación con pagos

- El domicilio entra en la **base** del cobro (la pasarela cobra sobre el total).
- Hoy el domicilio queda en la **parte de la plataforma** al pagar (no hay domi asignado todavía) y
  debe liquidarse al domiciliario después (`pagos.md` §15 vacío 3, F9).
- Si algún día el domi se asigna **antes** de pagar, podría entrar como receptor del split de
  ePayco (requiere que cada domi sea receptor registrado en ePayco ⏳).

## 9. Preguntas abiertas

| # | Pregunta | Para |
|---|---|---|
| G1 | ¿Los domis son de la plataforma (pool común) o cada restaurante tiene los suyos? ¿Ambos? | Tú |
| G2 | ¿Cuánto se le cobra al domiciliario (3–5 %) y bajo qué figura legal (reforma laboral 2025, seguridad social de repartidores de plataformas)? | Abogado |
| G3 | ¿Zonas por barrio para empezar? ¿Cuáles y a qué precio? | Tú |
| G4 | Presupuesto mensual para APIs de mapas (Google) vs. operar un motor propio | Tú |
| G5 | ¿PWA basta para el domi o hace falta app nativa (ubicación en segundo plano)? | Prueba con domis reales |
| G6 | ¿Cuánto tiempo se guarda el historial de ubicaciones? | Abogado (privacidad) |
