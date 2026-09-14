-- Local WS07-E receipt table. Production DC execution/authorization is a
-- separate adapter; this isolated register never activates a model/policy.
CREATE TABLE IF NOT EXISTS warehouse_ads_generations (
    generation text PRIMARY KEY,
    revision bigint NOT NULL CHECK (revision > 0),
    source_kind text NOT NULL CHECK (source_kind IN ('synthetic','observed')),
    definition_revision text NOT NULL,
    data_as_of timestamptz NOT NULL,
    input_sha256 char(64) NOT NULL,
    rows_sha256 char(64) NOT NULL,
    rollup_sha256 char(64) NOT NULL,
    manifest_sha256 char(64) NOT NULL,
    experiment_state text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
