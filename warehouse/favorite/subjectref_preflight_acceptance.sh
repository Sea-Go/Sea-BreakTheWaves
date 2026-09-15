#!/usr/bin/env bash
set -euo pipefail

favorite_root="$(cd "$(dirname "$0")/../.." && pwd)"
favorite_pg_bin="${FAVORITE_PREFLIGHT_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
favorite_tmp="$(mktemp -d /tmp/btwfav-preflight.XXXXXX)"
favorite_pg_started=false
finish() {
  if "$favorite_pg_started"; then
    "$favorite_pg_bin/pg_ctl" -D "$favorite_tmp/pg" -m fast -w stop >"$favorite_tmp/stop.log" 2>&1 || true
  fi
  rm -rf "$favorite_tmp"
}
trap finish EXIT

favorite_pg_port="$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    print(sock.getsockname()[1])
PY
)"
if ! "$favorite_pg_bin/initdb" -D "$favorite_tmp/pg" -A trust --no-locale -U sea_preflight >"$favorite_tmp/initdb.log" 2>&1; then
  printf 'isolated favorite SubjectRef preflight: PG init failed\n' >&2
  exit 1
fi
if ! "$favorite_pg_bin/pg_ctl" -D "$favorite_tmp/pg" -l "$favorite_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $favorite_pg_port -k $favorite_tmp" -w start >"$favorite_tmp/start.log" 2>&1; then
  printf 'isolated favorite SubjectRef preflight: PG start failed\n' >&2
  rg 'FATAL|could not|permission|No space|error' "$favorite_tmp/start.log" "$favorite_tmp/postgres.log" >&2 || true
  exit 1
fi
favorite_pg_started=true

if ! (cd "$favorite_root" && \
  env -u COVERAGE_S3_PREFIX -u COVERAGE_TWO_ODS_OUTPUT -u COVERAGE_TWO_REPORT \
  COVERAGE_TWO_DSN="host=127.0.0.1 port=$favorite_pg_port dbname=postgres user=sea_preflight sslmode=disable" \
  GOFLAGS='-p=1' GOMAXPROCS=2 go test -mod=readonly -count=1 -v \
    -run '^TestCoveragePublisherTwoSubjects$' ./internal/warehouse/favoritesource) \
    >"$favorite_tmp/test.log" 2>&1; then
  printf 'isolated favorite SubjectRef preflight failed; test output withheld because fixture may contain source values\n' >&2
  exit 1
fi
if ! (cd "$favorite_root" && GOFLAGS='-p=1' GOMAXPROCS=2 go vet ./internal/warehouse/favoritesource) \
  >"$favorite_tmp/vet.log" 2>&1; then
  printf 'isolated favorite SubjectRef preflight: vet failed\n' >&2
  exit 1
fi
rg 'L2 isolated PG/read-only local coverage preflight|--- PASS: TestCoveragePublisherTwoSubjects|^PASS$|^ok[[:space:]]' "$favorite_tmp/test.log"
printf 'favorite SubjectRef preflight vet: passed\n'
