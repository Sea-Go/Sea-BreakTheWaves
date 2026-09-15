#!/usr/bin/env bash
set -euo pipefail

community_root="$(cd "$(dirname "$0")/../.." && pwd)"
community_dc_root="${SEA_DC_PLATFORM_ROOT:?set SEA_DC_PLATFORM_ROOT to the reviewed DataCenter checkout}"
community_rtw_root="${SEA_RTW_COMMUNITY_ROOT:?set SEA_RTW_COMMUNITY_ROOT to the reviewed RTW checkout}"
community_runtime="${SEA_COMMUNITY_WAREHOUSE_RUNTIME:?set SEA_COMMUNITY_WAREHOUSE_RUNTIME to the locked CH/S3/dbt runtime}"
community_expected_dc="${SEA_COMMUNITY_EXPECTED_DC_SHA:?set the reviewed full DataCenter SHA}"
community_expected_rtw="${SEA_COMMUNITY_EXPECTED_RTW_SHA:?set the reviewed full RTW SHA}"
community_pg_bin="${COMMUNITY_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
community_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-community-warehouse.XXXXXX")"
community_release="$community_tmp/release"
community_comment_ready="$community_tmp/comment-ready.json"
community_like_ready="$community_tmp/like-ready.json"
community_pids=()
community_pg_started=false

finish() {
  : > "$community_release"
  for pid in "${community_pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  done
  for pid in "${community_pids[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
  if "$community_pg_started"; then
    "$community_pg_bin/pg_ctl" -D "$community_tmp/pg" -m fast -w stop >"$community_tmp/postgres-stop.log" 2>&1 || true
  fi
  printf 'Evidence directory: %s\n' "$community_tmp"
}
trap finish EXIT

for value in "$community_expected_dc" "$community_expected_rtw"; do
  if [[ ! "$value" =~ ^[0-9a-f]{40}$ ]]; then
    printf 'reviewed repository SHAs must be full lowercase commits\n' >&2
    exit 1
  fi
done
community_actual_dc="$(git -C "$community_dc_root" rev-parse HEAD)"
community_actual_rtw="$(git -C "$community_rtw_root" rev-parse HEAD)"
community_actual_btw="$(git -C "$community_root" rev-parse HEAD)"
if [[ "$community_actual_dc" != "$community_expected_dc" || "$community_actual_rtw" != "$community_expected_rtw" ]]; then
  printf 'DataCenter or RTW HEAD differs from the reviewed commit\n' >&2
  exit 1
fi
for artifact in clickhouse weed .venv/bin/dbt; do
  test -f "$community_runtime/$artifact"
done
test -f "$community_dc_root/cmd/platform/main.go"
test -f "$community_rtw_root/service/comment/rpc/cmd/fact-dispatch/main.go"
test -f "$community_rtw_root/service/like/rpc/cmd/fact-dispatch/main.go"

read -r community_pg_port community_dc_port community_comment_port community_like_port < <(python3 - <<'PY'
import socket
ports = []
for _ in range(4):
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        ports.append(str(sock.getsockname()[1]))
print(*ports)
PY
)
"$community_pg_bin/initdb" -D "$community_tmp/pg" -A trust --no-locale --encoding=UTF8 -U community_warehouse >"$community_tmp/initdb.log"
"$community_pg_bin/pg_ctl" -D "$community_tmp/pg" -l "$community_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $community_pg_port -k $community_tmp" -w start
community_pg_started=true
for database in dc comment like warehouse unit; do
  "$community_pg_bin/createdb" -h 127.0.0.1 -p "$community_pg_port" -U community_warehouse "$database"
done
community_dsn="postgres://community_warehouse@127.0.0.1:$community_pg_port"
community_dc_dsn="$community_dsn/dc?sslmode=disable"
community_comment_dsn="$community_dsn/comment?sslmode=disable"
community_like_dsn="$community_dsn/like?sslmode=disable"
community_warehouse_dsn="$community_dsn/warehouse?sslmode=disable"
community_unit_dsn="$community_dsn/unit?sslmode=disable"
community_dc_url="http://127.0.0.1:$community_dc_port"
community_comment_url="http://127.0.0.1:$community_comment_port"
community_like_url="http://127.0.0.1:$community_like_port"
community_dc_token="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
community_comment_token="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
community_like_token="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"

(cd "$community_dc_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/dc-platform" ./cmd/platform)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/comment-dispatch" ./service/comment/rpc/cmd/fact-dispatch)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/comment-authority" ./service/comment/rpc/cmd/fact-authority)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/like-dispatch" ./service/like/rpc/cmd/fact-dispatch)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/like-authority" ./service/like/rpc/cmd/fact-authority)

