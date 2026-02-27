ALTER TABLE tenants ADD COLUMN owner_id UUID REFERENCES users(id);
CREATE INDEX idx_tenants_owner_id ON tenants(owner_id);
