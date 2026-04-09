ALTER TABLE charging_sessions
    ADD COLUMN IF NOT EXISTS transaction_ref VARCHAR(100),
    ADD COLUMN IF NOT EXISTS remote_start_id BIGINT,
    ADD COLUMN IF NOT EXISTS failure_reason TEXT,
    ADD COLUMN IF NOT EXISTS stop_requested_at TIMESTAMP WITH TIME ZONE,
    ADD COLUMN IF NOT EXISTS billed_at TIMESTAMP WITH TIME ZONE;

CREATE UNIQUE INDEX IF NOT EXISTS charging_sessions_transaction_ref_idx
    ON charging_sessions (transaction_ref)
    WHERE transaction_ref IS NOT NULL;

CREATE TABLE IF NOT EXISTS charger_connectors (
    charger_id       VARCHAR(100) NOT NULL REFERENCES chargers(id) ON DELETE CASCADE,
    connector_id     INTEGER NOT NULL,
    evse_id          INTEGER,
    status           VARCHAR(32) NOT NULL DEFAULT 'Unknown',
    last_seen        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    metadata         JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (charger_id, connector_id)
);

CREATE INDEX IF NOT EXISTS charger_connectors_status_idx
    ON charger_connectors (status);

CREATE TABLE IF NOT EXISTS ocpp_webhook_events (
    event_id         VARCHAR(128) PRIMARY KEY,
    event_type       VARCHAR(64) NOT NULL,
    payload          JSONB NOT NULL,
    processed_at     TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
