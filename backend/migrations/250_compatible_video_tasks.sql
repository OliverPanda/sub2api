-- Durable ownership and balance holds for compatible video channels.
-- Prices remain in groups.video_model_prices and the existing video_price_* columns.
CREATE TABLE IF NOT EXISTS compatible_video_tasks (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    upstream_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'submitting',
    settled BOOLEAN NOT NULL DEFAULT false,
    hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (hold_amount >= 0),
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS compatible_video_tasks_pending_idx
    ON compatible_video_tasks(updated_at) WHERE NOT settled;
ALTER TABLE compatible_video_tasks ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS compatible_video_tasks_idempotency_idx
    ON compatible_video_tasks(api_key_id, idempotency_key) WHERE idempotency_key <> '';
