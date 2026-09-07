-- The RAM a tenant reserves on its node now belongs to the tenant, not to the
-- provisioning flow. reserved_ram_mb records how much the tenant holds so a
-- release can be idempotent and driven by the tenant record alone. NULL means
-- the tenant predates this column and its reservation is unknown: it must be
-- reconciled manually rather than guessed at.
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS reserved_ram_mb INTEGER;

-- reservation_released_at marks the moment the reservation was handed back to
-- the node. NULL means it is still held. A non-NULL value makes a later release
-- a no-op, which is what stops the double-release that used to zero a node's
-- accounting while a studio still existed.
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS reservation_released_at TIMESTAMPTZ;
