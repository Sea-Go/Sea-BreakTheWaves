-- WS08-F. Apply after 001_pools.sql; this does not activate a candidate.
CREATE TABLE IF NOT EXISTS recommend_pair_proposals (
    proposal_id char(64) PRIMARY KEY,
    module_id text NOT NULL,
    pool_release_id char(64) NOT NULL REFERENCES recommend_pool_releases(pool_release_id),
    pair_id text NOT NULL,
    proposal_body bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS recommend_item_index_generations (
    item_index_generation_id char(64) PRIMARY KEY,
    proposal_id char(64) NOT NULL REFERENCES recommend_pair_proposals(proposal_id),
    pool_release_id char(64) NOT NULL REFERENCES recommend_pool_releases(pool_release_id),
    index_body bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS recommend_pair_releases (
    encoder_pair_release_id char(64) PRIMARY KEY,
    proposal_id char(64) NOT NULL REFERENCES recommend_pair_proposals(proposal_id),
    item_index_generation_id char(64) NOT NULL REFERENCES recommend_item_index_generations(item_index_generation_id),
    module_id text NOT NULL,
    pool_release_id char(64) NOT NULL REFERENCES recommend_pool_releases(pool_release_id),
    pair_id text NOT NULL,
    approval_ref text NOT NULL,
    approval_revision bigint NOT NULL CHECK (approval_revision > 0),
    release_body bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (proposal_id,item_index_generation_id,approval_ref,approval_revision)
);

CREATE TABLE IF NOT EXISTS recommend_pair_heads (
    module_id text PRIMARY KEY,
    encoder_pair_release_id char(64) NOT NULL REFERENCES recommend_pair_releases(encoder_pair_release_id),
    pair_id text NOT NULL,
    pointer_version bigint NOT NULL CHECK (pointer_version > 0),
    approval_ref text NOT NULL,
    approval_revision bigint NOT NULL CHECK (approval_revision > 0),
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE FUNCTION recommend_reject_pair_artifact_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'recommend pair artifact is immutable'; END $$;
DROP TRIGGER IF EXISTS recommend_pair_proposals_immutable ON recommend_pair_proposals;
CREATE TRIGGER recommend_pair_proposals_immutable BEFORE UPDATE OR DELETE ON recommend_pair_proposals
FOR EACH ROW EXECUTE FUNCTION recommend_reject_pair_artifact_mutation();
DROP TRIGGER IF EXISTS recommend_item_index_generations_immutable ON recommend_item_index_generations;
CREATE TRIGGER recommend_item_index_generations_immutable BEFORE UPDATE OR DELETE ON recommend_item_index_generations
FOR EACH ROW EXECUTE FUNCTION recommend_reject_pair_artifact_mutation();
DROP TRIGGER IF EXISTS recommend_pair_releases_immutable ON recommend_pair_releases;
CREATE TRIGGER recommend_pair_releases_immutable BEFORE UPDATE OR DELETE ON recommend_pair_releases
FOR EACH ROW EXECUTE FUNCTION recommend_reject_pair_artifact_mutation();

CREATE INDEX IF NOT EXISTS recommend_pair_heads_pair ON recommend_pair_heads(pair_id);
