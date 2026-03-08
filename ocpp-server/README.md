# OCPP Server

Standalone OCPP gateway service for charge points.

## What it does

- Accepts OCPP 1.6 websocket connections from chargers:
  - `GET /ocpp?id=<charge_point_id>`
  - `GET /ocpp/{charge_point_id}`
- Exposes backend-facing REST APIs:
  - `GET /v1/chargers/{id}/connectivity`
  - `GET /v1/chargers/{id}/connectors/{connector_id}/status`
  - `POST /v1/commands/remote-start`
  - `POST /v1/commands/remote-stop`
- Pushes async charging events to backend webhook:
  - `remote_start_result`
  - `remote_stop_result`
  - `start_transaction`
  - `meter_values`
  - `stop_transaction`

## Environment

- `OCPP_LISTEN_ADDR` default `:8090`
- `OCPP_SERVICE_API_KEY` optional API key required in `X-API-Key` for `/v1/*`
- `BACKEND_OCPP_WEBHOOK_URL` backend endpoint, e.g. `http://localhost:8080/webhooks/ocpp`
- `OCPP_WEBHOOK_SECRET` optional secret for `X-OCPP-Signature` (`sha256=<hex>`)
- `OCPP_HTTP_TIMEOUT_SEC` default `10`

## Run

```bash
cd ocpp-server
go run ./cmd/ocpp-server
```

## Remote command payloads

`POST /v1/commands/remote-start`

```json
{
  "session_id": "...",
  "charger_id": "CP001",
  "connector_id": 1,
  "id_tag": "user-id"
}
```

`POST /v1/commands/remote-stop`

```json
{
  "session_id": "...",
  "charger_id": "CP001",
  "transaction_id": 123
}
```
