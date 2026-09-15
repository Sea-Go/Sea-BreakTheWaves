#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
preflight_pg_bin="${USERMODEL_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$preflight_pg_bin/initdb"
preflight_tmp="$(mktemp -d /tmp/sea-srpf-test.XXXXXX)"
preflight_started=false
finish() {
  if "$preflight_started"; then
    "$preflight_pg_bin/pg_ctl" -D "$preflight_tmp/data" -m immediate -w stop >/dev/null
  fi
  if [[ "${USERMODEL_PREFLIGHT_KEEP_EVIDENCE:-0}" == 1 ]]; then
    printf 'Fixture evidence directory: %s\n' "$preflight_tmp" >&2
  else
    rm -rf -- "$preflight_tmp"
  fi
}
trap finish EXIT
preflight_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$preflight_pg_bin/initdb" -D "$preflight_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$preflight_tmp/initdb.log"
"$preflight_pg_bin/pg_ctl" -D "$preflight_tmp/data" -l "$preflight_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $preflight_port -k $preflight_tmp" -w start >/dev/null
preflight_started=true
export USERMODEL_PREFLIGHT_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$preflight_port/postgres?sslmode=disable"
mkdir "$preflight_tmp/old-src"
git archive 1150534399849ab43a2751e9be32870e8746c26f \
  go.mod go.sum cmd/usermodel-subjectref-preflight/main.go \
  internal/usermodel/preflight/audit.go internal/usermodel/preflight/catalog.go \
  internal/usermodel/preflight/contract.json | tar -x -C "$preflight_tmp/old-src"
(
  cd "$preflight_tmp/old-src"
  GOMAXPROCS=2 go build -mod=readonly -p=1 -o "$preflight_tmp/old-preflight" \
    ./cmd/usermodel-subjectref-preflight
)
export USERMODEL_PREFLIGHT_OLD_BIN="$preflight_tmp/old-preflight"
preflight_filter="${USERMODEL_PREFLIGHT_TEST_FILTER:-.}"
GOMAXPROCS=2 go test -mod=readonly -p=1 -race -count=1 -v -run "$preflight_filter" \
  ./internal/usermodel/preflight ./cmd/usermodel-subjectref-preflight
GOMAXPROCS=2 go vet -mod=readonly -p=1 ./internal/usermodel/preflight ./cmd/usermodel-subjectref-preflight
GOMAXPROCS=2 go build -mod=readonly -p=1 -o "$preflight_tmp/preflight" ./cmd/usermodel-subjectref-preflight
if "$preflight_tmp/preflight" >"$preflight_tmp/offline.out" 2>"$preflight_tmp/offline.log"; then
  printf 'default-off CLI unexpectedly connected\n' >&2
  exit 1
fi
preflight_files=(
  migrations/usermodel/001_facts.sql
  migrations/usermodel/002_coverage_verification.sql
  migrations/usermodel/002_ontology.sql
  migrations/usermodel/003_features.sql
  migrations/usermodel/004_serving.sql
  migrations/usermodel/005_covered_baseline.sql
  migrations/usermodel/006_covered_snapshot.sql
)
for preflight_file in "${preflight_files[@]}"; do
  "$preflight_pg_bin/psql" -X -v ON_ERROR_STOP=1 "$USERMODEL_PREFLIGHT_TEST_POSTGRES_DSN" \
    -f "$preflight_file" >"$preflight_tmp/migration.log"
done
export USERMODEL_PREFLIGHT_FIXTURE_DSN="$USERMODEL_PREFLIGHT_TEST_POSTGRES_DSN"
"$preflight_tmp/preflight" --run --dsn-env USERMODEL_PREFLIGHT_FIXTURE_DSN \
  >"$preflight_tmp/report.json" 2>"$preflight_tmp/cli.log"
python3 - "$preflight_tmp/report.json" "$preflight_tmp/cli.log" "$preflight_tmp/offline.log" <<'PY'
import hashlib
import json
import pathlib
import sys

report_bytes = pathlib.Path(sys.argv[1]).read_bytes()
report = json.loads(report_bytes)
assert report['transaction'] == 'repeatable_read/read_only'
assert report['l1'] == report['l2'] == report['l3'] == 0
assert report['row_audit_complete'] and len(report['table_rows']) == 23 and report['total_rows'] == 0
for path in sys.argv[2:]:
    rows = [json.loads(line) for line in pathlib.Path(path).read_text().splitlines()]
    assert rows and all(row['service'] == 'btw-subjectref-preflight' for row in rows)
print('isolated CLI contentSHA=' + report['report_sha256'] +
      ' stdoutFileSHA=' + hashlib.sha256(report_bytes).hexdigest() + ' L1=0 L2=0 L3=0')
PY
