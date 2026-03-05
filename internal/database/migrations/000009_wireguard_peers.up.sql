-- wireguard_peers: WireGuard VPN peers for the mesh network
CREATE TABLE wireguard_peers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    public_key TEXT NOT NULL UNIQUE,
    preshared_key_encrypted TEXT,
    wg_ip TEXT NOT NULL UNIQUE,
    allowed_ips TEXT NOT NULL,
    endpoint TEXT,
    type TEXT NOT NULL DEFAULT 'user',
    tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_wireguard_peers_type CHECK (type IN ('admin', 'node', 'user'))
);

CREATE INDEX idx_wireguard_peers_type ON wireguard_peers(type);
CREATE INDEX idx_wireguard_peers_tenant_id ON wireguard_peers(tenant_id);
CREATE INDEX idx_wireguard_peers_enabled ON wireguard_peers(enabled);

CREATE TRIGGER trg_wireguard_peers_updated_at
    BEFORE UPDATE ON wireguard_peers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
