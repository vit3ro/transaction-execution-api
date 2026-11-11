# Mock del proveedor externo

El servicio no implementa al proveedor: lo consume. Para poder probar todos los caminos (aprobación, rechazo, caída, timeout) sin depender de un tercero, el repositorio incluye un proveedor simulado en [`mock-provider/`](../mock-provider/).

Es un servidor HTTP en Go que usa solo la librería estándar y no importa nada de `internal/`, de modo que se comporta como un sistema externo real: lo único que comparte con la API es el contrato.

## Contrato

`POST /provider/v1/execute`

Petición:

```json
{ "accountId": "acc-123456", "type": "CREDIT", "amount": 1500.00, "currency": "MXN" }
```

Éxito (200):

```json
{ "transactionId": "txn-000001", "status": "APPROVED", "balance": 11500.00, "executedAt": "2025-03-15T10:30:00Z" }
```

Fallo (4XX o 5XX):

```json
{ "status": "REJECTED", "code": "INSUFFICIENT_FUNDS", "message": "Account balance is not enough" }
```

## Cómo levantarlo

Con Docker:

```bash
docker compose up -d --build mock-provider
```

Sin Docker:

```bash
go run ./mock-provider
```

Queda escuchando en `http://localhost:8081`.

## Configuración

Se configura con variables de entorno, sin recompilar.

| Variable | Valor por defecto | Para qué sirve |
| --- | --- | --- |
| `MOCK_PORT` | `8081` | Puerto en el que escucha. |
| `MOCK_INITIAL_BALANCE` | `10000.00` | Saldo con el que arranca cada cuenta la primera vez que se usa. |
| `MOCK_SLOW_DELAY` | `5s` | Retraso de la cuenta `acc-slow`. Acepta duraciones de Go (`500ms`, `2s`). |

## Saldos

El mock guarda un saldo en memoria por cuenta. Cada cuenta arranca con `MOCK_INITIAL_BALANCE`; un `CREDIT` suma y un `DEBIT` resta. Si un `DEBIT` supera el saldo disponible, responde 422 con `INSUFFICIENT_FUNDS` y no modifica el saldo.

Los saldos se pierden al reiniciar el mock.

## Cuentas especiales

El comportamiento depende del `accountId`. Cualquier otro valor se trata como una cuenta normal.

| `accountId` | Respuesta del mock | Estado final de la transacción en la API |
| --- | --- | --- |
| cualquier otro | 200 `APPROVED` con el saldo actualizado | `EXECUTED` |
| `acc-insufficient` | 422 con `INSUFFICIENT_FUNDS` | `REJECTED` |
| `acc-blocked` | 403 con `ACCOUNT_BLOCKED` | `REJECTED` |
| `acc-error` | 500 con cuerpo del contrato (`INTERNAL_ERROR`) | `REJECTED` |
| `acc-down` | 503 con cuerpo de texto plano | `FAILED` |
| `acc-garbage` | 200 con JSON truncado | `FAILED` |
| `acc-slow` | 200 `APPROVED` después de `MOCK_SLOW_DELAY` | `FAILED` si el retraso supera el timeout de la API |

Dos casos merecen atención:

- `acc-error` devuelve un 500, pero con un cuerpo válido del contrato. La API lo trata como rechazo de negocio (`REJECTED`), no como fallo técnico.
- `acc-slow` sí ejecuta el movimiento en el mock aunque la API ya se haya rendido por timeout. Reproduce el caso real en el que no se sabe si el proveedor ejecutó o no, y por eso la API nunca reintenta después de un timeout.

Además, una petición mal formada (JSON inválido, campos faltantes, tipo desconocido, monto en cero o con más de dos decimales) recibe 400 con `INVALID_REQUEST`.

## Pruebas con curl

Cuenta normal, crédito:

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-123456","type":"CREDIT","amount":1500.00,"currency":"MXN"}'
```

Cuenta normal, débito (el saldo baja respecto a la llamada anterior):

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-123456","type":"DEBIT","amount":250.75,"currency":"MXN"}'
```

Saldo insuficiente (422):

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-insufficient","type":"DEBIT","amount":100.00,"currency":"MXN"}'
```

Cuenta bloqueada (403):

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-blocked","type":"CREDIT","amount":100.00,"currency":"MXN"}'
```

Error interno con cuerpo del contrato (500):

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-error","type":"CREDIT","amount":100.00,"currency":"MXN"}'
```

Proveedor caído, sin cuerpo válido (503):

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-down","type":"CREDIT","amount":100.00,"currency":"MXN"}'
```

Respuesta 200 con JSON inválido:

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-garbage","type":"CREDIT","amount":100.00,"currency":"MXN"}'
```

Respuesta lenta (tarda `MOCK_SLOW_DELAY`):

```bash
curl -i -X POST http://localhost:8081/provider/v1/execute \
  -H "Content-Type: application/json" \
  -d '{"accountId":"acc-slow","type":"CREDIT","amount":100.00,"currency":"MXN"}'
```

En PowerShell, usa `curl.exe` y comillas dobles escapadas, o ejecuta las peticiones desde [`requests.http`](requests.http).

## Pruebas contra la API

El archivo [`requests.http`](requests.http) contiene los mismos escenarios, pero pasando por la API (`POST /transactions`), además de las reglas de negocio, el listado y la idempotencia. Se puede ejecutar con la extensión REST Client de VS Code o con el cliente HTTP de los IDE de JetBrains.

## Logs

Cada petición genera una línea JSON con la cuenta, el tipo, el monto, el escenario aplicado, el código de respuesta y la duración:

```json
{"level":"INFO","msg":"provider request","accountId":"acc-down","type":"DEBIT","amount":"250.75","scenario":"unavailable","status":503,"durationMs":0}
```

Con Docker se consultan con `docker compose logs -f mock-provider`.
