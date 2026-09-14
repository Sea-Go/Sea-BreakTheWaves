-- WS08-B. Apply explicitly after 001_facts.sql. Definitions and projections are
-- domain records, not tRPC Agent Session or an independently managed graph DB.
CREATE TABLE IF NOT EXISTS usermodel_ontology_definitions (
    authority_id text NOT NULL, tenant_id text NOT NULL,
    definition_version bigint NOT NULL CHECK (definition_version > 0),
    definition_hash char(64) NOT NULL,
    definition_body jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id, tenant_id, definition_version)
);

CREATE TABLE IF NOT EXISTS usermodel_ontology_heads (
    authority_id text NOT NULL, tenant_id text NOT NULL,
    definition_version bigint NOT NULL,
    activated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id, tenant_id),
    FOREIGN KEY (authority_id, tenant_id, definition_version)
        REFERENCES usermodel_ontology_definitions(authority_id, tenant_id, definition_version)
);

-- Rebuildable cache. A row is current only when BOTH source state and active
-- definition version match. History and source evidence remain in 001 tables.
CREATE TABLE IF NOT EXISTS usermodel_ontology_projections (
    authority_id text NOT NULL, tenant_id text NOT NULL, subject_id text NOT NULL,
    definition_version bigint NOT NULL,
    state_version bigint NOT NULL CHECK (state_version >= 0),
    as_of timestamptz NOT NULL,
    next_change_at timestamptz,
    projection_body jsonb NOT NULL,
    rebuilt_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (authority_id, tenant_id, subject_id),
    FOREIGN KEY (authority_id, tenant_id, subject_id)
        REFERENCES usermodel_subject_state(authority_id, tenant_id, subject_id),
    FOREIGN KEY (authority_id, tenant_id, definition_version)
        REFERENCES usermodel_ontology_definitions(authority_id, tenant_id, definition_version)
);
CREATE INDEX IF NOT EXISTS usermodel_ontology_projection_change_idx
    ON usermodel_ontology_projections(authority_id, tenant_id, next_change_at)
    WHERE next_change_at IS NOT NULL;
