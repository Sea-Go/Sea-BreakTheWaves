-- WS08-E baseline. Apply explicitly; opening the store never migrates a DB.
CREATE TABLE IF NOT EXISTS recommend_pool_releases (
    pool_release_id char(64) PRIMARY KEY,
    module_id text NOT NULL,
    publication_revision bigint NOT NULL CHECK (publication_revision > 0),
    feature_version text NOT NULL,
    feature_hash char(64) NOT NULL,
    release_body bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE FUNCTION recommend_reject_release_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'recommend pool release is immutable'; END $$;
DROP TRIGGER IF EXISTS recommend_pool_releases_immutable ON recommend_pool_releases;
CREATE TRIGGER recommend_pool_releases_immutable BEFORE UPDATE OR DELETE ON recommend_pool_releases
FOR EACH ROW EXECUTE FUNCTION recommend_reject_release_mutation();

CREATE TABLE IF NOT EXISTS recommend_pool_heads (
    module_id text PRIMARY KEY,
    pool_release_id char(64) NOT NULL REFERENCES recommend_pool_releases(pool_release_id),
    publication_revision bigint NOT NULL CHECK (publication_revision > 0),
    pointer_version bigint NOT NULL CHECK (pointer_version > 0),
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS recommend_pool_releases_module ON recommend_pool_releases(module_id,publication_revision);