DATABASE_URL="$community_dc_dsn" PLATFORM_SERVICE_TOKEN="$community_dc_token" SERVICE_VERSION="$community_actual_dc" \
  "$community_tmp/dc-platform" -listen "127.0.0.1:$community_dc_port" -migrate >"$community_tmp/dc.log" 2>&1 &
community_dc_pid="$!"
community_pids+=("$community_dc_pid")
community_dc_ready=false
for _ in $(seq 1 120); do
  if [[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $community_dc_token" \
    "$community_dc_url/v1/events/rtw.comment-rpc/readiness" 2>/dev/null || true)" == "404" ]]; then
    community_dc_ready=true
    break
  fi
  sleep 0.25
done
if ! "$community_dc_ready"; then
  printf 'DataCenter did not become ready; inspect %s\n' "$community_tmp/dc.log" >&2
  exit 1
fi

(cd "$community_rtw_root" && RTW_COMMENT_FACT_TEST_DSN="$community_comment_dsn" \
  RTW_COMMENT_FACT_SHARED_READY_FILE="$community_comment_ready" RTW_COMMENT_FACT_SHARED_RELEASE_FILE="$community_release" \
  GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
  -run '^TestCommentFactProcessFixture$' ./service/comment/rpc/internal/model) >"$community_tmp/comment-fixture.log" 2>&1 &
community_comment_fixture_pid="$!"
community_pids+=("$community_comment_fixture_pid")
(cd "$community_rtw_root" && RTW_LIKE_FACT_TEST_DSN="$community_like_dsn" \
  RTW_LIKE_FACT_SHARED_READY_FILE="$community_like_ready" RTW_LIKE_FACT_SHARED_RELEASE_FILE="$community_release" \
  GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
  -run '^TestLikeFactProcessFixture$' ./service/like/rpc/internal/model) >"$community_tmp/like-fixture.log" 2>&1 &
community_like_fixture_pid="$!"
community_pids+=("$community_like_fixture_pid")
for _ in $(seq 1 600); do
  if [[ -f "$community_comment_ready" && -f "$community_like_ready" ]]; then
    break
  fi
  if ! kill -0 "$community_comment_fixture_pid" 2>/dev/null || ! kill -0 "$community_like_fixture_pid" 2>/dev/null; then
    printf 'RTW fixture exited before readiness; inspect %s\n' "$community_tmp" >&2
    exit 1
  fi
  sleep 0.1
done
test -f "$community_comment_ready"
test -f "$community_like_ready"

for _ in 1 2; do
  for migration in 001_comment_domain_fact_outbox.sql 002_comment_dc_wire.sql 003_comment_fact_delivery.sql; do
    "$community_pg_bin/psql" "$community_comment_dsn" -X -q -v ON_ERROR_STOP=1 \
      -f "$community_rtw_root/service/comment/rpc/internal/model/migrations/$migration" >/dev/null
  done
  for migration in 001_like_domain_fact_outbox.sql 002_like_dc_wire.sql 003_like_fact_delivery.sql; do
    "$community_pg_bin/psql" "$community_like_dsn" -X -q -v ON_ERROR_STOP=1 \
      -f "$community_rtw_root/service/like/rpc/internal/model/migrations/$migration" >/dev/null
  done
done

for _ in $(seq 1 4); do
  COMMENT_DATABASE_URL="$community_comment_dsn" DC_PLATFORM_EVENT_URL="$community_dc_url/v1/events" \
  DC_PLATFORM_SERVICE_TOKEN="$community_dc_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_actual_rtw" \
  RTW_INSTANCE_ID=community-warehouse-comment-dispatch "$community_tmp/comment-dispatch" -once >>"$community_tmp/comment-dispatch.log" 2>&1
done
for _ in $(seq 1 2); do
  LIKE_DATABASE_URL="$community_like_dsn" DC_PLATFORM_EVENT_URL="$community_dc_url/v1/events" \
  DC_PLATFORM_SERVICE_TOKEN="$community_dc_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_actual_rtw" \
  RTW_INSTANCE_ID=community-warehouse-like-dispatch "$community_tmp/like-dispatch" -once >>"$community_tmp/like-dispatch.log" 2>&1
done

