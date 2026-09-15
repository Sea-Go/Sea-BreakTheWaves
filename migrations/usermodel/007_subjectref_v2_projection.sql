-- SubjectRef v2 candidate for the user fact ledger. Apply explicitly after
-- 001..006, and only after the old-row preflight has cleared its blockers.
-- No constructor applies this DDL and no old fact/event/outbox row is rewritten.
-- Apply the whole file atomically with psql -X -v ON_ERROR_STOP=1
-- --single-transaction -f 007_subjectref_v2_projection.sql (or one pgx Tx).
CREATE TABLE IF NOT EXISTS usermodel_subjectref_v2_projection (
    legacy_authority_id text NOT NULL,
    legacy_tenant_id text NOT NULL,
    legacy_subject_id text NOT NULL,
    issuer text NOT NULL,
    subject_id text NOT NULL,
    projected_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT usermodel_sr_v2_legacy_pk
        PRIMARY KEY (legacy_authority_id,legacy_tenant_id,legacy_subject_id),
    CONSTRAINT usermodel_sr_v2_canonical_uq UNIQUE (issuer,subject_id),
    CONSTRAINT usermodel_sr_v2_owner_fk
        FOREIGN KEY (legacy_authority_id,legacy_tenant_id,legacy_subject_id)
        REFERENCES usermodel_subject_state(authority_id,tenant_id,subject_id),
    CONSTRAINT usermodel_sr_v2_identity_ck CHECK (issuer COLLATE "C" = 'rtw.identity'
           AND legacy_authority_id COLLATE "C" = issuer COLLATE "C"
           AND legacy_tenant_id COLLATE "C" = 'platform'
           AND legacy_subject_id COLLATE "C" = subject_id COLLATE "C"),
    CONSTRAINT usermodel_sr_v2_uid_ck CHECK (subject_id COLLATE "C" ~ '^[1-9][0-9]*$'
           AND (length(subject_id)<19 OR
                (length(subject_id)=19 AND subject_id COLLATE "C" <= '9223372036854775807')))
);

-- IF NOT EXISTS permits retry, but does not certify a pre-existing table.
-- Check exact PG16 definitions and column order/type/nullability, so a weak
-- same-named CHECK or FK cannot be mistaken for the migration's real guard.
DO $$
DECLARE guard_count integer;
DECLARE column_count integer;
DECLARE live_column_count integer;
BEGIN
    WITH expected(name, kind, definition) AS (VALUES
      ('usermodel_sr_v2_legacy_pk','p',
       'PRIMARY KEY (legacy_authority_id, legacy_tenant_id, legacy_subject_id)'),
      ('usermodel_sr_v2_canonical_uq','u','UNIQUE (issuer, subject_id)'),
      ('usermodel_sr_v2_owner_fk','f',
       'FOREIGN KEY (legacy_authority_id, legacy_tenant_id, legacy_subject_id) REFERENCES usermodel_subject_state(authority_id, tenant_id, subject_id)'),
      ('usermodel_sr_v2_identity_ck','c',
       $expected$CHECK ((((issuer COLLATE "C") = 'rtw.identity'::text) AND ((legacy_authority_id COLLATE "C") = (issuer COLLATE "C")) AND ((legacy_tenant_id COLLATE "C") = 'platform'::text) AND ((legacy_subject_id COLLATE "C") = (subject_id COLLATE "C"))))$expected$),
      ('usermodel_sr_v2_uid_ck','c',
       $expected$CHECK ((((subject_id COLLATE "C") ~ '^[1-9][0-9]*$'::text) AND ((length(subject_id) < 19) OR ((length(subject_id) = 19) AND ((subject_id COLLATE "C") <= '9223372036854775807'::text)))))$expected$)
    )
    SELECT count(*) INTO guard_count
    FROM expected e JOIN pg_constraint c
      ON c.conrelid='usermodel_subjectref_v2_projection'::regclass
     AND c.conname=e.name AND c.contype::text=e.kind
     AND c.convalidated AND pg_get_constraintdef(c.oid)=e.definition;
    SELECT count(*) INTO column_count FROM pg_attribute a
    WHERE a.attrelid='usermodel_subjectref_v2_projection'::regclass
      AND a.attnum>0 AND NOT a.attisdropped AND a.attnotnull
      AND (a.attnum,a.attname,a.atttypid) IN (
        (1,'legacy_authority_id','text'::regtype),
        (2,'legacy_tenant_id','text'::regtype),
        (3,'legacy_subject_id','text'::regtype),
        (4,'issuer','text'::regtype),
        (5,'subject_id','text'::regtype),
        (6,'projected_at','timestamptz'::regtype));
    SELECT count(*) INTO live_column_count FROM pg_attribute
    WHERE attrelid='usermodel_subjectref_v2_projection'::regclass
      AND attnum>0 AND NOT attisdropped;
    IF guard_count <> 5 OR column_count <> 6 OR live_column_count <> 6
       OR (SELECT pg_get_expr(adbin,adrelid) FROM pg_attrdef
           WHERE adrelid='usermodel_subjectref_v2_projection'::regclass
             AND adnum=6) IS DISTINCT FROM 'now()' THEN
        RAISE EXCEPTION 'SubjectRef v2 projection schema guards differ';
    END IF;
