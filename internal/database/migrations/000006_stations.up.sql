-- stations: radio station metadata
CREATE TABLE stations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    genre TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    artwork_url TEXT NOT NULL DEFAULT '',
    owner_id UUID REFERENCES users(id) ON DELETE SET NULL,
    tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL,
    is_public BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_stations_slug ON stations(slug);
CREATE INDEX idx_stations_is_public ON stations(is_public) WHERE is_public = true;

CREATE TRIGGER trg_stations_updated_at
    BEFORE UPDATE ON stations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
