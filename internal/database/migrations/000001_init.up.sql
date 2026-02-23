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
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
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
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
