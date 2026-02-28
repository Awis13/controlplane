-- Drop old absolute unique constraints
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_name_key;
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_subdomain_key;

-- Create partial unique indexes (allow reuse of deleted names)
CREATE UNIQUE INDEX idx_tenants_name_active ON tenants(name) WHERE status != 'deleted';
CREATE UNIQUE INDEX idx_tenants_subdomain_active ON tenants(subdomain) WHERE status != 'deleted';

-- Add 'deleting' to CHECK constraint (was missing)
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS chk_tenants_status;
ALTER TABLE tenants ADD CONSTRAINT chk_tenants_status CHECK (status IN ('provisioning', 'active', 'suspended', 'deleting', 'error', 'deleted'));
