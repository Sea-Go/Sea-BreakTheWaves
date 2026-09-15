#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../../.." && pwd)"
if [[ -z "${SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE:-}" ||
      -z "${SEA_BTW_SOURCEPROOF_REAL_FIXTURE:-}" ]]; then
  printf 'RTW old eleven-key and SourceProof twelve-key fixtures are both required.\n' >&2
  exit 2
fi
pg_bin="$(dirname "$(command -v initdb)")"
evidence="$(mktemp -d "${TMPDIR:-/tmp}/sea-wiki-sourceproof-real.XXXXXX")"
pg_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
started=0
cleanup() {
  local outcome=$?
  trap - EXIT
  if [[ "$started" == 1 ]]; then
    if ! "$pg_bin/pg_ctl" -D "$evidence/pg" -m fast stop >"$evidence/stop.log" 2>&1; then
      outcome=1
    fi
    if "$pg_bin/pg_ctl" -D "$evidence/pg" status >"$evidence/stop-status.log" 2>&1; then
      outcome=1 # a still-running server must never qualify a green test
    else
      local status_code=$?
      if [[ "$status_code" != 3 ]]; then
        outcome=1
      fi
    fi
  fi
  printf 'Evidence directory: %s\n' "$evidence"
  exit "$outcome"
}
trap cleanup EXIT

"$pg_bin/initdb" -D "$evidence/pg" -A trust --no-locale -U sea_wiki_sourceproof_test >"$evidence/initdb.log"
started=1 # pg_ctl start can report failure after a postmaster was launched
"$pg_bin/pg_ctl" -D "$evidence/pg" -l "$evidence/postgres.log" \
  -o "-h 127.0.0.1 -p $pg_port -k $evidence" start >"$evidence/start.log"

export WIKI_QUALITY_SOURCE_TEST_DSN="postgres://sea_wiki_sourceproof_test@127.0.0.1:$pg_port/postgres?sslmode=disable"
cd "$repo_root"
GOFLAGS='-mod=readonly -p=2' GOMAXPROCS=2 \
  go test -race -count=1 -v -run '^TestRTWRealWikiFactSetSource$' \
  ./internal/warehouse/wikiqualitysource >"$evidence/warehouse-source-test.log" 2>&1
GOFLAGS='-mod=readonly -p=2' GOMAXPROCS=2 \
  go test -race -count=1 -v -run '^TestRTWRealFactSetAcknowledgedSource$' \
  ./internal/evaluation/wiki_quality/sourceproof >"$evidence/sourceproof-reader-test.log" 2>&1
GOFLAGS='-mod=readonly -p=2' GOMAXPROCS=2 \
  go vet ./internal/warehouse/wikiqualitysource \
    ./internal/evaluation/wiki_quality/sourceproof >"$evidence/go-vet.log" 2>&1
printf 'RTW original/explicit history, real DC ACK-prefix and same-PG BTW ODS source projection passed.\n'
