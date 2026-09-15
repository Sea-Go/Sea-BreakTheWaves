-- SubjectRef v2 candidate for the user fact ledger. Apply explicitly after
-- 001..006, and only after the old-row preflight has cleared its blockers.
-- No constructor applies this DDL and no old fact/event/outbox row is rewritten.
CREATE TABLE usermodel_subjectref_v2_projection (
    legacy_authority_id text NOT NULL,
    legacy_tenant_id text NOT NULL,
    legacy_subject_id text NOT NULL,
    issuer text NOT NULL,
    subject_id text NOT NULL,
    projected_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (legacy_authority_id,legacy_tenant_id,legacy_subject_id),
    UNIQUE (issuer,subject_id),
    FOREIGN KEY (legacy_authority_id,legacy_tenant_id,legacy_subject_id)
        REFERENCES usermodel_subject_state(authority_id,tenant_id,subject_id),
    CHECK (issuer COLLATE "C" = 'rtw.identity'
           AND legacy_authority_id COLLATE "C" = issuer COLLATE "C"
           AND legacy_tenant_id COLLATE "C" = 'platform'
           AND legacy_subject_id COLLATE "C" = subject_id COLLATE "C"),
    CHECK (subject_id COLLATE "C" ~ '^[1-9][0-9]*$'
           AND (length(subject_id)<19 OR
                (length(subject_id)=19 AND subject_id COLLATE "C" <= '9223372036854775807')))
);

-- A projection is an identity assertion, not an editable alias. Disabling
-- the candidate application option is the rollback point; old keys remain.
CREATE FUNCTION usermodel_subjectref_v2_projection_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'SubjectRef v2 projection is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER usermodel_subjectref_v2_projection_immutable
    BEFORE UPDATE OR DELETE ON usermodel_subjectref_v2_projection
    FOR EACH ROW EXECUTE FUNCTION usermodel_subjectref_v2_projection_immutable();

-- These are candidate read surfaces only. They preserve the original v1
-- event_body, normalized_hash, Outbox payload and their business keys.
CREATE VIEW usermodel_subject_state_subjectref_v2 AS
SELECT p.issuer,p.subject_id,s.state_version,s.updated_at
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_subject_state s ON
    (s.authority_id,s.tenant_id,s.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);

CREATE VIEW usermodel_events_subjectref_v2 AS
SELECT p.issuer,p.subject_id,e.producer,e.event_id,e.normalized_hash,
       e.event_body,e.action,e.semantic_kind,e.occurred_at,e.observed_at,
       e.source_partition,e.source_sequence,e.status,e.initial_version,
       e.accepted_version
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_events e ON
    (e.authority_id,e.tenant_id,e.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);

CREATE VIEW usermodel_active_facts_subjectref_v2 AS
SELECT p.issuer,p.subject_id,a.producer,a.event_id,a.impression_id,
       a.activated_version
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_active_facts a ON
    (a.authority_id,a.tenant_id,a.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);

CREATE VIEW usermodel_outbox_subjectref_v2 AS
SELECT p.issuer,p.subject_id,o.outbox_id,o.state_version,o.event_type,
       o.producer,o.event_id,o.payload,o.created_at,o.delivered_at
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_outbox o ON
    (o.authority_id,o.tenant_id,o.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);
