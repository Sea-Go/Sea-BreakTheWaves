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
subjectref_filter="${USERMODEL_V2_TEST_FILTER:-^TestSubjectRefV2}"
GOMAXPROCS=2 go test -mod=readonly -p=1 -race -count=1 -v -run "$subjectref_filter" \
  ./internal/usermodel ./migrations/usermodel >"$subjectref_tmp/test.log" 2>&1
GOMAXPROCS=2 go vet -mod=readonly -p=1 ./internal/usermodel ./migrations/usermodel \
  >"$subjectref_tmp/vet.log" 2>&1
python3 - "$subjectref_tmp/test.log" "$subjectref_tmp/vet.log" <<'PY'
import hashlib
import pathlib
import sys
tests = pathlib.Path(sys.argv[1]).read_bytes()
vet = pathlib.Path(sys.argv[2]).read_bytes()
assert b'PASS\n' in tests
assert b'--- PASS: TestSubjectRefV2HistoricalProjectionAndRollback' in tests
assert b'--- PASS: TestSubjectRefV2MixedReplayConflictAndAtomicProjection' in tests
assert b'--- PASS: TestSubjectRefV2RejectsOldSlotAndNoncanonicalUIDWithoutMerge' in tests
print('PG16/race PASS testSHA=' + hashlib.sha256(tests).hexdigest() +
      ' vetSHA=' + hashlib.sha256(vet).hexdigest())
PY
