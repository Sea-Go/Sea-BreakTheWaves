#!/usr/bin/env bash
set -euo pipefail

community_root="$(cd "$(dirname "$0")/../.." && pwd)"
community_dc_root="${SEA_DC_PLATFORM_ROOT:?set SEA_DC_PLATFORM_ROOT to the pinned DataCenter checkout}"
community_rtw_root="${SEA_RTW_COMMUNITY_ROOT:?set SEA_RTW_COMMUNITY_ROOT to the RTW community delivery checkout}"
community_pg_bin="${COMMUNITY_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
community_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-community-facts.XXXXXX")"
community_comment_ready="$community_tmp/comment-ready.json"
community_like_ready="$community_tmp/like-ready.json"
community_release="$community_tmp/release"
community_pg_started=false
community_pids=()

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

"$community_pg_bin/initdb" -D "$community_tmp/pg" -A trust --no-locale --encoding=UTF8 -U sea_community >"$community_tmp/initdb.log"
"$community_pg_bin/pg_ctl" -D "$community_tmp/pg" -l "$community_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $community_pg_port -k $community_tmp" -w start
community_pg_started=true
for database in dc comment like comment_unit like_unit usermodel; do
  "$community_pg_bin/createdb" -h 127.0.0.1 -p "$community_pg_port" -U sea_community "$database"
done
community_dsn_prefix="postgres://sea_community@127.0.0.1:$community_pg_port"
community_dc_dsn="$community_dsn_prefix/dc?sslmode=disable"
community_comment_dsn="$community_dsn_prefix/comment?sslmode=disable"
community_like_dsn="$community_dsn_prefix/like?sslmode=disable"
community_comment_unit_dsn="$community_dsn_prefix/comment_unit?sslmode=disable"
community_like_unit_dsn="$community_dsn_prefix/like_unit?sslmode=disable"
community_usermodel_dsn="$community_dsn_prefix/usermodel?sslmode=disable"
community_dc_url="http://127.0.0.1:$community_dc_port"
community_comment_url="http://127.0.0.1:$community_comment_port"
community_like_url="http://127.0.0.1:$community_like_port"
community_dc_token="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
community_comment_token="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
community_like_token="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
community_rtw_sha="$(git -C "$community_rtw_root" rev-parse HEAD)"

