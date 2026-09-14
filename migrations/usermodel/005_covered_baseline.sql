-- WS08-C v2 historical candidate acceptance. Apply explicitly after
-- 001_facts.sql, 002_coverage_verification.sql, 003_features.sql and
-- 004_serving.sql. This does not create or move any v1 head or ServingBundle.
CREATE TABLE IF NOT EXISTS usermodel_covered_baselines_v2 (
    authority_id text NOT NULL,
    tenant_id text NOT NULL,
    subject_id text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    generation text NOT NULL,
    artifact_url text NOT NULL,
    artifact_sha256 char(64) NOT NULL,
    schema_version text NOT NULL CHECK (schema_version='sea.user-feature-baseline.covered.v2'),
    status text NOT NULL CHECK (status='accepted_historical_default_off'),
    spec_version text NOT NULL,
    spec_hash char(64) NOT NULL,
    prefix_manifest_sha256 char(64) NOT NULL,
    subject_receipt_sha256 char(64) NOT NULL,
    through_offset bigint NOT NULL CHECK (through_offset > 0 AND through_offset <= 9007199254740991),
    input_state_version bigint NOT NULL CHECK (input_state_version >= 0),
    as_of timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    candidate_raw bytea NOT NULL,
    candidate_body jsonb NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,subject_id,revision),
    UNIQUE (authority_id,tenant_id,subject_id,generation),
    FOREIGN KEY (prefix_manifest_sha256,authority_id,tenant_id,subject_id)
        REFERENCES usermodel_coverage_subject(manifest_sha256,authority_id,tenant_id,subject_id)
);

CREATE OR REPLACE FUNCTION usermodel_covered_baseline_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'v2 covered baseline receipt is immutable';
END;
$$;
DROP TRIGGER IF EXISTS usermodel_covered_baseline_immutable ON usermodel_covered_baselines_v2;
CREATE TRIGGER usermodel_covered_baseline_immutable BEFORE UPDATE OR DELETE ON usermodel_covered_baselines_v2
    FOR EACH ROW EXECUTE FUNCTION usermodel_covered_baseline_immutable();
