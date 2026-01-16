ALTER TABLE users
    DROP COLUMN IF EXISTS auth_token,
    DROP COLUMN IF EXISTS auth_token_expires_at;
