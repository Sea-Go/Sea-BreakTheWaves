-- The local READY outbox remains the durable source of work. This companion
-- row records the DC attempt needed to finish the RTW and technical handoff.
-- Credentials and HTTP responses are deliberately not persisted here.
CREATE TABLE IF NOT EXISTS content_index_dispatch (
    build_id text PRIMARY KEY REFERENCES content_builds(build_id),
    job_id text NOT NULL,
    worker_id text NOT NULL,
    attempt_id text NOT NULL,
    lease_epoch bigint NOT NULL CHECK (lease_epoch > 0),
    cancel_version bigint NOT NULL CHECK (cancel_version >= 0),
    lease_expires_at timestamptz NOT NULL,
    stage text NOT NULL DEFAULT 'pending'
        CHECK (stage IN ('pending','needs_new_attempt','manual','complete')),
    claim_epoch bigint NOT NULL DEFAULT 0,
    claim_until timestamptz,
    rtw_accepted_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS content_index_dispatch_pending
    ON content_index_dispatch(stage, claim_until, updated_at)
    WHERE stage = 'pending';
