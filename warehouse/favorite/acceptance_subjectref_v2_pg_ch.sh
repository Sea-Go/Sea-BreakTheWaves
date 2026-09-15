#!/usr/bin/env bash
set -euo pipefail

# One task-owned PG16/official locked Apply + preserved coverage objects,
# exported PG sidecar mapping, native CH/dbt/S3 candidate and expected rejects.
favorite_repo="$(cd "$(dirname "$0")/../.." && pwd)"
favorite_runtime="${WAREHOUSE_FAVORITE_RUNTIME:?set locked ClickHouse/SeaweedFS/dbt runtime}"
favorite_pg_bin="${FAVORITE_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
favorite_evidence="$(mktemp -d "${TMPDIR:-/tmp}/sea-favorite-v2-pg-ch.XXXXXX")"
favorite_test_pid=""
favorite_pg_started=false
finish() {
  local original_status=$? status_code
  if [[ -d "$favorite_evidence/handoff" ]]; then
    : > "$favorite_evidence/handoff/release"
  fi
  if [[ -n "$favorite_test_pid" ]]; then
    wait "$favorite_test_pid" || original_status=1
  fi
  if "$favorite_pg_started"; then
    if ! "$favorite_pg_bin/pg_ctl" -D "$favorite_evidence/pg" -m fast -w stop \
      >"$favorite_evidence/stop.log" 2>&1; then original_status=1; fi
    set +e
    "$favorite_pg_bin/pg_ctl" -D "$favorite_evidence/pg" status \
      >"$favorite_evidence/status.log" 2>&1
    status_code=$?
    set -e
    if [[ "$status_code" != 3 ]]; then original_status=1; fi
  fi
  printf 'Evidence directory: %s\n' "$favorite_evidence"
  return "$original_status"
}
trap finish EXIT

test -x "$favorite_runtime/clickhouse"
test -x "$favorite_runtime/weed"
test -x "$favorite_runtime/.venv/bin/python"
test -x "$favorite_runtime/.venv/bin/dbt"
test -x "$favorite_pg_bin/initdb"
test -x "$favorite_pg_bin/pg_ctl"
mkdir -m 700 "$favorite_evidence/handoff"
favorite_port="$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    print(sock.getsockname()[1])
PY
)"
"$favorite_pg_bin/initdb" -D "$favorite_evidence/pg" -A trust --no-locale \
  -U sea_btw_favorite_test >"$favorite_evidence/initdb.log"
"$favorite_pg_bin/pg_ctl" -D "$favorite_evidence/pg" -l "$favorite_evidence/postgres.log" \
  -o "-h 127.0.0.1 -p $favorite_port -k $favorite_evidence" -w start \
  >"$favorite_evidence/start.log"
favorite_pg_started=true
export FAVORITE_V2_TEST_DSN="postgres://sea_btw_favorite_test@127.0.0.1:$favorite_port/postgres?sslmode=disable"
export FAVORITE_V2_TEST_NONCE="$(python3 - <<'PY'
import secrets
print(secrets.token_hex(32))
PY
)"
export FAVORITE_V2_CH_HANDOFF_DIR="$favorite_evidence/handoff"
(cd "$favorite_repo" && GOMAXPROCS=2 go test -mod=readonly -race -p=1 -count=1 -v \
  -run '^TestFavoriteSubjectRefV2StorageContinuousWritersAndFrozenArtifacts$' \
  ./internal/warehouse/favoritesource) >"$favorite_evidence/official-pg-race.log" 2>&1 &
favorite_test_pid=$!
favorite_ready=false
for _ in $(seq 1 1800); do
  if [[ -f "$favorite_evidence/handoff/ready.json" ]]; then
    favorite_ready=true
    break
  fi
  if ! kill -0 "$favorite_test_pid" 2>/dev/null; then
    printf 'Official PG test exited before sidecar handoff; inspect %s\n' \
      "$favorite_evidence/official-pg-race.log" >&2
    exit 1
  fi
  sleep 0.1
done
if ! "$favorite_ready"; then
  printf 'Official locked PG Apply did not hand off within deadline; inspect %s\n' \
    "$favorite_evidence/official-pg-race.log" >&2
  exit 1
fi
"$favorite_runtime/.venv/bin/python" "$favorite_repo/warehouse/favorite/acceptance_subjectref_v2_r1.py" \
  --runtime "$favorite_runtime" --output "$favorite_evidence/ch-evidence" \
  --ods "$favorite_evidence/handoff/ods.jsonl" \
  --mapping "$favorite_evidence/handoff/mapping-pg.jsonl" \
  --ready "$favorite_evidence/handoff/ready.json" --skip-synthetic \
  >"$favorite_evidence/ch-acceptance.log" 2>&1
: > "$favorite_evidence/handoff/release"
wait "$favorite_test_pid"
favorite_test_pid=""
(cd "$favorite_repo" && GOMAXPROCS=2 go vet -mod=readonly ./internal/warehouse/favoritesource \
  >"$favorite_evidence/vet.log" 2>&1)
(cd "$favorite_repo" && go mod verify >"$favorite_evidence/mod-verify.log" 2>&1)
(cd "$favorite_repo" && git diff --check >"$favorite_evidence/diff-check.log" 2>&1)
python3 - "$favorite_evidence/ch-evidence/report.json" <<'PY'
import json, sys
report = json.load(open(sys.argv[1], encoding='utf-8'))
print('Official PG sidecar to native CH/dbt/S3:', report['status'],
      'logical_transitions=', report['real']['logical_transitions'],
      'rejected=', len(report['rejected']))
PY
