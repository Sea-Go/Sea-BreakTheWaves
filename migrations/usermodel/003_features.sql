-- WS08-C. Apply after 001_facts.sql and 002_ontology.sql. A warehouse
-- generation is immutable; the active head and derived serving snapshot move
-- separately under the subject-state lock.
CREATE TABLE IF NOT EXISTS usermodel_feature_baselines (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    generation text NOT NULL,
    spec_version text NOT NULL,
    spec_hash char(64) NOT NULL,
    baseline_hash char(64) NOT NULL,
    baseline_body jsonb NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,subject_id,revision),
    UNIQUE (authority_id,tenant_id,subject_id,generation),
    FOREIGN KEY (authority_id,tenant_id,subject_id)
        REFERENCES usermodel_subject_state(authority_id,tenant_id,subject_id)
);

CREATE TABLE IF NOT EXISTS usermodel_feature_heads (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    revision bigint NOT NULL,
    changed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,subject_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id,revision)
        REFERENCES usermodel_feature_baselines(authority_id,tenant_id,subject_id,revision)
);

CREATE TABLE IF NOT EXISTS usermodel_feature_snapshots (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    state_version bigint NOT NULL CHECK (state_version >= 0),
    spec_version text NOT NULL,
    spec_hash char(64) NOT NULL,
    baseline_revision bigint,
    as_of timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    snapshot_body jsonb NOT NULL,
    built_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,subject_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id)
        REFERENCES usermodel_subject_state(authority_id,tenant_id,subject_id)
);

-- A request/training row may retain this immutable ID after the current
-- snapshot pointer changes. The active table above is only the latest view.
CREATE TABLE IF NOT EXISTS usermodel_feature_snapshot_versions (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    snapshot_id char(64) NOT NULL,
    snapshot_body jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,subject_id,snapshot_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id)
        REFERENCES usermodel_subject_state(authority_id,tenant_id,subject_id)
);
