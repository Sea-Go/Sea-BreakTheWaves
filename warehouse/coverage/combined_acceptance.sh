#!/usr/bin/env bash
set -euo pipefail

fact_root="$(cd "$(dirname "$0")/../.." && pwd)"
fact_dc_root="${SEA_DC_PLATFORM_ROOT:?set SEA_DC_PLATFORM_ROOT to an isolated DataCenter checkout}"
fact_rtw_root="${SEA_RTW_FAVORITE_ROOT:?set SEA_RTW_FAVORITE_ROOT to the RTW authority checkout}"
fact_pg_bin="${FACT_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
fact_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-coverage-combined.XXXXXX")"
fact_ready="$fact_tmp/ready.json"
fact_release="$fact_tmp/release"
fact_dc_pid=""
fact_rtw_pid=""
fact_pg_started=false
finish() {
  # A failed publisher gate still releases the RTW fixture and isolated PG.
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
# The ready file holds credentials; the orchestrator passes them only as
# child-process environment and never prints them.
"${WAREHOUSE_FAVORITE_RUNTIME:?set locked CH/SeaweedFS/dbt runtime}/.venv/bin/python" \
  "$fact_root/warehouse/coverage/combined_acceptance.py" --runtime "$WAREHOUSE_FAVORITE_RUNTIME" \
  --output "$fact_tmp/cross-domain" --ready "$fact_ready" --pg-dsn "$DATABASE_URL" \
  --postgres-bin "$fact_pg_bin" >"$fact_tmp/cross-domain.log" 2>&1
python3 - "$fact_tmp/cross-domain" <<'PY'
import json
import sys
from pathlib import Path

root = Path(sys.argv[1])
summary_path = root / "report.json"
summary = json.loads(summary_path.read_text())
accepted = {}
snapshots = {}
bundles = {}
for label, fields in {
    "real": ("w1_after_retract", "w2"),
    "two": ("u1_w1_after_tail", "u1_w3", "u2_w3", "u3_empty_w3"),
    "multi": ("u1_w1_after_tail", "u1_w3", "u2_w3", "u3_empty_w3"),
}.items():
    report = json.loads((root / f"{label}-joined-ref.json").read_text())
    receipts = report.get("accepted_v2", {})
    if receipts.get("v1_and_serving_heads") != 0:
        raise RuntimeError(f"{label} v2 acceptance moved a v1 or Serving head")
    for field in fields:
        receipt = receipts.get(field, {})
        if receipt.get("status") != "accepted_historical_default_off" or receipt.get("replay") is not False:
            raise RuntimeError(f"{label} missing default-off v2 historical receipt: {field}")
        snapshot = report.get("snapshot_v2", {}).get(field, {})
        if snapshot.get("status") != "historical_default_off" or snapshot.get("baseline_artifact_sha256") != receipt.get("artifact_sha256"):
            raise RuntimeError(f"{label} missing same-run v2 historical snapshot: {field}")
    accepted[label] = {field: receipts[field] for field in fields}
    snapshots[label] = {field: report["snapshot_v2"][field]["snapshot_id"] for field in fields}
    expected_vectors = {"real": {"w2": 0}, "two": {"u1_w3": 0, "u2_w3": 1, "u3_empty_w3": 0},
                        "multi": {"u1_w3": 0, "u2_w3": 1, "u3_empty_w3": 0}}[label]
    if report.get("bundle_v2", {}).get(fields[0]) != "pending_tail":
        raise RuntimeError(f"{label} W1 with accepted tail became a current bundle")
    for field, vector in expected_vectors.items():
        bundle = report["bundle_v2"].get(field, {})
        if bundle.get("status") != "candidate_default_off" or bundle.get("user_vector") != [vector] or bundle.get("covered_snapshot_id") != snapshots[label][field]:
            raise RuntimeError(f"{label} missing same-run default-off bundle: {field}")
    bundles[label] = {field: report["bundle_v2"][field]["bundle_id"] for field in expected_vectors}
summary["accept_v2"] = "accepted_historical_default_off"
summary["accepted_v2"] = accepted
summary["snapshot_v2"] = snapshots
summary["bundle_v2"] = {"status": "candidate_default_off", "heads": "unchanged", "ids": bundles}
summary_path.write_text(json.dumps(summary, sort_keys=True, indent=2) + "\n")
print("V2 historical receipts and default-off snapshots/bundles accepted with v1 and Serving heads unchanged")
PY
: > "$fact_release"
wait "$fact_rtw_pid"
fact_rtw_pid=""
(cd "$fact_root" && GOFLAGS='-p=2' GOMAXPROCS=2 go vet ./warehouse/coverage ./internal/usermodel ./internal/warehouse/featurebaseline)
(cd "$fact_root" && go mod verify)
(cd "$fact_root" && git diff --check)
rg '^--- PASS:|^PASS$|^ok[[:space:]]' "$fact_tmp/rtw-test.log" "$fact_tmp/cross-domain/real-combined-test.log" "$fact_tmp/cross-domain/two-combined-test.log" "$fact_tmp/cross-domain/multi-rtw-test.log" "$fact_tmp/cross-domain/multi-combined-test.log" || true
printf 'Final combined report: %s\n' "$fact_tmp/cross-domain/report.json"
