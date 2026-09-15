#!/usr/bin/env bash
set -euo pipefail

# Disposable PG16 only. Do not run this while another Holder owns the local
# capacity; the parent task schedules the single PG turn.
wiki_v2_repo="$(cd "$(dirname "$0")/../../.." && pwd)"
wiki_v2_pg_bin="${WIKI_QUALITY_PG16_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
if [[ ! -x "$wiki_v2_pg_bin/initdb" || ! -x "$wiki_v2_pg_bin/pg_ctl" ||
      "$("$wiki_v2_pg_bin/postgres" --version)" != *" 16."* ]]; then
  printf 'Wiki FactSet schema acceptance requires PostgreSQL 16 binaries\n' >&2
  exit 2
fi
wiki_v2_evidence="$(mktemp -d "${TMPDIR:-/tmp}/sea-wiki-factset-ods-v2.XXXXXX")"
wiki_v2_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
wiki_v2_started=0
wiki_v2_finish() {
  if [[ "$wiki_v2_started" == 1 ]]; then
    "$wiki_v2_pg_bin/pg_ctl" -D "$wiki_v2_evidence/pg" -m fast -w stop \
      >"$wiki_v2_evidence/pg-stop.log" 2>&1 || true
    if "$wiki_v2_pg_bin/pg_ctl" -D "$wiki_v2_evidence/pg" status \
      >"$wiki_v2_evidence/pg-status.log" 2>&1; then
      printf '0\n' >"$wiki_v2_evidence/pg-status-code"
    else
      printf '%s\n' "$?" >"$wiki_v2_evidence/pg-status-code"
    fi
  fi
  for wiki_v2_log in go-test.log go-vet.log pg-stop.log pg-status.log; do
    if [[ -f "$wiki_v2_evidence/$wiki_v2_log" ]]; then
      shasum -a 256 "$wiki_v2_evidence/$wiki_v2_log"
    fi
  done
  printf 'Wiki FactSet v2 evidence: %s\n' "$wiki_v2_evidence"
}
trap wiki_v2_finish EXIT

"$wiki_v2_pg_bin/initdb" -D "$wiki_v2_evidence/pg" --no-locale \
  --encoding=UTF8 --auth=trust -U wiki_v2_test >"$wiki_v2_evidence/initdb.log" 2>&1
"$wiki_v2_pg_bin/pg_ctl" -D "$wiki_v2_evidence/pg" \
  -l "$wiki_v2_evidence/postgres.log" \
  -o "-h 127.0.0.1 -p $wiki_v2_port -k $wiki_v2_evidence" -w start \
  >"$wiki_v2_evidence/pg-start.log" 2>&1
wiki_v2_started=1
export WIKI_QUALITY_SCHEMA_V2_TEST_DSN="postgres://wiki_v2_test@127.0.0.1:$wiki_v2_port/postgres?sslmode=disable"
export WIKI_QUALITY_SCHEMA_V2_DISPOSABLE=1
cd "$wiki_v2_repo"
GOFLAGS='-mod=readonly -p=1' GOMAXPROCS=2 \
  go test -race -count=1 -v ./internal/warehouse/wikiqualitysource \
  -run '^TestFactSetSchema' >"$wiki_v2_evidence/go-test.log" 2>&1
GOFLAGS='-mod=readonly -p=1' GOMAXPROCS=2 \
  go vet ./internal/warehouse/wikiqualitysource >"$wiki_v2_evidence/go-vet.log" 2>&1
printf 'Explicit v1→v2 FactSet ODS migration, weak-schema refusal and parent/sidecar CAS passed.\n'
