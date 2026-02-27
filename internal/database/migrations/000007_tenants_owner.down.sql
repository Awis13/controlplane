DROP INDEX IF EXISTS idx_tenants_owner_id;
ALTER TABLE tenants DROP COLUMN IF EXISTS owner_id;
