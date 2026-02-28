DROP INDEX IF EXISTS idx_tenants_name_active;
DROP INDEX IF EXISTS idx_tenants_subdomain_active;
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS chk_tenants_status;
ALTER TABLE tenants ADD CONSTRAINT chk_tenants_status CHECK (status IN ('provisioning', 'active', 'suspended', 'deleted'));
-- Re-create absolute unique constraints
ALTER TABLE tenants ADD CONSTRAINT tenants_name_key UNIQUE (name);
ALTER TABLE tenants ADD CONSTRAINT tenants_subdomain_key UNIQUE (subdomain);
