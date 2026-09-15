#!/usr/bin/env bash
set -euo pipefail

favorite_test_dir="$(cd "$(dirname "$0")" && pwd)"
favorite_pg_bin="${FAVORITE_V2_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
favorite_tmp="$(mktemp -d /private/tmp/btw-favorite-v2-ddl.XXXXXX)"
favorite_started=false
favorite_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
favorite_psql=("$favorite_pg_bin/psql" -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "$favorite_port" -U "$(id -un)")

favorite_stop() {
  if "$favorite_started"; then
    "$favorite_pg_bin/pg_ctl" -D "$favorite_tmp/data" -m immediate -w stop >"$favorite_tmp/stop.log" 2>&1
  fi
}
trap favorite_stop EXIT

"$favorite_pg_bin/initdb" -D "$favorite_tmp/data" --no-instructions >"$favorite_tmp/initdb.log" 2>&1
"$favorite_pg_bin/pg_ctl" -D "$favorite_tmp/data" -l "$favorite_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $favorite_port -k $favorite_tmp" -w start >"$favorite_tmp/start.log" 2>&1
favorite_started=true

favorite_fixture() {
  local favorite_db="$1"
  "${favorite_psql[@]}" -d postgres -c "CREATE DATABASE $favorite_db" >"$favorite_tmp/$favorite_db-create.log"
  "${favorite_psql[@]}" -d "$favorite_db" -f "$favorite_test_dir/schema.sql" >"$favorite_tmp/$favorite_db-schema.log"
  "${favorite_psql[@]}" -d "$favorite_db" -f "$favorite_test_dir/coverage_schema.sql" >>"$favorite_tmp/$favorite_db-schema.log"
}
favorite_migrate() {
  local favorite_db="$1"
  # STRUCTURAL SMOKE ONLY: a synthetic marker lets the SQL body exercise
  # catalog/PK/FK/BIGINT behavior. This fixture deliberately does not pass the
  # complete ODS/S3 preflight and cannot represent official migration admission.
  "${favorite_psql[@]}" -d "$favorite_db" --single-transaction \
    -c "SELECT set_config('warehouse_favorite.subjectref_v2_locked_preflight',repeat('a',64),true)" \
    -f "$favorite_test_dir/migrate_subjectref_v2_storage.sql"
}
favorite_must_fail() {
  local favorite_db="$1" favorite_label="$2"
  if favorite_migrate "$favorite_db" >"$favorite_tmp/$favorite_label.log" 2>&1; then
    printf 'migration unexpectedly accepted %s\n' "$favorite_label" >&2
    exit 1
  fi
  "${favorite_psql[@]}" -d "$favorite_db" -Atc \
    "SELECT CASE WHEN to_regclass('warehouse_favorite.ods_event_subject_ref_v2') IS NULL
       AND to_regclass('warehouse_favorite.coverage_subject_receipt_subject_ref_v2') IS NULL
       AND NOT EXISTS (SELECT 1 FROM pg_constraint
         WHERE conname IN ('ods_event_v2_anchor_key','coverage_subject_receipt_v2_anchor_key'))
       THEN 'rollback-ok' ELSE 'rollback-failed' END" | rg -x 'rollback-ok'
}

favorite_fixture favorite_unmarked
if "${favorite_psql[@]}" -d favorite_unmarked --single-transaction \
    -f "$favorite_test_dir/migrate_subjectref_v2_storage.sql" \
    >"$favorite_tmp/unmarked-body.log" 2>&1; then
  printf 'structural SQL body applied without locked preflight marker\n' >&2
  exit 1
fi
rg 'requires a locked verified preflight snapshot' "$favorite_tmp/unmarked-body.log"

favorite_fixture favorite_ok
"${favorite_psql[@]}" -d favorite_ok <<'SQL' >"$favorite_tmp/fixture.log"
INSERT INTO warehouse_favorite.ods_event
  (producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,
   event_spec,source_event_hash,technical_receipt,authority_id,tenant_id,subject_id,
   favorite_id,folder_id,target_type,target_id,operation,predecessor_event_id,
   event_time,available_at,dc_received_at)
