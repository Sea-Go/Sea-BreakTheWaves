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
favorite_race_args=()
if [[ "${FAVORITE_PREFLIGHT_RACE:-0}" == "1" ]]; then
  favorite_race_args=(-race)
fi

if ! (cd "$favorite_root" && \
  env -u COVERAGE_S3_PREFIX -u COVERAGE_TWO_ODS_OUTPUT -u COVERAGE_TWO_REPORT \
  COVERAGE_TWO_DSN="host=127.0.0.1 port=$favorite_pg_port dbname=postgres user=sea_preflight sslmode=disable" \
  FAVORITE_PREFLIGHT_REPORT_OUTPUT="$favorite_tmp/report.json" \
  GOFLAGS='-p=1' GOMAXPROCS=2 go test "${favorite_race_args[@]}" -mod=readonly -count=1 -v \
    -run '^(TestSubjectRefV2.*|TestCoveragePublisherTwoSubjects)$' ./internal/warehouse/favoritesource) \
    >"$favorite_tmp/test.log" 2>&1; then
  printf 'isolated favorite SubjectRef preflight failed; test output withheld because fixture may contain source values\n' >&2
  exit 1
fi
if ! (cd "$favorite_root" && GOFLAGS='-p=1' GOMAXPROCS=2 go vet ./internal/warehouse/favoritesource) \
  >"$favorite_tmp/vet.log" 2>&1; then
  printf 'isolated favorite SubjectRef preflight: vet failed\n' >&2
  exit 1
fi
if [[ ! -s "$favorite_tmp/report.json" ]]; then
  printf 'isolated favorite SubjectRef preflight: sanitized report missing\n' >&2
  exit 1
fi
if ! "$favorite_pg_bin/pg_ctl" -D "$favorite_tmp/pg" -m fast -w stop >"$favorite_tmp/stop.log" 2>&1; then
  printf 'isolated favorite SubjectRef preflight: PG stop failed\n' >&2
  exit 1
fi
favorite_pg_started=false
if "$favorite_pg_bin/pg_isready" -q -h 127.0.0.1 -p "$favorite_pg_port"; then
  printf 'isolated favorite SubjectRef preflight: PG port still responds\n' >&2
  exit 1
fi

favorite_evidence="$(mktemp -d /private/tmp/btwfav-preflight-evidence.XXXXXX)"
chmod 700 "$favorite_evidence"
install -m 600 "$favorite_tmp/report.json" "$favorite_evidence/report.json"
favorite_report_sha="$(shasum -a 256 "$favorite_evidence/report.json" | awk '{print $1}')"
python3 - "$favorite_evidence/report.json" "$favorite_evidence/summary.json" \
  "$(cd "$favorite_root" && git rev-parse f51723e)" "$(cd "$favorite_root" && git rev-parse HEAD)" \
  "$favorite_report_sha" "${FAVORITE_PREFLIGHT_RACE:-0}" <<'PY'
import json, os, sys
report_path, summary_path, base, head, report_sha, race = sys.argv[1:]
with open(report_path, encoding='utf-8') as source:
    report = json.load(source)
summary = {
    'base_head': base, 'head': head, 'evidence_level': 'L2_isolated_PG_local_objects',
    'report_sha256': report_sha, 'snapshot_sha256': report['snapshot_sha256'],
    'race_passed': race == '1', 'vet_passed': True,
    'pg_stopped': True, 'pg_port_closed': True,
    'production_checked': False, 'clickhouse_checked': False,
}
with open(summary_path, 'w', encoding='utf-8') as target:
    json.dump(summary, target, sort_keys=True, separators=(',', ':'))
    target.write('\n')
os.chmod(summary_path, 0o600)
PY
rg 'L2 isolated PG/read-only local coverage preflight|--- PASS: TestCoveragePublisherTwoSubjects|^PASS$|^ok[[:space:]]' "$favorite_tmp/test.log"
printf 'favorite SubjectRef preflight vet: passed\n'
printf 'favorite SubjectRef preflight PG stopped; localhost port closed\n'
printf 'Sanitized report: %s/report.json\n' "$favorite_evidence"
printf 'Report SHA-256: %s\n' "$favorite_report_sha"
