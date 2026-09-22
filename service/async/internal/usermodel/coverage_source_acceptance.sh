#!/usr/bin/env bash
set -euo pipefail

fact_root="$(cd "$(dirname "$0")/../../../.." && pwd)"
fact_dc_root="${SEA_DC_PLATFORM_ROOT:?set SEA_DC_PLATFORM_ROOT to an isolated DataCenter checkout}"
fact_rtw_root="${SEA_RTW_FAVORITE_ROOT:?set SEA_RTW_FAVORITE_ROOT to the RTW authority checkout}"
fact_pg_bin="${FACT_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
fact_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-coverage-source.XXXXXX")"
fact_ready="$fact_tmp/ready.json"
fact_release="$fact_tmp/release"
fact_dc_pid=""
fact_rtw_pid=""
fact_pg_started=false
finish() {
  # A failed BTW test must still release the RTW test's authority/PG lifecycle.
  : > "$fact_release"
  if [[ -n "$fact_rtw_pid" ]]; then
    wait "$fact_rtw_pid" 2>/dev/null || true
  fi
  if [[ -n "$fact_dc_pid" ]]; then
    kill "$fact_dc_pid" 2>/dev/null || true
    wait "$fact_dc_pid" 2>/dev/null || true
  fi
  if "$fact_pg_started"; then
    "$fact_pg_bin/pg_ctl" -D "$fact_tmp/pg" -m fast -w stop >"$fact_tmp/stop.log" 2>&1 || true
  fi
  printf 'Evidence directory: %s\n' "$fact_tmp"
}
trap finish EXIT

test -f "$fact_dc_root/cmd/platform/main.go"
test -f "$fact_rtw_root/service/favorite/rpc/internal/model/favorite_authority.go"
read -r fact_pg_port fact_dc_port < <(python3 - <<'PY'
import socket
ports = []
for _ in range(2):
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        ports.append(str(sock.getsockname()[1]))
print(*ports)
PY
)
"$fact_pg_bin/initdb" -D "$fact_tmp/pg" -A trust --no-locale -U sea_fact_authority >"$fact_tmp/initdb.log"
"$fact_pg_bin/pg_ctl" -D "$fact_tmp/pg" -l "$fact_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $fact_pg_port -k $fact_tmp" -w start
fact_pg_started=true
export DATABASE_URL="postgres://sea_fact_authority@127.0.0.1:$fact_pg_port/postgres?sslmode=disable"
export FAVORITE_TEST_DSN="$DATABASE_URL"
export USERMODEL_TEST_POSTGRES_DSN="$DATABASE_URL"
export PLATFORM_SERVICE_TOKEN="$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(32))
PY
)"
export FAVORITE_DC_URL="http://127.0.0.1:$fact_dc_port"
export FAVORITE_DC_TOKEN="$PLATFORM_SERVICE_TOKEN"
export FAVORITE_SHARED_READY_FILE="$fact_ready"
export FAVORITE_SHARED_RELEASE_FILE="$fact_release"
(cd "$fact_dc_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$fact_tmp/dc-platform" ./cmd/platform)
(cd "$fact_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$fact_tmp/favorite-fact-dispatch" ./service/favorite/rpc/cmd/fact-dispatch)
(cd "$fact_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$fact_tmp/favorite-fact-authority" ./service/favorite/rpc/cmd/fact-authority)
export FAVORITE_DISPATCH_BIN="$fact_tmp/favorite-fact-dispatch"
export FAVORITE_AUTHORITY_BIN="$fact_tmp/favorite-fact-authority"
"$fact_tmp/dc-platform" -listen "127.0.0.1:$fact_dc_port" -migrate >"$fact_tmp/dc-platform.log" 2>&1 &
fact_dc_pid=$!
fact_ready_http=false
for _ in $(seq 1 80); do
  if [[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $PLATFORM_SERVICE_TOKEN" \
      "$FAVORITE_DC_URL/v1/events/rtw.community.favorite/readiness" 2>/dev/null || true)" == "404" ]]; then
    fact_ready_http=true
    break
  fi
  sleep 0.25
done
if ! "$fact_ready_http"; then
  printf 'DataCenter platform did not become ready; inspect %s\n' "$fact_tmp/dc-platform.log" >&2
  exit 1
fi
(cd "$fact_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
  -run '^TestFavoriteDeliverySharedAuthorityFixture$' ./service/favorite/rpc/internal/model) >"$fact_tmp/rtw-test.log" 2>&1 &
fact_rtw_pid=$!
fact_ready_file=false
for _ in $(seq 1 600); do
  if [[ -f "$fact_ready" ]]; then
    fact_ready_file=true
    break
  fi
  if ! kill -0 "$fact_rtw_pid" 2>/dev/null; then
    printf 'RTW shared authority exited before ready; inspect %s\n' "$fact_tmp/rtw-test.log" >&2
    exit 1
  fi
  sleep 0.1
done
if ! "$fact_ready_file"; then
  printf 'RTW shared authority did not become ready; inspect %s\n' "$fact_tmp/rtw-test.log" >&2
  exit 1
fi
# The ready file holds credentials. Pass them only as child process environment;
# never source/eval it or print it into logs.
python3 - "$fact_ready" "$fact_root" "$fact_tmp/btw-test.log" <<'PY'
import json
import os
import subprocess
import sys

ready_path, root, log_path = sys.argv[1:]
with open(ready_path, encoding='utf-8') as handle:
    ready = json.load(handle)
env = os.environ.copy()
env.update({
    'SEA_FACT_AUTHORITY_URL': ready['authority_url'],
    'SEA_FACT_AUTHORITY_TOKEN': ready['authority_token'],
    'SEA_FACT_DC_URL': ready['dc_url'],
    'SEA_FACT_DC_TOKEN': ready['dc_token'],
    'SEA_FACT_ASSERT_EVENT_ID': ready['assert_event_id'],
    'SEA_FACT_RETRACT_EVENT_ID': ready['retract_event_id'],
    'GOFLAGS': '-p=2',
    'GOMAXPROCS': '2',
})
with open(log_path, 'w', encoding='utf-8') as output:
    result = subprocess.run(['go', 'test', '-mod=readonly', '-race', '-count=1', '-v',
                             '-run', '^TestCoverageVerifierRealDCAndRTW$', './service/async/internal/usermodel'],
                            cwd=root, env=env, stdout=output, stderr=subprocess.STDOUT,
                            check=False)
raise SystemExit(result.returncode)
PY
: > "$fact_release"
wait "$fact_rtw_pid"
fact_rtw_pid=""
(cd "$fact_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go vet ./service/async/internal/usermodel)
(cd "$fact_root" && go mod verify)
(cd "$fact_root" && git diff --check)
rg '^--- PASS:|^PASS$|^ok[[:space:]]' "$fact_tmp/rtw-test.log" "$fact_tmp/btw-test.log" || true