(cd "$community_dc_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/dc-platform" ./cmd/platform)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/comment-dispatch" ./service/comment/rpc/cmd/fact-dispatch)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/comment-authority" ./service/comment/rpc/cmd/fact-authority)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/like-dispatch" ./service/like/rpc/cmd/fact-dispatch)
(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$community_tmp/like-authority" ./service/like/rpc/cmd/fact-authority)
(cd "$community_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -race -mod=readonly -o "$community_tmp/btw-worker" ./cmd/worker)

DATABASE_URL="$community_dc_dsn" PLATFORM_SERVICE_TOKEN="$community_dc_token" \
  SERVICE_VERSION="$(git -C "$community_dc_root" rev-parse HEAD)" \
  "$community_tmp/dc-platform" -listen "127.0.0.1:$community_dc_port" -migrate >"$community_tmp/dc.log" 2>&1 &
community_pids+=("$!")
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
  printf 'DataCenter platform did not become ready; inspect %s\n' "$community_tmp/dc.log" >&2
  exit 1
fi

(cd "$community_rtw_root" && \
  RTW_COMMENT_FACT_TEST_DSN="$community_comment_dsn" \
  RTW_COMMENT_FACT_SHARED_READY_FILE="$community_comment_ready" \
  RTW_COMMENT_FACT_SHARED_RELEASE_FILE="$community_release" \
  GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
    -run '^TestCommentFactProcessFixture$' ./service/comment/rpc/internal/model) >"$community_tmp/comment-fixture.log" 2>&1 &
community_comment_fixture_pid="$!"
community_pids+=("$community_comment_fixture_pid")
(cd "$community_rtw_root" && \
  RTW_LIKE_FACT_TEST_DSN="$community_like_dsn" \
  RTW_LIKE_FACT_SHARED_READY_FILE="$community_like_ready" \
  RTW_LIKE_FACT_SHARED_RELEASE_FILE="$community_release" \
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
  "$community_pg_bin/psql" "$community_comment_dsn" -X -q -v ON_ERROR_STOP=1 \
    -f "$community_rtw_root/service/comment/rpc/internal/model/migrations/001_comment_domain_fact_outbox.sql" >/dev/null
  "$community_pg_bin/psql" "$community_comment_dsn" -X -q -v ON_ERROR_STOP=1 \
    -f "$community_rtw_root/service/comment/rpc/internal/model/migrations/002_comment_dc_wire.sql" >/dev/null
  "$community_pg_bin/psql" "$community_comment_dsn" -X -q -v ON_ERROR_STOP=1 \
    -f "$community_rtw_root/service/comment/rpc/internal/model/migrations/003_comment_fact_delivery.sql" >/dev/null
  "$community_pg_bin/psql" "$community_like_dsn" -X -q -v ON_ERROR_STOP=1 \
    -f "$community_rtw_root/service/like/rpc/internal/model/migrations/001_like_domain_fact_outbox.sql" >/dev/null
  "$community_pg_bin/psql" "$community_like_dsn" -X -q -v ON_ERROR_STOP=1 \
    -f "$community_rtw_root/service/like/rpc/internal/model/migrations/002_like_dc_wire.sql" >/dev/null
  "$community_pg_bin/psql" "$community_like_dsn" -X -q -v ON_ERROR_STOP=1 \
    -f "$community_rtw_root/service/like/rpc/internal/model/migrations/003_like_fact_delivery.sql" >/dev/null
done

for _ in $(seq 1 4); do
  COMMENT_DATABASE_URL="$community_comment_dsn" DC_PLATFORM_EVENT_URL="$community_dc_url/v1/events" \
    DC_PLATFORM_SERVICE_TOKEN="$community_dc_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_rtw_sha" \
    RTW_INSTANCE_ID=comment-dispatch-acceptance "$community_tmp/comment-dispatch" -once >>"$community_tmp/comment-dispatch.log" 2>&1
done
for _ in $(seq 1 2); do
  LIKE_DATABASE_URL="$community_like_dsn" DC_PLATFORM_EVENT_URL="$community_dc_url/v1/events" \
    DC_PLATFORM_SERVICE_TOKEN="$community_dc_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_rtw_sha" \
    RTW_INSTANCE_ID=like-dispatch-acceptance "$community_tmp/like-dispatch" -once >>"$community_tmp/like-dispatch.log" 2>&1
done

COMMENT_DATABASE_URL="$community_comment_dsn" COMMENT_FACT_AUTHORITY_LISTEN="127.0.0.1:$community_comment_port" \
  COMMENT_FACT_AUTHORITY_TOKEN="$community_comment_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_rtw_sha" \
  RTW_INSTANCE_ID=comment-authority-acceptance "$community_tmp/comment-authority" >"$community_tmp/comment-authority.log" 2>&1 &
community_comment_authority_pid="$!"
community_pids+=("$community_comment_authority_pid")
LIKE_DATABASE_URL="$community_like_dsn" LIKE_FACT_AUTHORITY_LISTEN="127.0.0.1:$community_like_port" \
  LIKE_FACT_AUTHORITY_TOKEN="$community_like_token" RTW_ENVIRONMENT=test RTW_SERVICE_VERSION="$community_rtw_sha" \
  RTW_INSTANCE_ID=like-authority-acceptance "$community_tmp/like-authority" >"$community_tmp/like-authority.log" 2>&1 &
community_like_authority_pid="$!"
community_pids+=("$community_like_authority_pid")
for endpoint in \
  "$community_comment_url/internal/v1/community/facts?producer=rtw.comment-rpc&event_id=missing" \
  "$community_like_url/internal/v1/community/facts?producer=rtw.like-mq&event_id=missing"; do
  authority_ready=false
  authority_token="$community_comment_token"
  if [[ "$endpoint" == "$community_like_url"* ]]; then
    authority_token="$community_like_token"
  fi
  for _ in $(seq 1 120); do
    if [[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $authority_token" "$endpoint" 2>/dev/null || true)" == "404" ]]; then
      authority_ready=true
      break
    fi
    sleep 0.25
  done
  if ! "$authority_ready"; then
    printf 'RTW authority did not become ready: %s\n' "$endpoint" >&2
    exit 1
  fi
done

comment_ids="$(jq -r '.event_ids | join(",")' "$community_comment_ready")"
like_ids="$(jq -r '.event_ids | join(",")' "$community_like_ready")"
(cd "$community_root" && \
  SEA_COMMUNITY_DC_URL="$community_dc_url" SEA_COMMUNITY_DC_TOKEN="$community_dc_token" \
  SEA_COMMENT_AUTHORITY_URL="$community_comment_url" SEA_COMMENT_AUTHORITY_TOKEN="$community_comment_token" \
  SEA_COMMENT_EVENT_IDS="$comment_ids" SEA_LIKE_AUTHORITY_URL="$community_like_url" \
  SEA_LIKE_AUTHORITY_TOKEN="$community_like_token" SEA_LIKE_EVENT_IDS="$like_ids" \
  USERMODEL_TEST_POSTGRES_DSN="$community_usermodel_dsn" BTW_WORKER_BIN="$community_tmp/btw-worker" \
  SEA_COMMUNITY_EVIDENCE_DIR="$community_tmp" \
  BTW_WORKER_VERSION="$(git -C "$community_root" rev-parse HEAD)" \
  GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
    -run '^TestCommunityFactWorkerProcessesReal$' ./cmd/worker) | tee "$community_tmp/btw-process.log"

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

required = {'timestamp', 'level', 'service', 'environment', 'service_version', 'instance_id',
            'component', 'log_source', 'event', 'message'}
for path in sys.argv[1:]:
    records = []
    with open(path, encoding='utf-8') as handle:
        for number, line in enumerate(handle, 1):
            record = json.loads(line)
            missing = required - record.keys()
            assert not missing, (path, number, missing, record)
            records.append(record)
    assert records, path
    terminal = [record for record in records if record.get('outcome') in {'succeeded', 'failed', 'rejected', 'retryable', 'manual_migration_required'}]
    assert terminal and all('duration_ms' in record for record in terminal), (path, terminal)
assert any('trace_id' in json.loads(line) for path in sys.argv[3:] for line in open(path, encoding='utf-8')), 'authority Context trace missing'
PY

(cd "$community_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 \
  ./service/common/communityfact ./service/comment/rpc/cmd/fact-dispatch ./service/comment/rpc/cmd/fact-authority \
  ./service/like/rpc/cmd/fact-dispatch ./service/like/rpc/cmd/fact-authority)
(cd "$community_rtw_root" && RTW_COMMENT_FACT_TEST_DSN="$community_comment_unit_dsn" \
  GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 ./service/comment/rpc/internal/model)
(cd "$community_rtw_root" && RTW_LIKE_FACT_TEST_DSN="$community_like_unit_dsn" \
  GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 ./service/like/rpc/internal/model)
(cd "$community_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 ./internal/app ./cmd/worker)
(cd "$community_rtw_root" && go vet ./service/common/communityfact ./service/comment/rpc/cmd/fact-dispatch \
  ./service/comment/rpc/cmd/fact-authority ./service/like/rpc/cmd/fact-dispatch ./service/like/rpc/cmd/fact-authority)
(cd "$community_root" && go vet ./internal/app ./cmd/worker)
(cd "$community_rtw_root" && git diff --check)
(cd "$community_root" && git diff --check)

jq -n --arg dc_sha "$(git -C "$community_dc_root" rev-parse HEAD)" \
  --arg rtw_sha "$(git -C "$community_rtw_root" rev-parse HEAD)" \
  --arg btw_sha "$(git -C "$community_root" rev-parse HEAD)" \
  --argjson comment_events 4 --argjson like_events 2 \
  '{schema_version:"sea.community-fact-process-acceptance.v1",outcome:"passed",datacenter_sha:$dc_sha,
    ridethewind_sha:$rtw_sha,breakthewaves_sha:$btw_sha,comment_events:$comment_events,like_events:$like_events,
    identity_wire_v2:{issuer:"rtw.identity",subject_id_source:"rtw-user-center-uid",realm_field:false,tenant_model:"none"},
    source_precision:{target_revision:null,revision_status:"unknown",search_evidence:false},
    consumer:{trpc_agent_go_graph:true,lost_ack_restart:true,synthetic_impressions:0}}' >"$community_tmp/report.json"
jq -e '.outcome == "passed" and .datacenter_sha == "f59a676a3439f66122e0ec579cd22f030719e058" and
  .comment_events == 4 and .like_events == 2 and .identity_wire_v2.tenant_model == "none" and
  .consumer.trpc_agent_go_graph and .consumer.lost_ack_restart and .consumer.synthetic_impressions == 0' \
  "$community_tmp/report.json" >/dev/null
printf 'Community fact process acceptance passed: %s\n' "$community_tmp/report.json"
