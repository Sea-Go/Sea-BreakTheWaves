CREATE TABLE IF NOT EXISTS content_chunk_profiles (
    profile_id text PRIMARY KEY,
    definition_hash text NOT NULL
);
CREATE TABLE IF NOT EXISTS content_builds (
    build_id text PRIMARY KEY,
    module_id text NOT NULL,
    release_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    input_hash text NOT NULL,
    operation_id text NOT NULL,
    revisions jsonb NOT NULL,
    attempt_id text NOT NULL,
    lease_epoch bigint NOT NULL CHECK (lease_epoch > 0),
    lease_expires_at timestamptz NOT NULL,
    cancel_version bigint NOT NULL CHECK (cancel_version >= 0),
    state text NOT NULL CHECK (state IN ('BUILDING','READY','FAILED','CANCELLED','SUPERSEDED')),
    chunks jsonb,
    result jsonb,
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS content_builds_module ON content_builds(module_id, build_id);
CREATE TABLE IF NOT EXISTS content_build_lanes (
    build_id text NOT NULL REFERENCES content_builds(build_id),
    lane text NOT NULL CHECK (lane IN ('dense','sparse','multivector')),
    artifact jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(build_id,lane)
);
CREATE TABLE IF NOT EXISTS content_tombstones (
    module_id text NOT NULL,
    revision_id text NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    input_hash text NOT NULL,
    PRIMARY KEY(module_id,revision_id)
);
CREATE TABLE IF NOT EXISTS content_outbox (
    event_id text PRIMARY KEY,
    build_id text NOT NULL REFERENCES content_builds(build_id),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz
);
