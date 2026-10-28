-- Write-operation idempotency (DESIGN M5): client-supplied keys make a
-- retried write replay its stored outcome instead of re-executing — a
-- retried payment must never charge twice. Unknown outcomes (the school
-- gave no terminal answer) are stored too, so replays stay stable while
-- the client reconciles.

CREATE TABLE IF NOT EXISTS idempotency_keys (
	key         TEXT        NOT NULL,
	user_id     TEXT        NOT NULL,
	op          TEXT        NOT NULL,
	request_hash TEXT       NOT NULL,
	status      TEXT        NOT NULL,
	response    BYTEA,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (key, user_id)
);
