#!/usr/bin/env bash
set -euo pipefail

fact_root="$(cd "$(dirname "$0")/../.." && pwd)"
fact_dc_root="${SEA_DC_PLATFORM_ROOT:?set SEA_DC_PLATFORM_ROOT to an isolated DataCenter checkout}"
fact_rtw_root="${SEA_RTW_FAVORITE_ROOT:?set SEA_RTW_FAVORITE_ROOT to the delivered isolated RTW checkout}"
fact_pg_bin="${FACT_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
fact_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-fact-multitype.XXXXXX")"
fact_pg_started=false
fact_dc_pid=""
finish() {
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
test -f "$fact_rtw_root/service/favorite/rpc/internal/model/favorite_delivery_test.go"
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
"$fact_pg_bin/initdb" -D "$fact_tmp/pg" -A trust --no-locale -U sea_fact_test >"$fact_tmp/initdb.log"
"$fact_pg_bin/pg_ctl" -D "$fact_tmp/pg" -l "$fact_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $fact_pg_port -k $fact_tmp" -w start
fact_pg_started=true
export DATABASE_URL="postgres://sea_fact_test@127.0.0.1:$fact_pg_port/postgres?sslmode=disable"
export FAVORITE_TEST_DSN="$DATABASE_URL"
export USERMODEL_TEST_POSTGRES_DSN="$DATABASE_URL"
export PLATFORM_SERVICE_TOKEN="$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(32))
PY
)"
export FAVORITE_DC_URL="http://127.0.0.1:$fact_dc_port"
export FAVORITE_DC_TOKEN="$PLATFORM_SERVICE_TOKEN"
export SEA_FACT_DC_URL="$FAVORITE_DC_URL"
export SEA_FACT_DC_TOKEN="$PLATFORM_SERVICE_TOKEN"
(cd "$fact_dc_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$fact_tmp/dc-platform" ./cmd/platform)
(cd "$fact_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go build -mod=readonly -o "$fact_tmp/favorite-fact-dispatch" ./service/favorite/rpc/cmd/fact-dispatch)
export FAVORITE_DISPATCH_BIN="$fact_tmp/favorite-fact-dispatch"
"$fact_tmp/dc-platform" -listen "127.0.0.1:$fact_dc_port" -migrate >"$fact_tmp/dc-platform.log" 2>&1 &
fact_dc_pid=$!
fact_ready=false
for _ in $(seq 1 80); do
  if [[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $PLATFORM_SERVICE_TOKEN" \
      "$FAVORITE_DC_URL/v1/events/rtw.community.favorite/readiness" 2>/dev/null || true)" == "404" ]]; then
    fact_ready=true
    break
  fi
  sleep 0.25
done
if ! "$fact_ready"; then
  printf 'DataCenter platform did not become ready; inspect %s\n' "$fact_tmp/dc-platform.log" >&2
  exit 1
fi
(cd "$fact_rtw_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
  -run '^TestFavoriteDeliveryRealDataCenterTechnicalReceipt$' ./service/favorite/rpc/internal/model) | tee "$fact_tmp/rtw-test.log"
(cd "$fact_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go test -mod=readonly -race -count=1 -v \
  -run '^TestFactWorkerFavoriteRealDC$' ./internal/app) | tee "$fact_tmp/btw-test.log"
(cd "$fact_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go vet ./internal/app)
(cd "$fact_root" && git diff --check)
