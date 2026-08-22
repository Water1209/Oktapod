CREATE TABLE task_presentations (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    presentation_kind TEXT NOT NULL CHECK (presentation_kind IN ('pending_auto', 'answer', 'task')),
    resolved_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_task_presentations_kind
    ON task_presentations(presentation_kind, updated_at DESC);

CREATE TABLE model_call_performance (
    call_id TEXT PRIMARY KEY REFERENCES model_calls(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL,
    first_byte_ms INTEGER NOT NULL DEFAULT 0,
    first_safe_delta_ms INTEGER NOT NULL DEFAULT 0,
    assembly_ms INTEGER NOT NULL DEFAULT 0,
    request_bytes INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_model_call_performance_purpose
    ON model_call_performance(purpose, created_at DESC);

CREATE TABLE tool_batch_schedulers (
    batch_id TEXT PRIMARY KEY REFERENCES tool_batches(id) ON DELETE CASCADE,
    protocol_version TEXT NOT NULL,
    execution_mode TEXT NOT NULL CHECK (execution_mode = 'barriered'),
    group_count INTEGER NOT NULL CHECK (group_count > 0),
    created_at TEXT NOT NULL
);

CREATE TABLE tool_call_barriers (
    tool_call_id TEXT PRIMARY KEY REFERENCES tool_calls(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    barrier_group INTEGER NOT NULL CHECK (barrier_group >= 0),
    created_at TEXT NOT NULL
);

CREATE INDEX idx_tool_call_barriers_claim
    ON tool_call_barriers(barrier_group, step_id);

CREATE TABLE run_context_checkpoints (
    run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    projection_version TEXT NOT NULL,
    source_through_position INTEGER NOT NULL,
    content_json TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