VALUES
  ('rtw.favorite',1,'event-1','favorite.fact','favorite-1',1,
   '{"old":"event-1"}',repeat('a',64),'{}','rtw.identity','platform','42',
   'favorite-1','folder-1','article','target-1','assert',NULL,now(),now(),now()),
  ('rtw.favorite',2,'event-2','favorite.fact','favorite-1',2,
   '{"old":"event-2"}',repeat('b',64),'{}','rtw.identity','platform','42',
   'favorite-1','folder-1','article','target-1','retract','event-1',now(),now(),now());
INSERT INTO warehouse_favorite.coverage_publication
  (manifest_sha256,generation,producer,through_offset,event_index_sha256,batch_evidence_sha256)
VALUES (repeat('c',64),'generation-1','rtw.favorite',2,repeat('d',64),repeat('e',64));
INSERT INTO warehouse_favorite.coverage_subject_receipt
  (receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,sparse_index_sha256,event_count)
VALUES (repeat('f',64),repeat('c',64),'rtw.identity','platform','42',repeat('0',64),2);
SQL

favorite_source_bytes() {
  local favorite_db="$1"
  "${favorite_psql[@]}" -d "$favorite_db" -Atc \
    "SELECT md5(coalesce((SELECT string_agg(row_to_json(e)::text, ',' ORDER BY producer,source_offset)
       FROM warehouse_favorite.ods_event e),'')) || ':' ||
     md5(coalesce((SELECT string_agg(row_to_json(r)::text, ',' ORDER BY receipt_sha256)
       FROM warehouse_favorite.coverage_subject_receipt r),'')) || ':' ||
     md5(coalesce((SELECT string_agg(row_to_json(p)::text, ',' ORDER BY manifest_sha256)
       FROM warehouse_favorite.coverage_publication p),''))"
}
favorite_source_bytes favorite_ok >"$favorite_tmp/original-source-before.sha"
favorite_migrate favorite_ok >"$favorite_tmp/first-migration.log"
favorite_migrate favorite_ok >"$favorite_tmp/replay-migration.log"
favorite_source_bytes favorite_ok >"$favorite_tmp/original-source-after.sha"
cmp "$favorite_tmp/original-source-before.sha" "$favorite_tmp/original-source-after.sha"
"${favorite_psql[@]}" -d favorite_ok -At <<'SQL' >"$favorite_tmp/catalog-and-reconciliation.log"
SELECT 'ods_sidecar_rows=' || count(*) FROM warehouse_favorite.ods_event_subject_ref_v2;
SELECT 'receipt_sidecar_rows=' || count(*)
  FROM warehouse_favorite.coverage_subject_receipt_subject_ref_v2;
SELECT 'validated_fks=' || count(*) FROM pg_constraint
  WHERE conname IN ('ods_event_subject_ref_v2_source_fkey',
    'coverage_subject_receipt_subject_ref_v2_source_fkey') AND convalidated;
SELECT 'ods_projected_uid=' || string_agg(DISTINCT subject_uid::text, ',')
  FROM warehouse_favorite.ods_event_subject_ref_v2;
SELECT 'source_ods_bytes=' || md5(string_agg(event_spec::text || source_event_hash ||
  technical_receipt::text,',' ORDER BY source_offset)) FROM warehouse_favorite.ods_event;
SELECT c.conname || '=' || pg_get_constraintdef(c.oid) FROM pg_constraint c
  WHERE c.conrelid IN ('warehouse_favorite.ods_event_subject_ref_v2'::regclass,
    'warehouse_favorite.coverage_subject_receipt_subject_ref_v2'::regclass)
  ORDER BY c.conname;
SQL
rg -x 'ods_sidecar_rows=2' "$favorite_tmp/catalog-and-reconciliation.log"
rg -x 'receipt_sidecar_rows=1' "$favorite_tmp/catalog-and-reconciliation.log"
rg -x 'validated_fks=2' "$favorite_tmp/catalog-and-reconciliation.log"
rg -x 'ods_projected_uid=42' "$favorite_tmp/catalog-and-reconciliation.log"

"${favorite_psql[@]}" -d postgres -c 'CREATE DATABASE favorite_fakecheck TEMPLATE favorite_ok' \
  >"$favorite_tmp/fakecheck-create.log"
