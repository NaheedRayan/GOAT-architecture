-- +goose Up
-- Append-only record of back-office actions: who changed what, and when.
CREATE TABLE platform.audit_log (
    id        uuid PRIMARY KEY,
    at        timestamptz NOT NULL DEFAULT now(),
    actor_id  uuid,
    actor_role text NOT NULL DEFAULT '',
    action    text        NOT NULL,
    entity    text        NOT NULL DEFAULT '',
    entity_id text        NOT NULL DEFAULT '',
    detail    jsonb       NOT NULL DEFAULT '{}',
    ip        text        NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at_idx ON platform.audit_log (at DESC);
CREATE INDEX audit_log_entity_idx ON platform.audit_log (entity, entity_id, at DESC);
CREATE INDEX audit_log_actor_idx ON platform.audit_log (actor_id, at DESC);

-- +goose Down
DROP TABLE platform.audit_log;
