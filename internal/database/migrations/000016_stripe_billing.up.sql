-- Add tier column for billing tiers (free, starter, pro, studio)
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'free';
