CREATE SCHEMA IF NOT EXISTS warehouse_wiki_quality;

-- Fresh-schema DDL is not an upgrader. On an existing v1/partial parent it
-- refuses BEFORE CREATE TABLE IF NOT EXISTS could produce a half-v2 sidecar.
DO $fresh$
DECLARE parent_oid oid := to_regclass('warehouse_wiki_quality.ods_event');
BEGIN
  IF parent_oid IS NOT NULL AND (
    to_regclass('warehouse_wiki_quality.ods_fact_set') IS NULL OR
    NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=parent_oid
      AND attname='fact_set_payload_jcs_sha256' AND attnum>0 AND NOT attisdropped) OR
    NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=parent_oid
      AND conname='ods_event_status_v2_check' AND contype='c' AND convalidated) OR
    NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=parent_oid
      AND conname='ods_event_evidence_v2_check' AND contype='c' AND convalidated) OR
    NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=parent_oid
      AND tgname='ods_event_fact_set_sidecar_required' AND NOT tgisinternal AND tgenabled='O')
  ) THEN
    RAISE EXCEPTION 'existing Wiki quality ODS requires explicit FactSet migration before fresh DDL';
  END IF;
END $fresh$;

CREATE TABLE IF NOT EXISTS warehouse_wiki_quality.consumer_cursor (
  consumer text PRIMARY KEY,
  producer text NOT NULL,
  committed_offset bigint NOT NULL DEFAULT 0 CHECK (committed_offset >= 0)
);

-- The shared RTW producer is continuous. A quality judgment is only recorded
-- after verifying RTW's immutable original bytes and its frozen rubric.
CREATE TABLE IF NOT EXISTS warehouse_wiki_quality.ods_event (
  producer text NOT NULL,
  source_offset bigint NOT NULL CHECK (source_offset > 0),
  event_id text NOT NULL,
  event_type text NOT NULL,
  event_spec bytea NOT NULL,
  dc_input_hash text NOT NULL,
  dc_receipt bytea NOT NULL,
  dc_received_at timestamptz NOT NULL,
  status text NOT NULL,
  authority_event_json bytea,
  authority_event_sha256 text,
  fact_set_payload_jcs_sha256 text,
  PRIMARY KEY (producer, source_offset),
  UNIQUE (producer, event_id),
  CONSTRAINT ods_event_status_v2_check CHECK (status IN
    ('quality_verified', 'technical_skip', 'fact_set_verified')),
  CONSTRAINT ods_event_evidence_v2_check CHECK (
    (status = 'technical_skip' AND authority_event_json IS NULL
      AND authority_event_sha256 IS NULL AND fact_set_payload_jcs_sha256 IS NULL)
    OR (status = 'quality_verified' AND authority_event_json IS NOT NULL
      AND authority_event_sha256 IS NOT NULL
      AND authority_event_sha256 ~ '^[a-f0-9]{64}$'
      AND fact_set_payload_jcs_sha256 IS NULL)
    OR (status = 'fact_set_verified' AND authority_event_json IS NOT NULL
      AND authority_event_sha256 IS NOT NULL
      AND authority_event_sha256 ~ '^[a-f0-9]{64}$'
      AND fact_set_payload_jcs_sha256 IS NOT NULL
      AND fact_set_payload_jcs_sha256 ~ '^[a-f0-9]{64}$'))
);

-- ODS source events are append-only. Replay checks the frozen rows and may
-- advance the cursor; it cannot rewrite the RTW/DC original evidence.
CREATE OR REPLACE FUNCTION warehouse_wiki_quality.reject_ods_rewrite() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'Wiki quality ODS source row is immutable';
END $$;
DROP TRIGGER IF EXISTS wiki_quality_ods_immutable ON warehouse_wiki_quality.ods_event;
CREATE TRIGGER wiki_quality_ods_immutable BEFORE UPDATE OR DELETE ON warehouse_wiki_quality.ods_event
FOR EACH ROW EXECUTE FUNCTION warehouse_wiki_quality.reject_ods_rewrite();

