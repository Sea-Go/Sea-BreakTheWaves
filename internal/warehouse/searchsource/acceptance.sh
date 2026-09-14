#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
pg_bin="$(dirname "$(command -v initdb)")"
evidence="$(mktemp -d "${TMPDIR:-/tmp}/sea-search-qrel-source.XXXXXX")"
pg_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
started=0
cleanup() {
  if [[ "$started" == 1 ]]; then
    "$pg_bin/pg_ctl" -D "$evidence/pg" -m fast stop >"$evidence/stop.log" 2>&1 || true
  fi
  printf 'Evidence directory: %s\n' "$evidence"
}
trap cleanup EXIT

"$pg_bin/initdb" -D "$evidence/pg" -A trust --no-locale -U sea_qrel_test >"$evidence/initdb.log"
"$pg_bin/pg_ctl" -D "$evidence/pg" -l "$evidence/postgres.log" \
  -o "-h 127.0.0.1 -p $pg_port -k $evidence" start >"$evidence/start.log"
started=1

export SEARCH_QREL_SOURCE_TEST_DSN="postgres://sea_qrel_test@127.0.0.1:$pg_port/postgres?sslmode=disable"
export SEARCH_QREL_SUBSTREAM_OUTPUT="$evidence/substream"
cd "$repo_root"
GOFLAGS='-mod=readonly -p=2' GOMAXPROCS=2 \
  go test -race -count=1 -v ./internal/warehouse/searchsource >"$evidence/go-test.log" 2>&1
GOFLAGS='-mod=readonly -p=2' GOMAXPROCS=2 \
  go vet ./internal/warehouse/searchsource >"$evidence/go-vet.log" 2>&1
printf 'Independent qrel ODS cursor, RTW authority and PG replay acceptance passed.\n'
