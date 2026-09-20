#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
subjectref_pg_bin="${USERMODEL_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$subjectref_pg_bin/initdb"
subjectref_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-btw-usermodel-v2-storage.XXXXXX")"
subjectref_started=false
finish() {
  if "$subjectref_started"; then
    "$subjectref_pg_bin/pg_ctl" -D "$subjectref_tmp/data" -m immediate -w stop >"$subjectref_tmp/stop.log"
  fi
  printf 'Stopped isolated PG16; evidence directory: %s\n' "$subjectref_tmp" >&2
}
trap finish EXIT
subjectref_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$subjectref_pg_bin/initdb" -D "$subjectref_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$subjectref_tmp/initdb.log"
"$subjectref_pg_bin/pg_ctl" -D "$subjectref_tmp/data" -l "$subjectref_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $subjectref_port -k $subjectref_tmp" -w start >"$subjectref_tmp/start.log"
subjectref_started=true
export USERMODEL_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$subjectref_port/postgres?sslmode=disable"
subjectref_old_migrations=(
  migrations/usermodel/001_facts.sql
  migrations/usermodel/002_coverage_verification.sql
  migrations/usermodel/002_ontology.sql
  migrations/usermodel/003_features.sql
  migrations/usermodel/004_serving.sql
  migrations/usermodel/005_covered_baseline.sql
  migrations/usermodel/006_covered_snapshot.sql
)
for subjectref_migration in "${subjectref_old_migrations[@]}"; do
  "$subjectref_pg_bin/psql" -X -v ON_ERROR_STOP=1 --single-transaction \
    "$USERMODEL_TEST_POSTGRES_DSN" -f "$subjectref_migration" \
    >>"$subjectref_tmp/old-migrations.log" 2>&1
done
GOMAXPROCS=2 go build -mod=readonly -p=1 -o "$subjectref_tmp/preflight" \
  ./cmd/usermodel-subjectref-preflight >"$subjectref_tmp/build.log" 2>&1
"$subjectref_tmp/preflight" --run --dsn-env USERMODEL_TEST_POSTGRES_DSN --schema public \
  >"$subjectref_tmp/preflight.json" 2>"$subjectref_tmp/preflight.log"
python3 - "$subjectref_tmp/preflight.json" <<'PY'
import json
import pathlib
import sys
report = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert report["l1"] == report["l2"] == 0
assert report["row_audit_complete"] and len(report["table_rows"]) == 23
assert report["total_rows"] == 0
PY
"$subjectref_pg_bin/psql" -X -v ON_ERROR_STOP=1 --single-transaction \
  "$USERMODEL_TEST_POSTGRES_DSN" -f migrations/usermodel/007_subjectref_v2_projection.sql \
  >"$subjectref_tmp/v2-migration.log" 2>&1
"$subjectref_pg_bin/psql" -X -v ON_ERROR_STOP=1 --single-transaction \
  "$USERMODEL_TEST_POSTGRES_DSN" -f migrations/usermodel/007_subjectref_v2_projection.sql \
  >"$subjectref_tmp/v2-migration-retry.log" 2>&1
subjectref_filter="${USERMODEL_V2_TEST_FILTER:-^TestSubjectRefV2}"
GOMAXPROCS=2 go test -mod=readonly -p=1 -race -count=1 -v -run "$subjectref_filter" \
  ./internal/usermodel ./migrations/usermodel >"$subjectref_tmp/test.log" 2>&1
GOMAXPROCS=2 go vet -mod=readonly -p=1 ./internal/usermodel ./migrations/usermodel \
  >"$subjectref_tmp/vet.log" 2>&1
python3 - "$subjectref_tmp/test.log" "$subjectref_tmp/vet.log" "$subjectref_tmp/preflight.json" <<'PY'
import hashlib
import pathlib
import sys
tests = pathlib.Path(sys.argv[1]).read_bytes()
vet = pathlib.Path(sys.argv[2]).read_bytes()
preflight = pathlib.Path(sys.argv[3]).read_bytes()
assert b'PASS\n' in tests
for name in (
    'StrictJSON',
    'MigrationRetryRejectsMissingGuard',
    'MigrationRetryRejectsWeakSameNamedGuard',
    'MigrationRetryRejectsMissingProjectedAtDefault',
    'HistoricalProjectionAndRollback',
    'MixedReplayConflictAndAtomicProjection',
    'ProjectAndAppendConcurrentLockOrder',
    'BindUnmappedProjectsAtomicFactAndRejectsBadSlot',
    'AttributionAndRecoveryUseTheSameProjectionGuard',
    'RejectsOldSlotAndNoncanonicalUIDWithoutMerge',
):
    assert ('--- PASS: TestSubjectRefV2' + name).encode() in tests
print('PG16/race PASS testSHA=' + hashlib.sha256(tests).hexdigest() +
      ' vetSHA=' + hashlib.sha256(vet).hexdigest() +
      ' preflightFileSHA=' + hashlib.sha256(preflight).hexdigest())
PY
