# Transaction Execution API

API REST en Go que ejecuta transacciones financieras (`CREDIT` y `DEBIT`) contra un proveedor externo, guarda el resultado en PostgreSQL y permite consultarlo.

**En resumen**

- `POST /transactions` valida, aplica las reglas de negocio, llama al proveedor, persiste y responde con el estado final.
- `GET /transactions` lista con filtros (`accountId`, `status`, `type`) y paginación.
- Idea que guía el diseño: nunca mover dinero sin registro y nunca ejecutar dos veces por un reintento.
- Arquitectura hexagonal, dinero sin float y cliente del proveedor con timeout, reintentos acotados y circuit breaker.
- Se levanta con un comando de Docker e incluye un proveedor simulado para probar cada escenario.

## 1. Inicio rápido

Requisito: Docker con Compose v2.

```bash
git clone https://github.com/vit3ro/transaction-execution-api.git
cd transaction-execution-api
docker compose up -d --build --wait
```

| Servicio | Puerto | Qué es |
| --- | --- | --- |
| `api` | 8080 | La API. Aplica las migraciones al arrancar. |
| `mock-provider` | 8081 | Proveedor externo simulado. |
| `postgres` | 5432 | PostgreSQL 17. |

Primera prueba:

```bash
curl -s -X POST http://localhost:8080/transactions -H 'Content-Type: application/json' \
  -d '{"accountId":"acc-123456","type":"CREDIT","amount":1500.00,"currency":"MXN","description":"Transferencia recibida"}'
```

Para apagar: `docker compose down --volumes --remove-orphans`.

<details>
<summary>Sin Docker</summary>

Requisitos: Go 1.27 y un PostgreSQL accesible (probado con la versión 17) con una base vacía.

```bash
# Terminal 1: proveedor simulado en el puerto 8081
go run ./mock-provider

# Terminal 2: API en el puerto 8080
export DATABASE_URL="postgres://USUARIO:CLAVE@localhost:5432/transactions?sslmode=disable"
export PROVIDER_BASE_URL="http://localhost:8081"
export RUN_MIGRATIONS=true
go run ./cmd/api
```

</details>

## 2. Cómo probarla

| Forma | Cómo |
| --- | --- |
| Postman | Importar [docs/postman_collection.json](docs/postman_collection.json) y usar **Run collection**: 40 peticiones con pruebas automáticas, una carpeta por requisito. |
| curl | Los ejemplos de esta sección. |
| Contrato | [docs/openapi.yaml](docs/openapi.yaml), con un ejemplo por cada respuesta. |

```bash
# Regla de negocio: DEBIT mayor a 10,000.00 -> 422
curl -s -X POST http://localhost:8080/transactions -H 'Content-Type: application/json' \
  -d '{"accountId":"acc-123456","type":"DEBIT","amount":10000.01,"currency":"MXN"}'

# Rechazo del proveedor -> 201 con status REJECTED
curl -s -X POST http://localhost:8080/transactions -H 'Content-Type: application/json' \
  -d '{"accountId":"acc-insufficient","type":"DEBIT","amount":500.00,"currency":"MXN"}'

# Consulta con filtros y paginación
curl -s 'http://localhost:8080/transactions?accountId=acc-123456&status=EXECUTED&page=1&limit=5'
```

### Cuentas especiales del mock

El proveedor simulado decide su respuesta según el `accountId`. Cualquier otro valor es una cuenta normal con saldo inicial de 10,000.00 en memoria. Más detalle en [docs/mock-provider.md](docs/mock-provider.md).

| `accountId` | Respuesta del mock | Resultado en la API |
| --- | --- | --- |
| cualquier otro | 200 `APPROVED` con el saldo actualizado | 201 `EXECUTED` |
| `acc-insufficient` | 422 `INSUFFICIENT_FUNDS` | 201 `REJECTED` |
| `acc-blocked` | 403 `ACCOUNT_BLOCKED` | 201 `REJECTED` |
| `acc-error` | 500 con cuerpo del contrato | 201 `REJECTED` |
| `acc-down` | 503 sin cuerpo válido | 502 `FAILED` |
| `acc-garbage` | 200 con JSON truncado | 502 `FAILED` |
| `acc-slow` | 200 después de 5 segundos | 504 `FAILED` |

## 3. API

### `POST /transactions`

Reglas de negocio, validadas antes de llamar al proveedor: monto **mayor a 1.00**, `DEBIT` de **hasta 10,000.00** (`CREDIT` sin límite) y solo **MXN**.

