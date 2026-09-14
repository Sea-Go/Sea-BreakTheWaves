#!/usr/bin/env bash
set -euo pipefail

# Starts only disposable loopback PostgreSQL databases and in-test HTTP servers.
sea_root="$(cd "$(dirname "$0")/../.." && pwd)"
sea_pg="${SEA_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
sea_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-btw-worker-acceptance.XXXXXX")"
sea_pg_started=false
cleanup() {
  if [[ "$sea_pg_started" == true ]]; then
    "$sea_pg/pg_ctl" -D "$sea_tmp/pg" -m fast stop > "$sea_tmp/stop.log" 2>&1 || true
  fi
  printf 'Evidence directory: %s\n' "$sea_tmp"
}
trap cleanup EXIT
sea_port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
)"
"$sea_pg/initdb" -D "$sea_tmp/pg" -A trust --no-locale -U sea_btw_worker_test > "$sea_tmp/initdb.log"
"$sea_pg/pg_ctl" -D "$sea_tmp/pg" -l "$sea_tmp/postgres.log" -o "-h 127.0.0.1 -p $sea_port -k $sea_tmp" start > "$sea_tmp/start.log"
sea_pg_started=true
for sea_db in content sessions; do
  "$sea_pg/createdb" -h 127.0.0.1 -p "$sea_port" -U sea_btw_worker_test "$sea_db"
done
export BTW_WORKER_TEST_CONTENT_DSN="postgres://sea_btw_worker_test@127.0.0.1:$sea_port/content?sslmode=disable"
export BTW_WORKER_TEST_SESSION_DSN="postgres://sea_btw_worker_test@127.0.0.1:$sea_port/sessions?sslmode=disable"
cd "$sea_root"
go test -race -count=1 -v ./cmd/worker | tee "$sea_tmp/tests.log"
go vet ./cmd/worker
git diff --check
