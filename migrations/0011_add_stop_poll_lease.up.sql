ALTER TABLE charging_sessions
    ADD COLUMN IF NOT EXISTS stop_poll_claimed_at TIMESTAMP WITH TIME ZONE;
