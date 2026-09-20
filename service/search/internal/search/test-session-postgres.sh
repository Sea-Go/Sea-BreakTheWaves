#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

session_pg_bin="${SEARCH_SESSION_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$session_pg_bin/initdb"
session_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-search-session-acceptance.XXXXXX")"
session_started=false
finish() {
  if "$session_started"; then
    "$session_pg_bin/pg_ctl" -D "$session_tmp/data" -m immediate -w stop >/dev/null
  fi
  if [[ "${SEARCH_SESSION_KEEP_EVIDENCE:-0}" == 1 ]]; then
    printf 'Evidence directory: %s\n' "$session_tmp"
  else
    rm -rf -- "$session_tmp"
  fi
}
trap finish EXIT

session_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$session_pg_bin/initdb" -D "$session_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$session_tmp/initdb.log"
"$session_pg_bin/pg_ctl" -D "$session_tmp/data" -l "$session_tmp/postgres.log" -o "-h 127.0.0.1 -p $session_port -k $session_tmp" -w start >/dev/null
session_started=true
export SEARCH_SESSION_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$session_port/postgres?sslmode=disable"
go test -mod=readonly -race -count=1 -v -run '^TestRootSessionReadBoundary$' ./internal/search | tee "$session_tmp/go-test.log"
go vet ./internal/search
