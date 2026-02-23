-- Remove error_message column and revert status constraint
ALTER TABLE tenants DROP COLUMN IF EXISTS error_message;
ALTER TABLE tenants DROP CONSTRAINT chk_tenants_status;
ALTER TABLE tenants ADD CONSTRAINT chk_tenants_status
    CHECK (status IN ('provisioning', 'active', 'suspended', 'deleted'));
