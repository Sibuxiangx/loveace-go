-- Pool slot snapshots (DESIGN.md §7.7 boundary 11): pooled EC gateway
-- cookies are sensitive data — stored only as sealed vault blobs (AAD
-- domain "ecpool-slot"), capped by the pool's max_pool.

CREATE TABLE IF NOT EXISTS pool_slots (
    id TEXT PRIMARY KEY,
    blob JSONB NOT NULL,
    deposited_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_verified_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
