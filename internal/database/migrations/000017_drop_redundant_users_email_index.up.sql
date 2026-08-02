-- users.email is declared UNIQUE in 000005, which already creates a unique
-- B-tree index (users_email_key). idx_users_email indexes the same column the
-- same way, so every write to users maintained two identical indexes.
DROP INDEX IF EXISTS idx_users_email;