"${favorite_psql[@]}" -d favorite_fakecheck <<'SQL' >"$favorite_tmp/fakecheck-fixture.log"
ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
  DROP CONSTRAINT ods_event_subject_ref_v2_projection_check;
ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
  ADD CONSTRAINT ods_event_subject_ref_v2_projection_check CHECK (issuer = 'rtw.identity');
SQL
if favorite_migrate favorite_fakecheck >"$favorite_tmp/fakecheck-replay.log" 2>&1; then
  printf 'fake same-name CHECK unexpectedly accepted\n' >&2
  exit 1
fi
rg 'weak same-name replay: ods_event_subject_ref_v2_projection_check differs' \
  "$favorite_tmp/fakecheck-replay.log"

"${favorite_psql[@]}" -d postgres -c 'CREATE DATABASE favorite_fakefk TEMPLATE favorite_ok' \
  >"$favorite_tmp/fakefk-create.log"
"${favorite_psql[@]}" -d favorite_fakefk <<'SQL' >"$favorite_tmp/fakefk-fixture.log"
ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
  DROP CONSTRAINT ods_event_subject_ref_v2_source_fkey;
ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
  ADD CONSTRAINT ods_event_subject_ref_v2_source_fkey
    FOREIGN KEY (producer,source_offset)
    REFERENCES warehouse_favorite.ods_event(producer,source_offset);
SQL
if favorite_migrate favorite_fakefk >"$favorite_tmp/fakefk-replay.log" 2>&1; then
  printf 'fake same-name FK unexpectedly accepted\n' >&2
  exit 1
fi
rg 'weak same-name replay: ods_event_subject_ref_v2_source_fkey differs' \
  "$favorite_tmp/fakefk-replay.log"

"${favorite_psql[@]}" -d postgres -c 'CREATE DATABASE favorite_wronganchor TEMPLATE favorite_ok' \
  >"$favorite_tmp/wronganchor-create.log"
"${favorite_psql[@]}" -d favorite_wronganchor -c \
  "DELETE FROM warehouse_favorite.ods_event_subject_ref_v2 WHERE source_offset=2" \
  >"$favorite_tmp/wronganchor-delete.log"
if "${favorite_psql[@]}" -d favorite_wronganchor -c \
  "INSERT INTO warehouse_favorite.ods_event_subject_ref_v2
      (producer,source_offset,event_id,authority_id,tenant_id,subject_id,issuer,subject_uid)
    SELECT producer,source_offset,'changed-event-id',authority_id,tenant_id,subject_id,
      'rtw.identity',subject_id::bigint FROM warehouse_favorite.ods_event WHERE source_offset=2" \
  >"$favorite_tmp/wrong-event-id.log" 2>&1; then
  printf 'wrong event_id sidecar unexpectedly accepted\n' >&2
  exit 1
fi
rg 'violates foreign key constraint "ods_event_subject_ref_v2_source_fkey"' \
  "$favorite_tmp/wrong-event-id.log"
"${favorite_psql[@]}" -d favorite_wronganchor -c \
  'DELETE FROM warehouse_favorite.coverage_subject_receipt_subject_ref_v2' \
  >"$favorite_tmp/wrongreceipt-delete.log"
if "${favorite_psql[@]}" -d favorite_wronganchor -c \
  "INSERT INTO warehouse_favorite.coverage_subject_receipt_subject_ref_v2
      (receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,issuer,subject_uid)
    SELECT receipt_sha256,repeat('7',64),authority_id,tenant_id,subject_id,
      'rtw.identity',subject_id::bigint
      FROM warehouse_favorite.coverage_subject_receipt" \
  >"$favorite_tmp/wrong-manifest.log" 2>&1; then
  printf 'wrong manifest sidecar unexpectedly accepted\n' >&2
  exit 1
fi
rg 'violates foreign key constraint "coverage_subject_receipt_subject_ref_v2_source_fkey"' \
  "$favorite_tmp/wrong-manifest.log"