| Resultado | HTTP | Cuerpo |
| --- | --- | --- |
| Aprobada por el proveedor | 201 | Transacción `EXECUTED` con `providerTransactionId` y `balanceAfter` |
| Rechazada por el proveedor | 201 | Transacción `REJECTED` con `rejectionCode` y `rejectionMessage` |
| Repetición con la misma `Idempotency-Key` | 200 | Transacción original y header `Idempotent-Replayed: true` |
| Petición inválida (JSON, campos, tipos, formatos) | 400 | Error `INVALID_JSON` o `VALIDATION_ERROR`, con detalle por campo |
| Petición original con esa llave aún en proceso | 409 | Error `IDEMPOTENT_REQUEST_IN_PROGRESS` |
| Cuerpo mayor a 64 KB | 413 | Error `REQUEST_TOO_LARGE` |
| `Content-Type` distinto de `application/json` | 415 | Error `UNSUPPORTED_MEDIA_TYPE` |
| Regla de negocio incumplida | 422 | Error `AMOUNT_BELOW_MINIMUM`, `DEBIT_AMOUNT_EXCEEDS_LIMIT` o `UNSUPPORTED_CURRENCY` |
| Misma llave con otro cuerpo | 422 | Error `IDEMPOTENCY_KEY_REUSED` |
| Proveedor respondió fuera del contrato | 502 | Transacción `FAILED` |
| Proveedor inalcanzable o circuit breaker abierto | 503 | Transacción `FAILED` |
| Timeout del proveedor | 504 | Transacción `FAILED` |

Todos los errores usan `application/problem+json` (RFC 7807) con `code`, `detail`, `errors` por campo y `requestId`, incluidas las rutas inexistentes (404 `NOT_FOUND`) y los métodos no soportados (405 `METHOD_NOT_ALLOWED`, con el header `Allow`). Toda respuesta incluye el header `X-Request-ID`.

### `GET /transactions`

Filtros opcionales `accountId`, `status` y `type`. `page` va de 1 a 100,000 (por defecto 1) y `limit` de 1 a 100 (por defecto 20). Orden: más recientes primero. Respuesta: `{ "data": [...], "page": 1, "limit": 20, "total": 2 }`.

### Salud

`GET /healthz` (el proceso está vivo) y `GET /readyz` (verifica la conexión a la base de datos).

## 4. Cómo funciona

```mermaid
flowchart LR
    Cliente([Cliente]) -->|HTTP| API[Adaptador HTTP<br/>net/http]
    API --> UC[Casos de uso<br/>ExecuteTransaction<br/>ListTransactions]
    UC --> Dominio[Dominio<br/>Transaction, Money,<br/>reglas de negocio]
    UC -->|puerto ProviderClient| PC[Cliente del proveedor<br/>timeout, reintentos,<br/>circuit breaker]
    UC -->|puerto TransactionRepository| Repo[Repositorio<br/>pgx]
    PC -->|"POST /provider/v1/execute"| Proveedor[(Proveedor externo)]
    Repo --> DB[(PostgreSQL)]
```

Flujo de `POST /transactions`:

1. Se valida la forma de la petición (400 si falla).
2. El dominio aplica las reglas de negocio (422 si falla; no se guarda ni se ejecuta nada).
3. Se guarda la transacción en `PENDING`.
4. Se llama al proveedor.
5. Se actualiza a su estado final y se devuelve.

| Estado | Significado | Origen |
| --- | --- | --- |
| `PENDING` | Registrada, sin respuesta del proveedor. | Estado inicial. |
| `EXECUTED` | El proveedor la aprobó. | 200 `APPROVED`. |
| `REJECTED` | El proveedor la rechazó. | Cualquier 4XX o 5XX con cuerpo válido del contrato. |
| `FAILED` | Fallo técnico: no se sabe si se ejecutó. | Error de red, timeout o respuesta fuera del contrato. |

Solo se pasa de `PENDING` a un estado final; los estados finales no cambian.

## 5. Decisiones de diseño

