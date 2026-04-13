ALTER TABLE users
    ADD COLUMN IF NOT EXISTS id_tag VARCHAR(20);

CREATE UNIQUE INDEX IF NOT EXISTS users_id_tag_idx
    ON users (id_tag)
    WHERE id_tag IS NOT NULL;
