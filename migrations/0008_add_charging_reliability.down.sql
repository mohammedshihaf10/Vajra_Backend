DROP TABLE IF EXISTS ocpp_webhook_events;
DROP INDEX IF EXISTS charger_connectors_status_idx;
DROP TABLE IF EXISTS charger_connectors;
DROP INDEX IF EXISTS charging_sessions_transaction_ref_idx;

ALTER TABLE charging_sessions
    DROP COLUMN IF EXISTS billed_at,
    DROP COLUMN IF EXISTS stop_requested_at,
    DROP COLUMN IF EXISTS failure_reason,
    DROP COLUMN IF EXISTS remote_start_id,
    DROP COLUMN IF EXISTS transaction_ref;
