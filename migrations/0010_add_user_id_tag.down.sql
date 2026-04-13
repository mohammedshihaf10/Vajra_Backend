DROP INDEX IF EXISTS users_id_tag_idx;

ALTER TABLE users
    DROP COLUMN IF EXISTS id_tag;
