-- WS08-D v2 historical snapshots and default-off fixed-rule bundles.
-- Apply explicitly after 001_facts, 002_coverage_verification,
-- 003_features, 004_serving, and 005_covered_baseline.
CREATE TABLE IF NOT EXISTS usermodel_covered_snapshots_v2 (
  authority_id text NOT NULL,
  tenant_id text NOT NULL,
  subject_id text NOT NULL,
  snapshot_id char(64) NOT NULL,
  baseline_revision bigint NOT NULL CHECK (baseline_revision > 0),
  baseline_artifact_sha256 char(64) NOT NULL,
  prefix_manifest_sha256 char(64) NOT NULL,
  subject_receipt_sha256 char(64) NOT NULL,
  spec_version text NOT NULL,
  spec_hash char(64) NOT NULL,
  input_state_version bigint NOT NULL CHECK (input_state_version >= 0),
  snapshot_raw_sha256 char(64) NOT NULL,
  snapshot_raw bytea NOT NULL,
  snapshot_body jsonb NOT NULL,
  status text NOT NULL CHECK (status='historical_default_off'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (authority_id,tenant_id,subject_id,snapshot_id),
  UNIQUE (authority_id,tenant_id,subject_id,baseline_revision),
  FOREIGN KEY (authority_id,tenant_id,subject_id,baseline_revision)
    REFERENCES usermodel_covered_baselines_v2(authority_id,tenant_id,subject_id,revision),
  FOREIGN KEY (prefix_manifest_sha256,authority_id,tenant_id,subject_id)
    REFERENCES usermodel_coverage_subject(manifest_sha256,authority_id,tenant_id,subject_id)
);

CREATE TABLE IF NOT EXISTS usermodel_covered_bundle_candidates_v2 (
  authority_id text NOT NULL,
  tenant_id text NOT NULL,
  subject_id text NOT NULL,
  bundle_id char(64) NOT NULL,
  snapshot_id char(64) NOT NULL,
  pair_id text NOT NULL,
  space_id text NOT NULL,
  encoder_id text NOT NULL,
  pair_feature_spec_hash char(64) NOT NULL,
  bundle_raw_sha256 char(64) NOT NULL,
  bundle_raw bytea NOT NULL,
  bundle_body jsonb NOT NULL,
  status text NOT NULL CHECK (status='candidate_default_off'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (authority_id,tenant_id,subject_id,bundle_id),
  UNIQUE (authority_id,tenant_id,subject_id,snapshot_id,pair_id),
  FOREIGN KEY (authority_id,tenant_id,subject_id,snapshot_id)
    REFERENCES usermodel_covered_snapshots_v2(authority_id,tenant_id,subject_id,snapshot_id)
);

CREATE OR REPLACE FUNCTION usermodel_covered_snapshot_bundle_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'v2 covered snapshot and bundle candidates are immutable';
END;
$$;
DROP TRIGGER IF EXISTS usermodel_covered_snapshot_immutable ON usermodel_covered_snapshots_v2;
CREATE TRIGGER usermodel_covered_snapshot_immutable BEFORE UPDATE OR DELETE ON usermodel_covered_snapshots_v2
  FOR EACH ROW EXECUTE FUNCTION usermodel_covered_snapshot_bundle_immutable();
DROP TRIGGER IF EXISTS usermodel_covered_bundle_immutable ON usermodel_covered_bundle_candidates_v2;
CREATE TRIGGER usermodel_covered_bundle_immutable BEFORE UPDATE OR DELETE ON usermodel_covered_bundle_candidates_v2
  FOR EACH ROW EXECUTE FUNCTION usermodel_covered_snapshot_bundle_immutable();
