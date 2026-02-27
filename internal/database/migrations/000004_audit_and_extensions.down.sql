DROP TRIGGER IF EXISTS trg_projects_updated_at ON projects;
ALTER TABLE projects DROP COLUMN IF EXISTS updated_at;

ALTER TABLE tenants DROP COLUMN IF EXISTS health_checked_at;
ALTER TABLE tenants DROP COLUMN IF EXISTS health_status;
ALTER TABLE tenants DROP COLUMN IF EXISTS stripe_customer_id;

DROP TABLE IF EXISTS audit_log;
