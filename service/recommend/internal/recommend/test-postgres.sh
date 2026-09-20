#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
recommend_pg_bin="${RECOMMEND_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$recommend_pg_bin/initdb"
recommend_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-recommend-acceptance.XXXXXX")"
recommend_started=false
finish() {
  if "$recommend_started"; then "$recommend_pg_bin/pg_ctl" -D "$recommend_tmp/data" -m immediate -w stop >/dev/null; fi
  if [[ "${RECOMMEND_KEEP_EVIDENCE:-0}" == 1 ]]; then
    echo "Evidence directory: $recommend_tmp"
  else
    rm -rf -- "$recommend_tmp"
  fi
}
trap finish EXIT
recommend_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$recommend_pg_bin/initdb" -D "$recommend_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$recommend_tmp/initdb.log"
"$recommend_pg_bin/pg_ctl" -D "$recommend_tmp/data" -l "$recommend_tmp/postgres.log" -o "-h 127.0.0.1 -p $recommend_port -k $recommend_tmp" -w start >/dev/null
recommend_started=true
export RECOMMEND_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$recommend_port/postgres?sslmode=disable"
go test -mod=readonly -race -count=1 -v ./internal/recommend ./migrations/recommend ./internal/app | tee "$recommend_tmp/go-test.log"
go vet ./internal/recommend ./migrations/recommend ./internal/app
