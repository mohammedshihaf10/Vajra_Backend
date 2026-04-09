# Charging Integration

## Architecture

```text
Mobile App
  -> Go API (`/chargers/verify`, `/charging/start`, `/charging/stop`)
      -> Charging Service
          -> CitrineOS HTTP Client
              -> `/ocpp/2.0.1/evdriver/requestStartTransaction`
              -> `/ocpp/2.0.1/evdriver/requestStopTransaction`
              -> `/ocpp/2.0.1/evdriver/unlockConnector`
              -> `/data/transactions/transaction`

CitrineOS / charger events
  -> `/webhooks/ocpp`
      -> signature validation
      -> webhook dedupe table
      -> transactional state updates
      -> websocket fanout

Postgres
  -> `charging_sessions`
  -> `charger_connectors`
  -> `ocpp_webhook_events`
  -> `wallet_transactions`
```

## Start Flow

```text
Client -> POST /charging/start
API -> verify wallet + cached connector availability
API -> create session(pending) -> transition(starting)
API -> CitrineOS requestStartTransaction
API -> return 202 starting
CitrineOS webhook remote_start_result -> accepted/rejected
Charger webhook start_transaction -> attach transaction_id -> charging
Charger webhook meter_values -> update kWh + cost -> websocket push
```

## Stop Flow

```text
Client -> POST /charging/stop
API -> transition session to stopping
API -> if transaction_id known, call requestStopTransaction
API -> if transaction_id missing, wait for start_transaction and then retry stop
API -> return 202 stopping
CitrineOS webhook remote_stop_result -> if rejected, revert to charging
Charger webhook stop_transaction -> finalize session -> wallet debit -> websocket push
```

## Meter Update Flow

```text
Charger -> webhook meter_values
Webhook processor -> dedupe event_id
Webhook processor -> transactional update of session energy/cost
Webhook processor -> publish websocket payload
GET /charging/session/:id stays consistent with websocket state
```

## Schema Changes

- `charging_sessions.transaction_ref`
  Stores CitrineOS transaction id string without depending on legacy integer ids.
- `charging_sessions.remote_start_id`
  Stores the OCPP 2.0.1 `remoteStartId` used for retries.
- `charging_sessions.failure_reason`, `stop_requested_at`, `billed_at`
  Preserve operational state and billing safety.
- `charger_connectors`
  Internal availability cache updated from `status_notification`.
- `ocpp_webhook_events`
  Idempotency table for exactly-once webhook processing.

## Retry And Failure Strategy

- Start/stop dispatch failures are retried with exponential backoff in-memory.
- After max attempts, the command lands in the retry queue dead-letter buffer.
- Sessions stuck in `starting` beyond `CHARGING_START_TIMEOUT` are marked `failed`.
- Duplicate webhook deliveries are ignored by `ocpp_webhook_events`.
- Wallet debit uses transaction-level idempotency keyed by session id to prevent double billing.

## Production Checklist

- Run migration `0008_add_charging_reliability`.
- Set `CITRINE_BASE_URL`, `CITRINE_TENANT_ID`, `CITRINE_CALLBACK_URL`, `CITRINE_ID_TOKEN_TYPE`.
- Set `OCPP_WEBHOOK_SECRET` and validate signed deliveries.
- Move retry/dead-letter storage to Redis if process-local retries are not sufficient.
- Add a Prometheus exporter if you need external metric scraping beyond the JSON `/metrics/charging` endpoint.
- Ensure CitrineOS webhook payloads include stable `event_id` values.
