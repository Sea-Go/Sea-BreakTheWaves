#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
covered_pg_bin="${COVERED_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
covered_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-h10-covered.XXXXXX")"
covered_started=false
finish() {
  if "$covered_started"; then
    "$covered_pg_bin/pg_ctl" -D "$covered_tmp/pg" -m fast -w stop >"$covered_tmp/pg-stop.log" 2>&1
  fi
  printf 'Evidence directory: %s\n' "$covered_tmp"
}
trap finish EXIT
covered_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$covered_pg_bin/initdb" -D "$covered_tmp/pg" --no-locale --encoding=UTF8 --auth=trust >"$covered_tmp/initdb.log"
"$covered_pg_bin/pg_ctl" -D "$covered_tmp/pg" -l "$covered_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $covered_port -k $covered_tmp" -w start >"$covered_tmp/pg-start.log"
covered_started=true
export USERMODEL_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$covered_port/postgres?sslmode=disable"
GOFLAGS=-p=2 GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
  -run '^TestBuildCoveredUsesVerifiedPostgresHistoricalState$' ./internal/warehouse/featurebaseline \
  2>&1 | tee "$covered_tmp/go-test.log"
