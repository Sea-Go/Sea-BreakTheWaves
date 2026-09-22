-- Additive transaction body. Official application is through the Go
-- ApplySubjectRefV2StorageCandidate owner entry: it locks all five old PG
-- tables, verifies the exact same RR snapshot and S3 roots, then executes
-- this body before the same transaction commits. Direct psql with a fabricated
-- marker is only a structural smoke test, never migration admission.
DO $preflight_gate$
BEGIN
  IF coalesce(current_setting('warehouse_favorite.subjectref_v2_locked_preflight', true), '')
       !~ '^[a-f0-9]{64}$' THEN
    RAISE EXCEPTION 'favorite v2 DDL requires a locked verified preflight snapshot';
  END IF;
END
$preflight_gate$;
SET LOCAL lock_timeout = '10s';
SET LOCAL statement_timeout = '5min';
LOCK TABLE warehouse_favorite.ods_event,
  warehouse_favorite.coverage_subject_receipt IN SHARE ROW EXCLUSIVE MODE;

DO $guard$
DECLARE
  actual text;
  expected text;
  table_name text;
  constraint_name text;
  constraint_definition text;
  actual_validated boolean;
BEGIN
  -- A similarly named old table/constraint must not silently pass CREATE IF NOT EXISTS.
  FOREACH table_name IN ARRAY ARRAY['ods_event', 'coverage_subject_receipt'] LOOP
    IF to_regclass('warehouse_favorite.' || table_name) IS NULL THEN
      RAISE EXCEPTION 'missing original warehouse_favorite.%', table_name;
    END IF;
  END LOOP;
  FOR table_name, constraint_name, constraint_definition IN
    SELECT * FROM (VALUES
      ('ods_event', 'ods_event_pkey', 'PRIMARY KEY (producer, source_offset)'),
      ('ods_event', 'ods_event_producer_event_id_key', 'UNIQUE (producer, event_id)'),
      ('coverage_subject_receipt', 'coverage_subject_receipt_pkey',
        'PRIMARY KEY (receipt_sha256)'),
      ('coverage_subject_receipt', 'coverage_subject_receipt_manifest_sha256_authority_id_tenan_key',
        'UNIQUE (manifest_sha256, authority_id, tenant_id, subject_id)')) AS required(table_name, name, definition)
  LOOP
    SELECT pg_get_constraintdef(c.oid) INTO actual
      FROM pg_constraint c WHERE c.conrelid = ('warehouse_favorite.' || table_name)::regclass
        AND c.conname = constraint_name;
    IF actual IS DISTINCT FROM constraint_definition THEN
      RAISE EXCEPTION 'old constraint % has missing or unexpected definition', constraint_name;
    END IF;
  END LOOP;

  -- An existing sidecar is only a replay target when its complete column and
  -- constraint shape matches the previous successful transaction.
  FOR table_name, expected IN
    SELECT * FROM (VALUES
      ('ods_event_subject_ref_v2',
       'producer:text:t,source_offset:bigint:t,event_id:text:t,authority_id:text:t,tenant_id:text:t,subject_id:text:t,issuer:text:t,subject_uid:bigint:t'),
      ('coverage_subject_receipt_subject_ref_v2',
       'receipt_sha256:character(64):t,manifest_sha256:character(64):t,authority_id:text:t,tenant_id:text:t,subject_id:text:t,issuer:text:t,subject_uid:bigint:t')) AS sidecars(name, columns)
  LOOP
    IF to_regclass('warehouse_favorite.' || table_name) IS NULL THEN
      CONTINUE;
    END IF;
    SELECT string_agg(a.attname || ':' || format_type(a.atttypid, a.atttypmod) || ':' ||
      CASE WHEN a.attnotnull THEN 't' ELSE 'f' END, ',' ORDER BY a.attnum)
      INTO actual FROM pg_attribute a
      WHERE a.attrelid = ('warehouse_favorite.' || table_name)::regclass
        AND a.attnum > 0 AND NOT a.attisdropped;
    IF actual IS DISTINCT FROM expected THEN
      RAISE EXCEPTION 'weak same-name replay: warehouse_favorite.% columns differ', table_name;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_attribute a
        WHERE a.attrelid = ('warehouse_favorite.' || table_name)::regclass
          AND a.attnum > 0 AND NOT a.attisdropped
          AND (a.atthasdef OR a.attgenerated <> '' OR a.attidentity <> '')) OR
      EXISTS (SELECT 1 FROM pg_class r WHERE r.oid = ('warehouse_favorite.' || table_name)::regclass
        AND (r.relkind <> 'r' OR r.relpersistence <> 'p')) THEN
      RAISE EXCEPTION 'weak same-name replay: warehouse_favorite.% defaults or relkind differ', table_name;
    END IF;
    IF (SELECT count(*) FROM pg_constraint c
        WHERE c.conrelid = ('warehouse_favorite.' || table_name)::regclass) <> 4 OR
      (SELECT count(*) FROM pg_index i
        WHERE i.indrelid = ('warehouse_favorite.' || table_name)::regclass) <> 2 THEN
      RAISE EXCEPTION 'weak same-name replay: warehouse_favorite.% constraints differ', table_name;
    END IF;
    FOR constraint_name, constraint_definition IN
      SELECT name, definition FROM (VALUES
        ('ods_event_subject_ref_v2', 'ods_event_subject_ref_v2_pkey',
          'PRIMARY KEY (producer, source_offset)'),
        ('ods_event_subject_ref_v2', 'ods_event_subject_ref_v2_producer_event_id_key',
          'UNIQUE (producer, event_id)'),
        ('ods_event_subject_ref_v2', 'ods_event_subject_ref_v2_projection_check',
          $def$CHECK (((issuer = 'rtw.identity'::text) AND (authority_id = issuer) AND (tenant_id = 'platform'::text) AND (subject_uid > 0) AND (subject_id = (subject_uid)::text)))$def$),
        ('ods_event_subject_ref_v2', 'ods_event_subject_ref_v2_source_fkey',
          'FOREIGN KEY (producer, source_offset, event_id, authority_id, tenant_id, subject_id) REFERENCES warehouse_favorite.ods_event(producer, source_offset, event_id, authority_id, tenant_id, subject_id)'),
        ('coverage_subject_receipt_subject_ref_v2',
          'coverage_subject_receipt_subject_ref_v2_pkey', 'PRIMARY KEY (receipt_sha256)'),
        ('coverage_subject_receipt_subject_ref_v2',
          'coverage_subject_receipt_subject_ref_v2_manifest_issuer_uid_key',
          'UNIQUE (manifest_sha256, issuer, subject_uid)'),
        ('coverage_subject_receipt_subject_ref_v2',
          'coverage_subject_receipt_subject_ref_v2_projection_check',
          $def$CHECK (((issuer = 'rtw.identity'::text) AND (authority_id = issuer) AND (tenant_id = 'platform'::text) AND (subject_uid > 0) AND (subject_id = (subject_uid)::text)))$def$),
        ('coverage_subject_receipt_subject_ref_v2',
          'coverage_subject_receipt_subject_ref_v2_source_fkey',
          'FOREIGN KEY (receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id) REFERENCES warehouse_favorite.coverage_subject_receipt(receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id)'))
        AS required(sidecar, name, definition) WHERE sidecar = table_name
    LOOP
      SELECT pg_get_constraintdef(c.oid), c.convalidated INTO actual, actual_validated
        FROM pg_constraint c
        WHERE c.conrelid = ('warehouse_favorite.' || table_name)::regclass
          AND c.conname = constraint_name;
      IF actual IS DISTINCT FROM constraint_definition OR actual_validated IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'weak same-name replay: % differs', constraint_name;
      END IF;
    END LOOP;
  END LOOP;
