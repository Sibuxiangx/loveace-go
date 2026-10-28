-- M3/M4 additions: cold-eviction user session snapshots (sealed blobs,
-- AAD domain "user-session"), announcements (DESIGN §11.2) and the
-- academic calendar terms/rules (§11.1).

CREATE TABLE IF NOT EXISTS user_session_snapshots (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    blob JSONB NOT NULL,
    saved_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS announcements (
    id UUID PRIMARY KEY,
    level TEXT NOT NULL DEFAULT 'info',
    body_md TEXT NOT NULL,
    audience TEXT NOT NULL DEFAULT 'both',
    pinned BOOLEAN NOT NULL DEFAULT FALSE,
    requires_ack BOOLEAN NOT NULL DEFAULT FALSE,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    starts_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS announcements_active_idx ON announcements(active, pinned);

CREATE TABLE IF NOT EXISTS announcement_acks (
    announcement_id UUID NOT NULL REFERENCES announcements(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    acked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (announcement_id, user_id)
);

CREATE TABLE IF NOT EXISTS calendar_terms (
    id TEXT PRIMARY KEY,
    first_week_monday DATE NOT NULL,
    weeks INTEGER NOT NULL DEFAULT 20
);

CREATE TABLE IF NOT EXISTS calendar_rules (
    id UUID PRIMARY KEY,
    term_id TEXT NOT NULL REFERENCES calendar_terms(id) ON DELETE CASCADE,
    day DATE NOT NULL,
    kind TEXT NOT NULL,
    follow_week INTEGER,
    follow_weekday INTEGER
);

CREATE INDEX IF NOT EXISTS calendar_rules_term_idx ON calendar_rules(term_id, day);
