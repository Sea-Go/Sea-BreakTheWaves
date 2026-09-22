-- Explicit, reentrant upgrade of the independent Wiki quality ODS. Apply
-- only after checking the old v1 cursor/source rows; never backfill a Catalog
-- from a single judgment Event or change old DC offsets and receipts.
BEGIN;

DO $migration$
DECLARE
  parent_oid oid := to_regclass('warehouse_wiki_quality.ods_event');
  sidecar_oid oid := to_regclass('warehouse_wiki_quality.ods_fact_set');
  payload_col boolean;
  old_status text;
  old_evidence text;
  new_status text;
  new_evidence text;
BEGIN
  IF parent_oid IS NULL OR to_regclass('warehouse_wiki_quality.consumer_cursor') IS NULL THEN
    RAISE EXCEPTION 'Wiki quality v1 ODS and cursor must exist before FactSet migration';
  END IF;
  SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=parent_oid
    AND attname='fact_set_payload_jcs_sha256' AND attnum>0 AND NOT attisdropped)
    INTO payload_col;
  SELECT pg_get_constraintdef(oid) INTO old_status FROM pg_constraint
    WHERE conrelid=parent_oid AND conname='ods_event_status_check' AND contype='c' AND convalidated;
  SELECT pg_get_constraintdef(oid) INTO old_evidence FROM pg_constraint
    WHERE conrelid=parent_oid AND conname='ods_event_check' AND contype='c' AND convalidated;
  SELECT pg_get_constraintdef(oid) INTO new_status FROM pg_constraint
    WHERE conrelid=parent_oid AND conname='ods_event_status_v2_check' AND contype='c' AND convalidated;
  SELECT pg_get_constraintdef(oid) INTO new_evidence FROM pg_constraint
    WHERE conrelid=parent_oid AND conname='ods_event_evidence_v2_check' AND contype='c' AND convalidated;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=parent_oid
    AND tgname='wiki_quality_ods_immutable' AND NOT tgisinternal AND tgenabled='O') THEN
    RAISE EXCEPTION 'old Wiki quality append-only trigger is missing';
  END IF;

  IF payload_col THEN
    IF sidecar_oid IS NULL OR old_status IS NOT NULL OR old_evidence IS NOT NULL OR
      new_status IS NULL OR new_evidence IS NULL OR
      position('fact_set_verified' in new_status)=0 OR
      position('quality_verified' in new_status)=0 OR
      position('technical_skip' in new_status)=0 OR
      position('fact_set_payload_jcs_sha256' in new_evidence)=0 OR
      position('authority_event_sha256' in new_evidence)=0 OR
      position('^[a-f0-9]{64}$' in new_evidence)=0 OR
      NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=sidecar_oid
        AND conname='ods_fact_set_event_fk' AND contype='f'
        AND confrelid=parent_oid AND convalidated) OR
      NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=sidecar_oid
        AND conname='ods_fact_set_pkey' AND contype='p' AND convalidated) OR
      NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=sidecar_oid
        AND tgname='wiki_fact_set_ods_immutable' AND NOT tgisinternal AND tgenabled='O') OR
      NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=sidecar_oid
        AND tgname='ods_fact_set_parent_check' AND NOT tgisinternal AND tgenabled='O') OR
      NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=parent_oid
        AND tgname='ods_event_fact_set_sidecar_required' AND NOT tgisinternal
        AND tgenabled='O' AND tgdeferrable AND tginitdeferred) THEN
      RAISE EXCEPTION 'partial or weak Wiki FactSet ODS v2 schema cannot be repaired implicitly';
    END IF;
    RETURN; -- Fully upgraded: repeated execution preserves old rows/watermark.
  END IF;

  IF sidecar_oid IS NOT NULL OR new_status IS NOT NULL OR new_evidence IS NOT NULL OR
    old_status IS NULL OR old_evidence IS NULL OR
    position('quality_verified' in old_status)=0 OR
    position('technical_skip' in old_status)=0 OR
    position('fact_set_verified' in old_status)>0 OR
    position('authority_event_json' in old_evidence)=0 OR
    position('authority_event_sha256' in old_evidence)=0 OR
    position('fact_set_payload_jcs_sha256' in old_evidence)>0 THEN
    RAISE EXCEPTION 'Wiki quality ODS is neither complete v1 nor complete v2';
  END IF;

  ALTER TABLE warehouse_wiki_quality.ods_event
    ADD COLUMN fact_set_payload_jcs_sha256 text;
  ALTER TABLE warehouse_wiki_quality.ods_event
    DROP CONSTRAINT ods_event_status_check,
    DROP CONSTRAINT ods_event_check;
  ALTER TABLE warehouse_wiki_quality.ods_event
    ADD CONSTRAINT ods_event_status_v2_check CHECK (status IN
      ('quality_verified', 'technical_skip', 'fact_set_verified')),
    ADD CONSTRAINT ods_event_evidence_v2_check CHECK (
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
        AND fact_set_payload_jcs_sha256 ~ '^[a-f0-9]{64}$'));
END $migration$;

-- Only the first complete v1→v2 transition creates the sidecar and triggers.
-- A second call keeps trigger OIDs, old bytes, rows and committed watermark.
DO $create$
BEGIN
IF to_regclass('warehouse_wiki_quality.ods_fact_set') IS NULL THEN
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
END IF;
END $create$;

COMMIT;