| Decisión | Por qué | Costo o trade-off |
| --- | --- | --- |
| **Hexagonal, sin frameworks** | El dominio y los casos de uso no conocen HTTP ni PostgreSQL y se prueban con fakes. El lint (`depguard`) falla si una capa importa lo que no debe. | Más paquetes que una estructura plana. |
| **PostgreSQL** | ACID y escritura durable; `NUMERIC` exacto; restricciones `CHECK` que rechazan datos inválidos aunque falle la aplicación; índices compuestos; camino conocido para escalar (réplicas, particionado). | Requiere operar una base de datos. |
| **Guardar `PENDING` antes del proveedor** | Si el servicio cae a mitad del flujo, queda evidencia de que se intentó mover dinero. Si falla ese guardado, no se llama al proveedor. | Una escritura extra. Es un desvío consciente del orden del enunciado, que lista persistir después de llamar. |
| **Dinero sin float** | `Money` es un entero de centavos; el monto se lee como texto JSON y se rechaza con más de dos decimales, sin redondear. `NUMERIC(18,2)` en la base. | Asume monedas de dos decimales. |
| **Rechazo distinto de fallo** | Un rechazo es una respuesta de negocio válida (`REJECTED`, 201), aunque llegue como 500. Un fallo técnico deja el resultado desconocido (`FAILED`). | El cliente debe leer `status`, no solo el código HTTP. |
| **Reintentar solo errores de conexión** | El contrato del proveedor no tiene llave de idempotencia: repetir una petición que pudo llegar puede mover el dinero dos veces. | Más transacciones en `FAILED` que con reintentos agresivos. |
| **Circuit breaker y plazos** | Se abre tras 5 fallos técnicos consecutivos y responde de inmediato durante 30 s. Plazo total de 3 s por llamada al proveedor y de 10 s por petición. Los rechazos de negocio no cuentan como fallo. | Su estado es por instancia. |
| **`Idempotency-Key` opcional** | El cliente puede reintentar sin duplicar: un índice único garantiza una sola transacción por llave, también entre peticiones simultáneas. | Toca todas las capas y no cubre la llamada al proveedor. |
| **UUID v7** | Ordenado por tiempo: los inserts caen al final del índice de la llave primaria. | Ninguno relevante. |
| **`LIMIT/OFFSET`** | Es lo que pide el enunciado y permite saltar de página. El total se omite cuando la página no viene llena. | Costoso en páginas profundas y al contar sin filtros; por eso `page` tiene tope. |

Qué se reintenta ante un problema con el proveedor:

| Situación | ¿Se reintenta? | Por qué |
| --- | --- | --- |
| La conexión nunca se estableció | Sí, hasta 2 veces, con backoff y jitter | No salió ni un byte. |
| Timeout de lectura | No | La petición salió; no se sabe si se ejecutó. |
| Conexión cortada a medias | No | Pudo cortarse después de procesar. |
| Respuesta 5XX | No | El proveedor la recibió y pudo ejecutarla. |

Índices, todos terminados en `(created_at DESC, id DESC)` para coincidir con el orden de la consulta:

| Índice | Para qué |
| --- | --- |
| `(account_id, created_at DESC, id DESC)` | Historial de una cuenta, solo o con estado o tipo. |
| `(status, created_at DESC, id DESC)` | Listar por estado y localizar `PENDING` antiguos. |
| `(created_at DESC, id DESC)` | Listado sin filtros y filtro solo por tipo. |
| `(idempotency_key)` único y parcial | Una sola transacción por llave. |

No hay índice propio para `type`: con dos valores casi no descarta filas, y cada índice encarece los inserts.

## 6. Pruebas

| Tipo | Qué prueba | Comando |
| --- | --- | --- |
| Unitarias | Dominio, casos de uso con fakes, handlers HTTP, cliente del proveedor, configuración | `go test ./...` |
| Integración | Repositorio y migraciones contra PostgreSQL real | Ver abajo |
| e2e | Escenarios completos contra la API, el mock y la base | Ver abajo |

```bash
docker compose up -d --build --wait

# e2e
API_BASE_URL=http://localhost:8080 go test -tags=e2e -count=1 -v ./test/e2e/...

# Integración: borra y recrea la tabla transactions; usar solo una base desechable
TEST_DATABASE_URL="postgres://transactions:transactions@localhost:5432/transactions?sslmode=disable" \
  go test -tags=integration -count=1 ./internal/adapters/postgres/...

# Cobertura
go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out
```

Cobertura medida con `-race`: dominio 97.6 %, casos de uso 97.5 %, adaptador HTTP 98.5 %, cliente del proveedor 90.9 % y repositorio 84.1 % con los tests de integración.