END
$guard$;

-- PostgreSQL requires a unique parent key covering every mirrored old value
-- before the v2 sidecar can reference the exact immutable source row.
DO $anchors$
DECLARE
  actual text;
BEGIN
  SELECT pg_get_constraintdef(c.oid) INTO actual FROM pg_constraint c
    WHERE c.conrelid = 'warehouse_favorite.ods_event'::regclass
      AND c.conname = 'ods_event_v2_anchor_key';
  IF actual IS NULL THEN
    ALTER TABLE warehouse_favorite.ods_event ADD CONSTRAINT ods_event_v2_anchor_key
      UNIQUE (producer, source_offset, event_id, authority_id, tenant_id, subject_id);
  ELSIF actual <> 'UNIQUE (producer, source_offset, event_id, authority_id, tenant_id, subject_id)' THEN
    RAISE EXCEPTION 'weak same-name replay: ods_event_v2_anchor_key differs';
  END IF;
  SELECT pg_get_constraintdef(c.oid) INTO actual FROM pg_constraint c
    WHERE c.conrelid = 'warehouse_favorite.coverage_subject_receipt'::regclass
      AND c.conname = 'coverage_subject_receipt_v2_anchor_key';
  IF actual IS NULL THEN
    ALTER TABLE warehouse_favorite.coverage_subject_receipt
      ADD CONSTRAINT coverage_subject_receipt_v2_anchor_key
      UNIQUE (receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id);
  ELSIF actual <> 'UNIQUE (receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id)' THEN
    RAISE EXCEPTION 'weak same-name replay: coverage_subject_receipt_v2_anchor_key differs';
  END IF;