END;
$$;

-- A projection is an identity assertion, not an editable alias. Disabling
-- the candidate application option is the rollback point; old keys remain.
CREATE OR REPLACE FUNCTION usermodel_subjectref_v2_projection_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'SubjectRef v2 projection is immutable' USING ERRCODE='23514';
END;
$$;
DROP TRIGGER IF EXISTS usermodel_subjectref_v2_projection_immutable
    ON usermodel_subjectref_v2_projection;
CREATE TRIGGER usermodel_subjectref_v2_projection_immutable
    BEFORE UPDATE OR DELETE ON usermodel_subjectref_v2_projection
    FOR EACH ROW EXECUTE FUNCTION usermodel_subjectref_v2_projection_immutable();

-- These are candidate read surfaces only. They preserve the original v1
-- event_body, normalized_hash, Outbox payload and their business keys.
CREATE OR REPLACE VIEW usermodel_subject_state_subjectref_v2 AS
SELECT p.issuer,p.subject_id,s.state_version,s.updated_at
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_subject_state s ON
    (s.authority_id,s.tenant_id,s.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);

CREATE OR REPLACE VIEW usermodel_events_subjectref_v2 AS
SELECT p.issuer,p.subject_id,e.producer,e.event_id,e.normalized_hash,
       e.event_body,e.action,e.semantic_kind,e.occurred_at,e.observed_at,
       e.source_partition,e.source_sequence,e.status,e.initial_version,
       e.accepted_version
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_events e ON
    (e.authority_id,e.tenant_id,e.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);

CREATE OR REPLACE VIEW usermodel_active_facts_subjectref_v2 AS
SELECT p.issuer,p.subject_id,a.producer,a.event_id,a.impression_id,
       a.activated_version
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_active_facts a ON
    (a.authority_id,a.tenant_id,a.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);

CREATE OR REPLACE VIEW usermodel_outbox_subjectref_v2 AS
SELECT p.issuer,p.subject_id,o.outbox_id,o.state_version,o.event_type,
       o.producer,o.event_id,o.payload,o.created_at,o.delivered_at
FROM usermodel_subjectref_v2_projection p
JOIN usermodel_outbox o ON
    (o.authority_id,o.tenant_id,o.subject_id)=
    (p.legacy_authority_id,p.legacy_tenant_id,p.legacy_subject_id);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger
      WHERE tgrelid='usermodel_subjectref_v2_projection'::regclass
        AND tgname='usermodel_subjectref_v2_projection_immutable'
        AND tgfoid='usermodel_subjectref_v2_projection_immutable'::regproc
        AND tgenabled='O' AND tgtype=27) THEN
        RAISE EXCEPTION 'SubjectRef v2 projection immutability guard differs';
    END IF;
END;
$$;