favorite_fixture favorite_badslot
"${favorite_psql[@]}" -d favorite_badslot <<'SQL' >"$favorite_tmp/badslot-fixture.log"
INSERT INTO warehouse_favorite.ods_event
  (producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,
   event_spec,source_event_hash,technical_receipt,authority_id,tenant_id,subject_id,
   favorite_id,folder_id,target_type,target_id,operation,event_time,available_at,dc_received_at)
VALUES ('rtw.favorite',1,'bad-event','favorite.fact','bad-favorite',1,'{}',repeat('a',64),
  '{}','rtw.identity','platform','042','bad-favorite','bad-folder','article','bad-target',
  'assert',now(),now(),now());
SQL
favorite_must_fail favorite_badslot bad-slot

favorite_fixture favorite_overflow
"${favorite_psql[@]}" -d favorite_overflow <<'SQL' >"$favorite_tmp/overflow-fixture.log"
INSERT INTO warehouse_favorite.ods_event
  (producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,
   event_spec,source_event_hash,technical_receipt,authority_id,tenant_id,subject_id,
   favorite_id,folder_id,target_type,target_id,operation,event_time,available_at,dc_received_at)
VALUES ('rtw.favorite',1,'overflow-event','favorite.fact','favorite-overflow',1,
  '{}',repeat('a',64),'{}','rtw.identity','platform','9223372036854775808',
  'favorite-overflow','folder-overflow','article','target-overflow','assert',now(),now(),now());
SQL
favorite_must_fail favorite_overflow int64-overflow

# Catalog and BIGINT STRUCTURAL smoke only: this intentionally sparse high
# offset and placeholder EventSpec/receipt cannot pass the complete existing
# continuous-prefix preflight or publish coverage. The synthetic marker above
# is intentionally not the official Go Apply owner path.
favorite_fixture favorite_highoffset
"${favorite_psql[@]}" -d favorite_highoffset <<'SQL' >"$favorite_tmp/highoffset-fixture.log"
INSERT INTO warehouse_favorite.ods_event
  (producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,
   event_spec,source_event_hash,technical_receipt,authority_id,tenant_id,subject_id,
   favorite_id,folder_id,target_type,target_id,operation,event_time,available_at,dc_received_at)
VALUES ('rtw.community.favorite',9007199254740993,'high-event','favorite.fact','high-favorite',1,
  '{}',repeat('a',64),'{}','rtw.identity','platform','9007199254740993',
  'high-favorite','high-folder','article','high-target','assert',now(),now(),now());
SQL
favorite_migrate favorite_highoffset >"$favorite_tmp/highoffset-migration.log"
"${favorite_psql[@]}" -d favorite_highoffset -Atc \
  "SELECT e.source_offset::text || '|' || v.source_offset::text || '|' ||
     e.subject_id || '|' || v.subject_uid::text
    FROM warehouse_favorite.ods_event e
    JOIN warehouse_favorite.ods_event_subject_ref_v2 v
      ON (e.producer,e.source_offset,e.event_id,e.authority_id,e.tenant_id,e.subject_id)
       = (v.producer,v.source_offset,v.event_id,v.authority_id,v.tenant_id,v.subject_id)" \
  | rg -x '9007199254740993\|9007199254740993\|9007199254740993\|9007199254740993'
"${favorite_psql[@]}" -d favorite_highoffset -Atc \
  "SELECT conname || '=' || pg_get_constraintdef(oid) FROM pg_constraint
    WHERE conrelid='warehouse_favorite.ods_event'::regclass
      AND conname IN ('ods_event_pkey','ods_event_producer_event_id_key') ORDER BY conname" \
  >"$favorite_tmp/highoffset-old-keys.log"
rg -x 'ods_event_pkey=PRIMARY KEY \(producer, source_offset\)' \
  "$favorite_tmp/highoffset-old-keys.log"
rg -x 'ods_event_producer_event_id_key=UNIQUE \(producer, event_id\)' \
  "$favorite_tmp/highoffset-old-keys.log"
if "${favorite_psql[@]}" -d favorite_highoffset -c \
  'INSERT INTO warehouse_favorite.ods_event SELECT * FROM warehouse_favorite.ods_event' \
  >"$favorite_tmp/highoffset-duplicate-pk.log" 2>&1; then
  printf 'high-offset duplicate old PK unexpectedly accepted\n' >&2
  exit 1