END
$anchors$;

CREATE TABLE IF NOT EXISTS warehouse_favorite.ods_event_subject_ref_v2 (
  producer text NOT NULL,
  source_offset bigint NOT NULL,
  event_id text NOT NULL,
  authority_id text NOT NULL,
  tenant_id text NOT NULL,
  subject_id text NOT NULL,
  issuer text NOT NULL,
  subject_uid bigint NOT NULL,
  CONSTRAINT ods_event_subject_ref_v2_pkey PRIMARY KEY (producer, source_offset),
  CONSTRAINT ods_event_subject_ref_v2_producer_event_id_key UNIQUE (producer, event_id),
  CONSTRAINT ods_event_subject_ref_v2_projection_check CHECK (
    issuer = 'rtw.identity' AND authority_id = issuer AND tenant_id = 'platform'
    AND subject_uid > 0 AND subject_id = subject_uid::text)
);

CREATE TABLE IF NOT EXISTS warehouse_favorite.coverage_subject_receipt_subject_ref_v2 (
  receipt_sha256 char(64) NOT NULL,
  manifest_sha256 char(64) NOT NULL,
  authority_id text NOT NULL,
  tenant_id text NOT NULL,
  subject_id text NOT NULL,
  issuer text NOT NULL,
  subject_uid bigint NOT NULL,
  CONSTRAINT coverage_subject_receipt_subject_ref_v2_pkey PRIMARY KEY (receipt_sha256),
  CONSTRAINT coverage_subject_receipt_subject_ref_v2_manifest_issuer_uid_key
    UNIQUE (manifest_sha256, issuer, subject_uid),
  CONSTRAINT coverage_subject_receipt_subject_ref_v2_projection_check CHECK (
    issuer = 'rtw.identity' AND authority_id = issuer AND tenant_id = 'platform'
    AND subject_uid > 0 AND subject_id = subject_uid::text)
);

DO $foreign_keys$
DECLARE
  actual text;
BEGIN
  SELECT pg_get_constraintdef(c.oid) INTO actual FROM pg_constraint c
    WHERE c.conrelid = 'warehouse_favorite.ods_event_subject_ref_v2'::regclass
      AND c.conname = 'ods_event_subject_ref_v2_source_fkey';
  IF actual IS NULL THEN
    ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
      ADD CONSTRAINT ods_event_subject_ref_v2_source_fkey
      FOREIGN KEY (producer, source_offset, event_id, authority_id, tenant_id, subject_id)
      REFERENCES warehouse_favorite.ods_event
        (producer, source_offset, event_id, authority_id, tenant_id, subject_id) NOT VALID;
  END IF;
  SELECT pg_get_constraintdef(c.oid) INTO actual FROM pg_constraint c
    WHERE c.conrelid = 'warehouse_favorite.coverage_subject_receipt_subject_ref_v2'::regclass
      AND c.conname = 'coverage_subject_receipt_subject_ref_v2_source_fkey';
  IF actual IS NULL THEN
    ALTER TABLE warehouse_favorite.coverage_subject_receipt_subject_ref_v2
      ADD CONSTRAINT coverage_subject_receipt_subject_ref_v2_source_fkey
      FOREIGN KEY (receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id)
      REFERENCES warehouse_favorite.coverage_subject_receipt
        (receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id) NOT VALID;
  END IF;
