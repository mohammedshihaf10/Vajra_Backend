ALTER TABLE users
    ADD COLUMN auth_token TEXT,
    ADD COLUMN auth_token_expires_at TIMESTAMP WITH TIME ZONE;
