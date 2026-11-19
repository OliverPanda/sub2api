-- Durable Replicate prediction ownership and idempotent music billing state.
CREATE TABLE IF NOT EXISTS replicate_music_tasks (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    upstream_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'submitting',
    settled BOOLEAN NOT NULL DEFAULT false,
    hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (hold_amount >= 0),
    output JSONB,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS replicate_music_tasks_pending_idx
    ON replicate_music_tasks(updated_at) WHERE NOT settled;