END
$foreign_keys$;

-- Reject noncanonical old slots before any cast or insert. Older source rows
-- remain byte-for-byte unchanged; no normalization is performed in place.
DO $source_slots$
BEGIN
  IF EXISTS (
    SELECT 1 FROM (
      SELECT authority_id, tenant_id, subject_id FROM warehouse_favorite.ods_event
      UNION ALL
      SELECT authority_id, tenant_id, subject_id
        FROM warehouse_favorite.coverage_subject_receipt
    ) old_slot WHERE authority_id <> 'rtw.identity' OR tenant_id <> 'platform'
      OR subject_id !~ '^[1-9][0-9]{0,18}$'
      OR (length(subject_id) = 19 AND subject_id COLLATE "C" > '9223372036854775807')
  ) THEN
    RAISE EXCEPTION 'favorite SubjectRef v2 storage has noncanonical old slots';
  END IF;
END
$source_slots$;

INSERT INTO warehouse_favorite.ods_event_subject_ref_v2
  (producer,source_offset,event_id,authority_id,tenant_id,subject_id,issuer,subject_uid)
SELECT producer,source_offset,event_id,authority_id,tenant_id,subject_id,
  'rtw.identity',subject_id::bigint FROM warehouse_favorite.ods_event
ON CONFLICT (producer,source_offset) DO NOTHING;

INSERT INTO warehouse_favorite.coverage_subject_receipt_subject_ref_v2
  (receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,issuer,subject_uid)
SELECT receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,
  'rtw.identity',subject_id::bigint
  FROM warehouse_favorite.coverage_subject_receipt
ON CONFLICT (receipt_sha256) DO NOTHING;

-- Both directions exclude silent missing, extra or changed sidecar rows.
DO $reconcile$
BEGIN
  IF EXISTS (
    (SELECT producer,source_offset,event_id,authority_id,tenant_id,subject_id,
      'rtw.identity'::text issuer,subject_id::bigint subject_uid
      FROM warehouse_favorite.ods_event
     EXCEPT
     SELECT producer,source_offset,event_id,authority_id,tenant_id,subject_id,issuer,subject_uid
       FROM warehouse_favorite.ods_event_subject_ref_v2)
  ) OR EXISTS (
    (SELECT producer,source_offset,event_id,authority_id,tenant_id,subject_id,issuer,subject_uid
       FROM warehouse_favorite.ods_event_subject_ref_v2
     EXCEPT
     SELECT producer,source_offset,event_id,authority_id,tenant_id,subject_id,
       'rtw.identity'::text,subject_id::bigint FROM warehouse_favorite.ods_event)
  ) OR EXISTS (
    (SELECT receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,
      'rtw.identity'::text issuer,subject_id::bigint subject_uid
      FROM warehouse_favorite.coverage_subject_receipt
     EXCEPT
     SELECT receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,issuer,subject_uid
       FROM warehouse_favorite.coverage_subject_receipt_subject_ref_v2)
  ) OR EXISTS (
    (SELECT receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,issuer,subject_uid
      FROM warehouse_favorite.coverage_subject_receipt_subject_ref_v2
     EXCEPT
     SELECT receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,
       'rtw.identity'::text,subject_id::bigint FROM warehouse_favorite.coverage_subject_receipt)
  ) THEN
    RAISE EXCEPTION 'favorite SubjectRef v2 sidecar differs from immutable source';
  END IF;
END
$reconcile$;

ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
  VALIDATE CONSTRAINT ods_event_subject_ref_v2_source_fkey;
ALTER TABLE warehouse_favorite.coverage_subject_receipt_subject_ref_v2
  VALIDATE CONSTRAINT coverage_subject_receipt_subject_ref_v2_source_fkey;
