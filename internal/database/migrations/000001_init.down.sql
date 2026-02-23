DROP TRIGGER IF EXISTS trg_tenants_updated_at ON tenants;
DROP TRIGGER IF EXISTS trg_nodes_updated_at ON nodes;
DROP FUNCTION IF EXISTS update_updated_at();
DROP TABLE IF EXISTS tenants;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS nodes;