COMMENT_DATABASE_URL="$community_comment_dsn" COMMENT_FACT_AUTHORITY_LISTEN="127.0.0.1:$community_comment_port" \
COMMENT_FACT_AUTHORITY_TOKEN="$community_comment_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_actual_rtw" \
RTW_INSTANCE_ID=community-warehouse-comment-authority "$community_tmp/comment-authority" >"$community_tmp/comment-authority.log" 2>&1 &
community_comment_authority_pid="$!"
community_pids+=("$community_comment_authority_pid")
LIKE_DATABASE_URL="$community_like_dsn" LIKE_FACT_AUTHORITY_LISTEN="127.0.0.1:$community_like_port" \
LIKE_FACT_AUTHORITY_TOKEN="$community_like_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_actual_rtw" \
RTW_INSTANCE_ID=community-warehouse-like-authority "$community_tmp/like-authority" >"$community_tmp/like-authority.log" 2>&1 &
community_like_authority_pid="$!"
community_pids+=("$community_like_authority_pid")
for spec in "$community_comment_url|$community_comment_token|rtw.comment-rpc" "$community_like_url|$community_like_token|rtw.like-mq"; do
  IFS='|' read -r authority_url authority_token authority_producer <<<"$spec"
  authority_ready=false
  for _ in $(seq 1 120); do
    if [[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $authority_token" \
      "$authority_url/internal/v1/community/facts?producer=$authority_producer&event_id=missing" 2>/dev/null || true)" == "404" ]]; then
      authority_ready=true
      break
    fi
    sleep 0.25
  done
  if ! "$authority_ready"; then
    printf 'RTW authority did not become ready for %s\n' "$authority_producer" >&2
    exit 1
  fi
done

community_comment_ids="$(jq -r '.event_ids | join(",")' "$community_comment_ready")"
community_like_ids="$(jq -r '.event_ids | join(",")' "$community_like_ready")"
COMMUNITY_WAREHOUSE_REAL_DSN="$community_warehouse_dsn" \
COMMUNITY_WAREHOUSE_DC_URL="$community_dc_url" COMMUNITY_WAREHOUSE_DC_TOKEN="$community_dc_token" \
COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_URL="$community_comment_url" COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_TOKEN="$community_comment_token" \
COMMUNITY_WAREHOUSE_LIKE_AUTHORITY_URL="$community_like_url" COMMUNITY_WAREHOUSE_LIKE_AUTHORITY_TOKEN="$community_like_token" \
COMMUNITY_WAREHOUSE_COMMENT_EVENT_IDS="$community_comment_ids" COMMUNITY_WAREHOUSE_LIKE_EVENT_IDS="$community_like_ids" \
COMMUNITY_WAREHOUSE_DC_SHA="$community_actual_dc" COMMUNITY_WAREHOUSE_RTW_SHA="$community_actual_rtw" \
COMMUNITY_WAREHOUSE_BTW_SHA="$community_actual_btw" \
"$community_runtime/.venv/bin/python" "$community_root/warehouse/community/acceptance.py" \
  --runtime "$community_runtime" --output "$community_tmp/runtime-evidence" | tee "$community_tmp/acceptance.log"

: > "$community_release"
wait "$community_comment_fixture_pid"
wait "$community_like_fixture_pid"
kill "$community_comment_authority_pid" "$community_like_authority_pid"
wait "$community_comment_authority_pid"
wait "$community_like_authority_pid"

python3 - "$community_tmp/comment-dispatch.log" "$community_tmp/like-dispatch.log" \
  "$community_tmp/comment-authority.log" "$community_tmp/like-authority.log" <<'PY'
import json
import sys
required = {'timestamp','level','service','environment','service_version','instance_id','component','log_source','event','message'}
for path in sys.argv[1:]:
    rows = [json.loads(line) for line in open(path, encoding='utf-8')]
    assert rows and all(not (required - row.keys()) for row in rows), path
    terminal = [row for row in rows if row.get('event','').endswith(('.accepted','.stopped','request_finished'))]
    assert terminal and all('outcome' in row and 'duration_ms' in row for row in terminal), (path, terminal)
PY

(cd "$community_root" && COMMUNITY_WAREHOUSE_TEST_DSN="$community_unit_dsn" \
  GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 \
  ./internal/clients/ridethewind/communityauthority ./internal/warehouse/communitysource)
(cd "$community_root" && go vet ./internal/clients/ridethewind/communityauthority ./internal/warehouse/communitysource)
(cd "$community_root" && go mod verify)
(cd "$community_root" && git diff --check)
python3 - "$community_tmp/runtime-evidence/report.json" "$community_expected_dc" "$community_expected_rtw" <<'PY'
import json
import sys
report = json.load(open(sys.argv[1], encoding='utf-8'))
assert report['outcome'] == 'passed'
assert report['datacenter_sha'] == sys.argv[2] and report['ridethewind_sha'] == sys.argv[3]
assert report['producer_offsets'] == {'rtw.comment-rpc':[1,2,3,4], 'rtw.like-mq':[1,2]}
assert report['source_rows'] == 6 and report['dbt']['transition_count'] == 6
assert all(report['coverage']['negative_checks'].values())
PY
printf 'Community warehouse acceptance passed: %s\n' "$community_tmp/runtime-evidence/report.json"
