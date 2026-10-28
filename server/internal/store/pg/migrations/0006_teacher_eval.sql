-- Teacher auto-evaluation (client parity with the Android app): consent,
-- background tasks, per-course progress, and a capped progress log.
-- At most one queued/running task per user is enforced by the partial
-- unique index; the store keeps 200 log rows per task.

CREATE TABLE IF NOT EXISTS teacher_eval_consents (
	user_id     TEXT        PRIMARY KEY,
	version     TEXT        NOT NULL,
	accepted_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS teacher_eval_tasks (
	id               TEXT        PRIMARY KEY,
	user_id          TEXT        NOT NULL,
	device_id        TEXT        NOT NULL DEFAULT '',
	status           TEXT        NOT NULL,
	error_kind       TEXT        NOT NULL DEFAULT '',
	error_message    TEXT        NOT NULL DEFAULT '',
	cancel_requested BOOLEAN     NOT NULL DEFAULT FALSE,
	course_total     INT         NOT NULL DEFAULT 0,
	score_mode       TEXT        NOT NULL DEFAULT 'random',
	created_at       TIMESTAMPTZ NOT NULL,
	started_at       TIMESTAMPTZ,
	finished_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS teacher_eval_tasks_user_idx ON teacher_eval_tasks(user_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS teacher_eval_tasks_one_active
	ON teacher_eval_tasks(user_id) WHERE status IN ('queued', 'running');

CREATE TABLE IF NOT EXISTS teacher_eval_task_courses (
	task_id         TEXT        NOT NULL REFERENCES teacher_eval_tasks(id) ON DELETE CASCADE,
	course_key      TEXT        NOT NULL,
	name            TEXT        NOT NULL DEFAULT '',
	teacher         TEXT        NOT NULL DEFAULT '',
	position        INT         NOT NULL,
	status          TEXT        NOT NULL,
	error_kind      TEXT        NOT NULL DEFAULT '',
	message         TEXT        NOT NULL DEFAULT '',
	submit_attempts INT         NOT NULL DEFAULT 0,
	submitted_at    TIMESTAMPTZ,
	updated_at      TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (task_id, course_key)
);

CREATE TABLE IF NOT EXISTS teacher_eval_task_logs (
	seq     BIGSERIAL   PRIMARY KEY,
	task_id TEXT        NOT NULL REFERENCES teacher_eval_tasks(id) ON DELETE CASCADE,
	at      TIMESTAMPTZ NOT NULL,
	message TEXT        NOT NULL
);

CREATE INDEX IF NOT EXISTS teacher_eval_task_logs_task_idx ON teacher_eval_task_logs(task_id, seq);
