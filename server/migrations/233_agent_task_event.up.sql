-- Durable per-task execution history.
--
-- agent_task_queue remains the authoritative current-state row. This table is
-- the append-only transition and observation ledger used for reconciliation,
-- incident review, and status projection. Relationships are intentionally not
-- enforced with foreign keys per the repository's database rules; workspace
-- deletion performs explicit application-layer cleanup.
CREATE TABLE agent_task_event (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    task_id         UUID NOT NULL,
    workspace_id    UUID NOT NULL,
    issue_id        UUID,
    runtime_id      UUID,
    sequence        BIGINT NOT NULL CHECK (sequence > 0),
    event_type      TEXT NOT NULL,
    source          TEXT NOT NULL,
    source_event_id TEXT,
    occurred_at     TIMESTAMPTZ NOT NULL,
    observed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    schema_version  INT NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    data            JSONB NOT NULL DEFAULT '{}'::jsonb
);
