-- Add error and deleting states to tenant status, add error_message column
ALTER TABLE tenants DROP CONSTRAINT chk_tenants_status;
ALTER TABLE tenants ADD CONSTRAINT chk_tenants_status
    CHECK (status IN ('provisioning', 'active', 'error', 'deleting', 'deleted', 'suspended'));
ALTER TABLE tenants ADD COLUMN error_message TEXT;
