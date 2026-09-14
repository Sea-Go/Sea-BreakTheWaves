-- WS08-D. Apply after 001_facts.sql, 002_ontology.sql and 003_features.sql.
-- Bundles are immutable input/output records. Activation is a separate CAS
-- pointer owned by usermodel and authorized by the recommend pair owner.
CREATE TABLE IF NOT EXISTS usermodel_serving_bundles (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    bundle_id char(64) NOT NULL,
    pair_id text NOT NULL, space_id text NOT NULL,
    feature_snapshot_id char(64) NOT NULL,
    bundle_body jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,subject_id,bundle_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id,feature_snapshot_id)
        REFERENCES usermodel_feature_snapshot_versions(authority_id,tenant_id,subject_id,snapshot_id)
);

CREATE OR REPLACE FUNCTION usermodel_reject_bundle_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'serving bundle is immutable';
END;
$$;
DROP TRIGGER IF EXISTS usermodel_serving_bundles_immutable ON usermodel_serving_bundles;
CREATE TRIGGER usermodel_serving_bundles_immutable
BEFORE UPDATE OR DELETE ON usermodel_serving_bundles
FOR EACH ROW EXECUTE FUNCTION usermodel_reject_bundle_mutation();

CREATE TABLE IF NOT EXISTS usermodel_serving_pointers (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    pair_id text NOT NULL,
    bundle_id char(64) NOT NULL,
    state text NOT NULL CHECK (state IN ('active','disabled')),
    approval_ref text NOT NULL,
    approval_revision bigint NOT NULL CHECK (approval_revision > 0),
    pointer_version bigint NOT NULL CHECK (pointer_version > 0),
    changed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id,tenant_id,subject_id,pair_id),
    FOREIGN KEY (authority_id,tenant_id,subject_id,bundle_id)
        REFERENCES usermodel_serving_bundles(authority_id,tenant_id,subject_id,bundle_id)
);
