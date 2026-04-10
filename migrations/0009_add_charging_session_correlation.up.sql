ALTER TABLE charging_sessions
    ALTER COLUMN remote_start_id TYPE VARCHAR(128) USING remote_start_id::text,
    ADD COLUMN IF NOT EXISTS ocpp_transaction_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS charging_state VARCHAR(50),
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS charging_sessions_remote_start_id_idx
    ON charging_sessions (remote_start_id);