Sin Go instalado, todo corre en contenedores:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test -race -count=1 -cover ./...
docker run --rm -v "$PWD:/src" -w /src golangci/golangci-lint:latest golangci-lint run --build-tags integration,e2e ./...
```

## 7. Configuración

Variables de entorno, validadas al arrancar: si algo es inválido, el proceso termina y lista todos los problemas. Solo `DATABASE_URL` y `PROVIDER_BASE_URL` son obligatorias. La lista completa está en [.env.example](.env.example).

| Variable | Por defecto | Para qué sirve |
| --- | --- | --- |
| `HTTP_PORT` | `8080` | Puerto de la API. |
| `HTTP_REQUEST_TIMEOUT` | `10s` | Tiempo máximo por petición. |
| `RUN_MIGRATIONS` | `false` | Aplica las migraciones al arrancar. |
| `DB_MAX_CONNS` | `20` | Tamaño máximo del pool de conexiones. |
| `PROVIDER_REQUEST_TIMEOUT` | `3s` | Plazo total de una llamada al proveedor. |
| `PROVIDER_MAX_RETRIES` | `2` | Reintentos ante errores de conexión. |
| `PROVIDER_BREAKER_FAILURE_THRESHOLD` | `5` | Fallos consecutivos que abren el circuit breaker. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` o `error`. |

## 8. Estructura del proyecto

```text
cmd/api/                 Punto de entrada: configuración, composición y apagado ordenado
internal/
  domain/                Transaction, Money, reglas de negocio, errores tipados
  application/           Casos de uso: ExecuteTransaction y ListTransactions
  ports/                 Interfaces: repositorio, proveedor, reloj y generador de ids
  adapters/
    http/                Handlers, DTOs, errores RFC 7807, middlewares, health checks
    postgres/            Repositorio con pgx, pool y migraciones
    providerclient/      Cliente del proveedor: timeout, reintentos, circuit breaker
    system/              Reloj y generador de UUID v7
  config/                Lectura y validación de variables de entorno
  logging/               Logger JSON (slog) con request id por contexto
migrations/              SQL versionado, embebido en el binario
mock-provider/           Proveedor externo simulado (solo librería estándar)
test/e2e/                Pruebas de punta a punta
docs/                    OpenAPI, guía del mock y colección de Postman
```

Dependencias directas: `pgx`, `gobreaker`, `golang-migrate` y `uuid`. Operación: logs JSON con `requestId` en cada línea (sin cuerpos, montos ni cuentas), apagado ordenado ante SIGINT o SIGTERM e imagen `distroless` sin shell y con usuario sin privilegios.

## 9. Limitaciones y siguientes pasos

Limitaciones conocidas:

- **Sin conciliación.** Una transacción puede quedar en `PENDING` si el servicio cae a mitad del flujo, y una `FAILED` puede haberse ejecutado en el proveedor. Ninguno de los dos casos se resuelve automáticamente.
- **Llave atascada.** Si una transacción con `Idempotency-Key` queda en `PENDING`, las repeticiones reciben 409 indefinidamente.
- **Sin autenticación ni rate limiting.** La `Idempotency-Key` es global, no por cliente, y no expira.
- **Sin métricas ni trazas.** Solo hay logs estructurados.
- **Una sola moneda y dos decimales.**
- **Migraciones al arrancar** pensadas para desarrollo; en producción conviene un paso de despliegue aparte.

Siguientes pasos, en orden de valor:

1. **Conciliación con el proveedor** para resolver `PENDING` y `FAILED`.
2. **Idempotencia hacia el proveedor**, que permitiría reintentar también tras timeouts y 5XX.
3. **Paginación por cursor** y total opcional.
4. **Particionado por fecha** y réplicas de lectura cuando la tabla llegue a cientos de millones de filas.
5. **Eventos de transacción** con outbox y Kafka.
6. **Métricas y trazas** (Prometheus y OpenTelemetry), autenticación y rate limiting.

## 10. Uso de IA

Usé Claude Code en VS Code como asistente de implementación, bajo mi especificación y mi revisión.

- **Qué definí yo.** Antes de generar código fijé las decisiones de arquitectura y de dominio: hexagonal, dinero sin float, `PENDING` antes del proveedor, reintentos solo donde es seguro y la regla `REJECTED` frente a `FAILED`. También las reglas de negocio, la política de resiliencia y el modelo de datos.
- **Cómo trabajé.** En pasos pequeños, un prompt por paso con requisitos, restricciones de diseño y criterio de verificación. Acepté o corregí cada propuesta del asistente.
- **Qué delegué.** La implementación de cada capa a partir de esa especificación y los test. También le pedí alternativas con pros y contras en los puntos abiertos: índices, cálculo del total, idempotencia y código HTTP ante un rechazo.
- **Cómo se validó.** Tests unitarios, de integración contra PostgreSQL real y e2e contra el entorno en Docker; detector de carreras (`-race`); y `golangci-lint` con una regla que hace cumplir la arquitectura.
