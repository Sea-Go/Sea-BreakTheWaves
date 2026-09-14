#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

fact_pg_bin="${FACT_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$fact_pg_bin/initdb"
fact_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-fact-worker-acceptance.XXXXXX")"
fact_started=false
finish() {
  if "$fact_started"; then
    "$fact_pg_bin/pg_ctl" -D "$fact_tmp/data" -m immediate -w stop >/dev/null
  fi
  if [[ "${FACT_KEEP_EVIDENCE:-0}" == 1 ]]; then
    printf 'Evidence directory: %s\n' "$fact_tmp"
  else
    rm -rf -- "$fact_tmp"
  fi
}
trap finish EXIT

fact_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$fact_pg_bin/initdb" -D "$fact_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$fact_tmp/initdb.log"
"$fact_pg_bin/pg_ctl" -D "$fact_tmp/data" -l "$fact_tmp/postgres.log" -o "-h 127.0.0.1 -p $fact_port -k $fact_tmp" -w start >/dev/null
fact_started=true
export USERMODEL_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$fact_port/postgres?sslmode=disable"
go test -mod=readonly -race -count=1 -v -run '^TestFactWorker' ./internal/app | tee "$fact_tmp/go-test.log"
go vet ./internal/app
