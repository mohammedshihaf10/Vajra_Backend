CREATE TABLE chargers (
    id          VARCHAR(100) PRIMARY KEY,
    status      VARCHAR(20) NOT NULL DEFAULT 'offline',
    last_seen   TIMESTAMP WITH TIME ZONE,
    model       VARCHAR(100),
    vendor      VARCHAR(100)
);

CREATE TABLE charging_sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    charger_id    VARCHAR(100) NOT NULL REFERENCES chargers(id) ON DELETE CASCADE,
    connector_id  INTEGER NOT NULL,
    user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    start_time    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    end_time      TIMESTAMP WITH TIME ZONE,
    energy_kwh    NUMERIC(12, 3) NOT NULL DEFAULT 0,
    cost          NUMERIC(12, 2) NOT NULL DEFAULT 0,
    status        VARCHAR(20) NOT NULL DEFAULT 'pending'
);

CREATE INDEX charging_sessions_charger_id_idx ON charging_sessions (charger_id);
CREATE INDEX charging_sessions_user_id_idx ON charging_sessions (user_id);
