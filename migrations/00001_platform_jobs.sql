-- +goose Up
CREATE SCHEMA IF NOT EXISTS platform;

-- Transactional outbox + background jobs. Events and jobs share one queue;
-- workers claim rows with FOR UPDATE SKIP LOCKED.
CREATE TABLE platform.jobs (
    id           uuid PRIMARY KEY,
    kind         text        NOT NULL,
    payload      jsonb       NOT NULL DEFAULT '{}',
    status       text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','done','dead')),
    attempts     int         NOT NULL DEFAULT 0,
    max_attempts int         NOT NULL DEFAULT 10,
    run_at       timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX jobs_claim_idx ON platform.jobs (run_at) WHERE status IN ('pending','running');

-- +goose Down
DROP TABLE platform.jobs;
DROP SCHEMA platform;
