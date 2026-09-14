-- WS08-A. Explicit deployment migration; usermodel constructors never apply DDL.
CREATE TABLE IF NOT EXISTS usermodel_subject_state (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    state_version bigint NOT NULL DEFAULT 0 CHECK (state_version >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id, tenant_id, subject_id)
);

CREATE TABLE IF NOT EXISTS usermodel_events (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    producer text NOT NULL, event_id text NOT NULL,
    normalized_hash char(64) NOT NULL,
    event_body jsonb NOT NULL,
    action text NOT NULL CHECK (action IN ('assert','correct','retract')),
    semantic_kind text NOT NULL,
    occurred_at timestamptz NOT NULL, observed_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    source_partition text NOT NULL, source_sequence bigint,
    supersedes_producer text, supersedes_event_id text,
    status text NOT NULL CHECK (status IN ('accepted','pending_dependency')),
    initial_status text NOT NULL CHECK (initial_status IN ('accepted','pending_dependency')),
    initial_version bigint NOT NULL CHECK (initial_version >= 0),
    accepted_version bigint,
    PRIMARY KEY (authority_id, tenant_id, subject_id, producer, event_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id) REFERENCES usermodel_subject_state,
    CHECK ((supersedes_producer IS NULL) = (supersedes_event_id IS NULL)),
    CHECK ((action = 'assert') = (supersedes_event_id IS NULL)),
    CHECK (source_sequence IS NULL OR source_sequence > 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS usermodel_event_position_uq
    ON usermodel_events(authority_id,tenant_id,subject_id,producer,source_partition,source_sequence)
    WHERE source_sequence IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS usermodel_successor_uq
    ON usermodel_events(authority_id,tenant_id,subject_id,supersedes_producer,supersedes_event_id)
    WHERE supersedes_event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS usermodel_events_history_idx
    ON usermodel_events(authority_id,tenant_id,subject_id,occurred_at,producer,event_id);

-- A pending identity binding is source scoped. It is not a user fact until RTW
-- resolves the complete SubjectRef; the original source envelope remains fixed.
CREATE TABLE IF NOT EXISTS usermodel_unmapped_events (
    authority_id text NOT NULL, tenant_id text NOT NULL, external_subject_id text NOT NULL,
    producer text NOT NULL, event_id text NOT NULL, normalized_hash char(64) NOT NULL,
    event_body jsonb NOT NULL, received_at timestamptz NOT NULL DEFAULT now(),
    bound_subject_id text, bound_version bigint, bound_status text,
    PRIMARY KEY (authority_id,tenant_id,external_subject_id,producer,event_id),
    CHECK ((bound_subject_id IS NULL) = (bound_version IS NULL)),
    CHECK ((bound_subject_id IS NULL) = (bound_status IS NULL))
);

CREATE TABLE IF NOT EXISTS usermodel_subject_bindings (
    authority_id text NOT NULL, tenant_id text NOT NULL, external_subject_id text NOT NULL,
    subject_id text NOT NULL, first_bound_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,external_subject_id)
);

-- Current projection can be rebuilt from events. Historical rows are never
-- overwritten or deleted when a correction or retraction changes this table.
CREATE TABLE IF NOT EXISTS usermodel_active_facts (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    producer text NOT NULL, event_id text NOT NULL,
    impression_id text,
    activated_version bigint NOT NULL,
    PRIMARY KEY (authority_id,tenant_id,subject_id,producer,event_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id,producer,event_id)
        REFERENCES usermodel_events(authority_id,tenant_id,subject_id,producer,event_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS usermodel_active_impression_uq
    ON usermodel_active_facts(authority_id,tenant_id,subject_id,impression_id)
    WHERE impression_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS usermodel_attributions (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    producer text NOT NULL, event_id text NOT NULL,
    impression_id text NOT NULL, source_evidence_ref text NOT NULL,
    linked_version bigint NOT NULL, linked_at timestamptz NOT NULL DEFAULT now(),
    revoked_version bigint,
    PRIMARY KEY (authority_id,tenant_id,subject_id,producer,event_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id,producer,event_id)
        REFERENCES usermodel_events(authority_id,tenant_id,subject_id,producer,event_id)
);

CREATE TABLE IF NOT EXISTS usermodel_watermarks (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    producer text NOT NULL, source_partition text NOT NULL,
    contiguous_sequence bigint NOT NULL DEFAULT 0,
    max_seen_sequence bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (authority_id,tenant_id,subject_id,producer,source_partition)
);

CREATE TABLE IF NOT EXISTS usermodel_outbox (
    outbox_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    state_version bigint NOT NULL, event_type text NOT NULL,
    producer text NOT NULL, event_id text NOT NULL,
    payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    UNIQUE(authority_id,tenant_id,subject_id,state_version)
);
CREATE INDEX IF NOT EXISTS usermodel_outbox_pending_idx
    ON usermodel_outbox(outbox_id) WHERE delivered_at IS NULL;