-- The FactSet payload and its revision identity are an append-only sidecar,
-- attached to exactly one verified event at its real continuous DC position.
CREATE TABLE IF NOT EXISTS warehouse_wiki_quality.ods_fact_set (
  producer text NOT NULL,
  source_offset bigint NOT NULL CONSTRAINT ods_fact_set_offset_check CHECK (source_offset > 0),
  fact_set_id text NOT NULL,
  revision_id text NOT NULL,
  revision bigint NOT NULL CONSTRAINT ods_fact_set_revision_check CHECK (revision > 0),
  base_revision_id text NOT NULL,
  wiki_revision_id text NOT NULL,
  source_scope_revision text NOT NULL
    CONSTRAINT ods_fact_set_scope_check CHECK (source_scope_revision ~ '^scope_[a-f0-9]{64}$'),
  payload_jcs bytea NOT NULL CONSTRAINT ods_fact_set_payload_nonempty_check CHECK (octet_length(payload_jcs) > 0),
  payload_jcs_sha256 text NOT NULL
    CONSTRAINT ods_fact_set_payload_sha_check CHECK (payload_jcs_sha256 ~ '^[a-f0-9]{64}$'),
  PRIMARY KEY (producer, source_offset),
  UNIQUE (producer, revision_id),
  UNIQUE (producer, fact_set_id, revision),
  CONSTRAINT ods_fact_set_identity_check CHECK (
    producer <> '' AND fact_set_id <> '' AND revision_id <> '' AND wiki_revision_id <> ''),
  CONSTRAINT ods_fact_set_event_fk FOREIGN KEY (producer, source_offset)
    REFERENCES warehouse_wiki_quality.ods_event (producer, source_offset) ON DELETE RESTRICT
);
DROP TRIGGER IF EXISTS wiki_fact_set_ods_immutable ON warehouse_wiki_quality.ods_fact_set;
CREATE TRIGGER wiki_fact_set_ods_immutable BEFORE UPDATE OR DELETE ON warehouse_wiki_quality.ods_fact_set
FOR EACH ROW EXECUTE FUNCTION warehouse_wiki_quality.reject_ods_rewrite();

CREATE OR REPLACE FUNCTION warehouse_wiki_quality.check_fact_set_parent() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE parent_status text; parent_payload_sha text;
BEGIN
  SELECT status, fact_set_payload_jcs_sha256 INTO parent_status, parent_payload_sha
    FROM warehouse_wiki_quality.ods_event
    WHERE producer=NEW.producer AND source_offset=NEW.source_offset FOR SHARE;
  IF parent_status IS DISTINCT FROM 'fact_set_verified'
     OR parent_payload_sha IS DISTINCT FROM NEW.payload_jcs_sha256 THEN
    RAISE EXCEPTION 'FactSet sidecar differs from verified ODS event';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ods_fact_set_parent_check ON warehouse_wiki_quality.ods_fact_set;
CREATE TRIGGER ods_fact_set_parent_check BEFORE INSERT ON warehouse_wiki_quality.ods_fact_set
FOR EACH ROW EXECUTE FUNCTION warehouse_wiki_quality.check_fact_set_parent();

CREATE OR REPLACE FUNCTION warehouse_wiki_quality.require_fact_set_sidecar() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.status='fact_set_verified' AND NOT EXISTS (
    SELECT 1 FROM warehouse_wiki_quality.ods_fact_set
    WHERE producer=NEW.producer AND source_offset=NEW.source_offset
  ) THEN
    RAISE EXCEPTION 'Verified FactSet event lacks append-only sidecar';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ods_event_fact_set_sidecar_required ON warehouse_wiki_quality.ods_event;
CREATE CONSTRAINT TRIGGER ods_event_fact_set_sidecar_required
AFTER INSERT ON warehouse_wiki_quality.ods_event DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION warehouse_wiki_quality.require_fact_set_sidecar();
