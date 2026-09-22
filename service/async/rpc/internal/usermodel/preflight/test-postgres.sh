#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../../../../.."
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
preflight_filter="${USERMODEL_PREFLIGHT_TEST_FILTER:-.}"
GOMAXPROCS=2 go test -mod=readonly -p=1 -race -count=1 -v -run "$preflight_filter" \
  ./service/async/rpc/internal/usermodel/preflight ./service/async/rpc/cmd/usermodel-subjectref-preflight
GOMAXPROCS=2 go vet -mod=readonly -p=1 ./service/async/rpc/internal/usermodel/preflight \
  ./service/async/rpc/cmd/usermodel-subjectref-preflight ./service/async/rpc/cmd/usermodel-schema
GOMAXPROCS=2 go build -mod=readonly -p=1 -o "$preflight_tmp/preflight" \
  ./service/async/rpc/cmd/usermodel-subjectref-preflight
GOMAXPROCS=2 go build -mod=readonly -p=1 -o "$preflight_tmp/schema" \
  ./service/async/rpc/cmd/usermodel-schema
if "$preflight_tmp/preflight" >"$preflight_tmp/offline.out" 2>"$preflight_tmp/offline.log"; then
  printf 'default-off CLI unexpectedly connected\n' >&2
  exit 1
fi
"$preflight_tmp/schema" --run --dsn-env USERMODEL_PREFLIGHT_TEST_POSTGRES_DSN --schema public \
  >"$preflight_tmp/schema.log" 2>&1
"$preflight_tmp/preflight" --run --dsn-env USERMODEL_PREFLIGHT_TEST_POSTGRES_DSN --schema public \
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
