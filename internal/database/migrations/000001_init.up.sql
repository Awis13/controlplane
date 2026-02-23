-- nodes: Proxmox compute nodes
CREATE TABLE nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    tailscale_ip TEXT NOT NULL,
    proxmox_url TEXT NOT NULL,
    api_token_encrypted TEXT NOT NULL,
    total_ram_mb INTEGER NOT NULL,
    allocated_ram_mb INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_nodes_status CHECK (status IN ('active', 'maintenance', 'offline'))
);

-- projects: LXC project types (studio23, future projects)
CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    template_id INTEGER NOT NULL,
    ports INTEGER[] NOT NULL DEFAULT '{}',
    stripe_price_id TEXT,
    health_path TEXT NOT NULL DEFAULT '/api/health',
    ram_mb INTEGER NOT NULL DEFAULT 1536,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- tenants: individual customer instances
CREATE TABLE tenants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    project_id UUID NOT NULL REFERENCES projects(id),
    node_id UUID NOT NULL REFERENCES nodes(id),
    lxc_id INTEGER,
    subdomain TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'provisioning',
    stripe_subscription_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_tenants_status CHECK (status IN ('provisioning', 'active', 'suspended', 'deleted'))
);

-- Indexes on FK columns
CREATE INDEX idx_tenants_project_id ON tenants(project_id);
CREATE INDEX idx_tenants_node_id ON tenants(node_id);

-- Trigger for auto-updating updated_at
CREATE OR REPLACE FUNCTION update_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_nodes_updated_at
    BEFORE UPDATE ON nodes
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_tenants_updated_at
    BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
