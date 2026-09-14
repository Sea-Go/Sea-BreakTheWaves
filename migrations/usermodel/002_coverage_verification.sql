-- H08 v2 read-side proof cache. Deployment applies this explicitly after
-- 001_facts.sql; neither Store nor CoverageVerifier creates tables.
CREATE TABLE IF NOT EXISTS usermodel_coverage_prefix (
    manifest_sha256 char(64) PRIMARY KEY,
    producer text NOT NULL,
    binding_policy_id text NOT NULL,
    through_offset bigint NOT NULL CHECK (through_offset > 0 AND through_offset <= 9007199254740991),
    event_index_sha256 char(64) NOT NULL,
    batch_evidence_sha256 char(64) NOT NULL,
    ref_body jsonb NOT NULL,
    verified_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS usermodel_coverage_prefix_scope_idx
    ON usermodel_coverage_prefix(producer,binding_policy_id,through_offset);

CREATE TABLE IF NOT EXISTS usermodel_coverage_event (
    manifest_sha256 char(64) NOT NULL REFERENCES usermodel_coverage_prefix(manifest_sha256),
    source_offset bigint NOT NULL CHECK (source_offset > 0),
    producer text NOT NULL,
    event_id text NOT NULL,
    input_hash char(64) NOT NULL,
    authority_id text NOT NULL,
    tenant_id text NOT NULL,
    subject_id text NOT NULL,
    normalized_hash char(64) NOT NULL,
    accepted_version bigint NOT NULL CHECK (accepted_version > 0),
    row_body jsonb NOT NULL,
    PRIMARY KEY (manifest_sha256,source_offset),
    FOREIGN KEY (authority_id,tenant_id,subject_id,producer,event_id)
        REFERENCES usermodel_events(authority_id,tenant_id,subject_id,producer,event_id)
);
CREATE INDEX IF NOT EXISTS usermodel_coverage_subject_offset_idx
    ON usermodel_coverage_event(manifest_sha256,authority_id,tenant_id,subject_id,source_offset);

CREATE TABLE IF NOT EXISTS usermodel_coverage_subject (
    manifest_sha256 char(64) NOT NULL REFERENCES usermodel_coverage_prefix(manifest_sha256),
    authority_id text NOT NULL,
    tenant_id text NOT NULL,
    subject_id text NOT NULL,
    receipt_sha256 char(64) NOT NULL,
    sparse_index_sha256 char(64) NOT NULL,
    event_count bigint NOT NULL CHECK (event_count >= 0),
    ref_body jsonb NOT NULL,
    verified_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (manifest_sha256,authority_id,tenant_id,subject_id)
);

CREATE OR REPLACE FUNCTION usermodel_coverage_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'verified coverage cache is immutable';
END;
$$;
DROP TRIGGER IF EXISTS usermodel_coverage_prefix_immutable ON usermodel_coverage_prefix;
CREATE TRIGGER usermodel_coverage_prefix_immutable BEFORE UPDATE OR DELETE ON usermodel_coverage_prefix
    FOR EACH ROW EXECUTE FUNCTION usermodel_coverage_immutable();
DROP TRIGGER IF EXISTS usermodel_coverage_event_immutable ON usermodel_coverage_event;
CREATE TRIGGER usermodel_coverage_event_immutable BEFORE UPDATE OR DELETE ON usermodel_coverage_event
    FOR EACH ROW EXECUTE FUNCTION usermodel_coverage_immutable();
DROP TRIGGER IF EXISTS usermodel_coverage_subject_immutable ON usermodel_coverage_subject;
CREATE TRIGGER usermodel_coverage_subject_immutable BEFORE UPDATE OR DELETE ON usermodel_coverage_subject
    FOR EACH ROW EXECUTE FUNCTION usermodel_coverage_immutable();
