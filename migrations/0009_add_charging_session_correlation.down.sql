DROP INDEX IF EXISTS charging_sessions_remote_start_id_idx;

ALTER TABLE charging_sessions
    DROP COLUMN IF EXISTS is_active,
    DROP COLUMN IF EXISTS charging_state,
    DROP COLUMN IF EXISTS ocpp_transaction_id,
    ALTER COLUMN remote_start_id TYPE BIGINT USING NULLIF(remote_start_id, '')::bigint;
