-- OAuth 2.0 Device Authorization Grant (RFC 8628). Only hashes of the device
-- and user codes are stored. Expired rows are purged by the store.

CREATE TABLE IF NOT EXISTS device_grants (
	id               TEXT        PRIMARY KEY,
	device_code_hash TEXT        NOT NULL UNIQUE,
	user_code_hash   TEXT        NOT NULL UNIQUE,
	client_name      TEXT        NOT NULL DEFAULT '',
	platform         TEXT        NOT NULL DEFAULT '',
	status           TEXT        NOT NULL,
	user_id          TEXT        NOT NULL DEFAULT '',
	attempts         INTEGER     NOT NULL DEFAULT 0,
	interval_s       INTEGER     NOT NULL,
	last_polled_at   TIMESTAMPTZ,
	expires_at       TIMESTAMPTZ NOT NULL,
	created_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS device_grants_expires_idx ON device_grants(expires_at);
