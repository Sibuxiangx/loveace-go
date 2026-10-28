-- Calendar workflow (DESIGN.md §11.1): rules are authored as drafts,
-- previewed, then published (version bump + audit). The public calendar
-- only ever renders the published set of the term's current version.

ALTER TABLE calendar_rules ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'published';
ALTER TABLE calendar_rules ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE calendar_terms ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
