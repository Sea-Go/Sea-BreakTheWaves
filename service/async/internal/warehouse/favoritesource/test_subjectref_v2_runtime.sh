#!/usr/bin/env bash
set -euo pipefail

# Test-only: a NEW loopback PostgreSQL 16 cluster, one fresh database per Go
# scenario, and the existing favorite ODS/coverage schema. No configured DB.
favorite_repo="$(cd "$(dirname "$0")/../../.." && pwd)"
favorite_pg_bin="${FAVORITE_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
favorite_evidence="$(mktemp -d "${TMPDIR:-/tmp}/btw-favorite-v2-runtime.XXXXXX")"
favorite_started=false
finish() {
  local original_status=$? pg_status
  if "$favorite_started"; then
    if ! "$favorite_pg_bin/pg_ctl" -D "$favorite_evidence/pg" -m fast -w stop \
      > "$favorite_evidence/stop.log" 2>&1; then original_status=1; fi
    set +e
    "$favorite_pg_bin/pg_ctl" -D "$favorite_evidence/pg" status \
      > "$favorite_evidence/status.log" 2>&1
    pg_status=$?
    set -e
    if [[ "$pg_status" != 3 ]]; then original_status=1; fi
  fi
  printf 'Evidence directory: %s\n' "$favorite_evidence"
  return "$original_status"
}
trap finish EXIT

favorite_port="$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    print(sock.getsockname()[1])
PY
)"
"$favorite_pg_bin/initdb" -D "$favorite_evidence/pg" -A trust --no-locale \
  -U sea_btw_favorite_test > "$favorite_evidence/initdb.log"
"$favorite_pg_bin/pg_ctl" -D "$favorite_evidence/pg" -l "$favorite_evidence/postgres.log" \
  -o "-h 127.0.0.1 -p $favorite_port -k $favorite_evidence" -w start \
  > "$favorite_evidence/start.log"
favorite_started=true
export FAVORITE_V2_TEST_DSN="postgres://sea_btw_favorite_test@127.0.0.1:$favorite_port/postgres?sslmode=disable"
export FAVORITE_V2_TEST_NONCE="$(python3 - <<'PY'
import secrets
print(secrets.token_hex(32))
PY
)"
cd "$favorite_repo"
favorite_filter="${FAVORITE_V2_TEST_FILTER:-^TestFavoriteSubjectRefV2Storage}"
GOMAXPROCS=2 go test -mod=readonly -race -p=1 -count=1 -v \
  -run "$favorite_filter" ./internal/warehouse/favoritesource \
  > "$favorite_evidence/favoritesource-race.log" 2>&1
go vet -mod=readonly ./internal/warehouse/favoritesource \
  > "$favorite_evidence/favoritesource-vet.log" 2>&1
git diff --check > "$favorite_evidence/diff-check.log" 2>&1
printf 'Favorite SubjectRef v2 runtime candidate passed against a fresh local PostgreSQL 16\n'