fi
rg 'violates unique constraint "ods_event_pkey"' "$favorite_tmp/highoffset-duplicate-pk.log"
if "${favorite_psql[@]}" -d favorite_highoffset -c \
  "INSERT INTO warehouse_favorite.ods_event
    (producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,
     event_spec,source_event_hash,technical_receipt,authority_id,tenant_id,subject_id,
     favorite_id,folder_id,target_type,target_id,operation,event_time,available_at,dc_received_at)
   VALUES ('rtw.community.favorite',9007199254740994,'high-event','favorite.fact',
     'other-favorite',2,'{}',repeat('b',64),'{}','rtw.identity','platform',
     '9007199254740993','other-favorite','high-folder','article','high-target',
     'assert',now(),now(),now())" \
  >"$favorite_tmp/highoffset-duplicate-eventid.log" 2>&1; then
  printf 'high-offset duplicate old event_id unexpectedly accepted\n' >&2
  exit 1
fi
rg 'violates unique constraint "ods_event_producer_event_id_key"' \
  "$favorite_tmp/highoffset-duplicate-eventid.log"

favorite_fixture favorite_collision
"${favorite_psql[@]}" -d favorite_collision <<'SQL' >"$favorite_tmp/collision-fixture.log"
INSERT INTO warehouse_favorite.coverage_publication
  (manifest_sha256,generation,producer,through_offset,event_index_sha256,batch_evidence_sha256)
VALUES (repeat('c',64),'generation-1','rtw.favorite',1,repeat('d',64),repeat('e',64));
INSERT INTO warehouse_favorite.coverage_subject_receipt
  (receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,sparse_index_sha256,event_count)
VALUES (repeat('a',64),repeat('c',64),'rtw.identity','platform','42',repeat('0',64),1),
  (repeat('b',64),repeat('c',64),'rtw.identity','old-platform','42',repeat('1',64),1);
SQL
favorite_must_fail favorite_collision projected-key-collision

favorite_fixture favorite_weakanchor
"${favorite_psql[@]}" -d favorite_weakanchor -c \
  'ALTER TABLE warehouse_favorite.ods_event ADD CONSTRAINT ods_event_v2_anchor_key UNIQUE (producer,event_id)' \
  >"$favorite_tmp/weakanchor-fixture.log"
if favorite_migrate favorite_weakanchor >"$favorite_tmp/weakanchor-replay.log" 2>&1; then
  printf 'weak same-name parent anchor unexpectedly accepted\n' >&2
  exit 1
fi
rg 'weak same-name replay: ods_event_v2_anchor_key differs' \
  "$favorite_tmp/weakanchor-replay.log"
"${favorite_psql[@]}" -d favorite_weakanchor -Atc \
  "SELECT CASE WHEN to_regclass('warehouse_favorite.ods_event_subject_ref_v2') IS NULL
    AND to_regclass('warehouse_favorite.coverage_subject_receipt_subject_ref_v2') IS NULL
    AND NOT EXISTS (SELECT 1 FROM pg_constraint
      WHERE conname='coverage_subject_receipt_v2_anchor_key')
  THEN 'weak-anchor-rollback-ok' ELSE 'weak-anchor-rollback-failed' END" \
  | rg -x 'weak-anchor-rollback-ok'

favorite_fixture favorite_weak
"${favorite_psql[@]}" -d favorite_weak -c \
  'CREATE TABLE warehouse_favorite.ods_event_subject_ref_v2 (producer text)' \
  >"$favorite_tmp/weak-fixture.log"
if favorite_migrate favorite_weak >"$favorite_tmp/weak-same-name.log" 2>&1; then
  printf 'weak same-name table unexpectedly accepted\n' >&2
  exit 1
fi
"${favorite_psql[@]}" -d favorite_weak -Atc \
  "SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM pg_constraint
    WHERE conname IN ('ods_event_v2_anchor_key','coverage_subject_receipt_v2_anchor_key'))
  THEN 'weak-rollback-ok' ELSE 'weak-rollback-failed' END" | rg -x 'weak-rollback-ok'

printf 'PASS favorite SubjectRef v2 PG16 DDL; evidence=%s\n' "$favorite_tmp"
