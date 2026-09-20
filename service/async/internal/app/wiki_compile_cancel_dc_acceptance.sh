#!/usr/bin/env bash
set -euo pipefail

# One task-owned PG16 and official DataCenter HTTP instance; no RTW/model/S3.
: "${SEA_WIKI_DC_ROOT:?Set to the fixed DataCenter checkout}"
wiki_cancel_pg_bin="${SEA_WIKI_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
wiki_cancel_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-wiki-cancel-acceptance.XXXXXX")"
wiki_cancel_pg_started=false
wiki_cancel_dc_pid=''
wiki_cancel_cleanup() {
  if [[ -n "$wiki_cancel_dc_pid" ]]; then
    kill "$wiki_cancel_dc_pid" 2>/dev/null || true
    wait "$wiki_cancel_dc_pid" 2>/dev/null || true
  fi
  if [[ "$wiki_cancel_pg_started" == true ]]; then
    "$wiki_cancel_pg_bin/pg_ctl" -D "$wiki_cancel_tmp/pg" -m fast -w stop > "$wiki_cancel_tmp/pg.stop.log" 2>&1 || true
  fi
  printf 'Evidence directory: %s\n' "$wiki_cancel_tmp"
}
trap wiki_cancel_cleanup EXIT

read -r wiki_cancel_pg_port wiki_cancel_dc_port < <(python3 - <<'PY'
import socket
sockets = [socket.socket() for _ in range(2)]
for sock in sockets:
    sock.bind(('127.0.0.1', 0))
print(*(sock.getsockname()[1] for sock in sockets))
for sock in sockets:
    sock.close()
PY
)
"$wiki_cancel_pg_bin/initdb" -D "$wiki_cancel_tmp/pg" -A trust --no-locale -U sea_wiki_cancel_test > "$wiki_cancel_tmp/pg.init.log"
"$wiki_cancel_pg_bin/pg_ctl" -D "$wiki_cancel_tmp/pg" -l "$wiki_cancel_tmp/pg.log" \
  -o "-h 127.0.0.1 -p $wiki_cancel_pg_port -k $wiki_cancel_tmp" -w start
wiki_cancel_pg_started=true
"$wiki_cancel_pg_bin/createdb" -h 127.0.0.1 -p "$wiki_cancel_pg_port" -U sea_wiki_cancel_test dc

(cd "$SEA_WIKI_DC_ROOT" && GOMAXPROCS=2 go build -mod=readonly -p=1 -o "$wiki_cancel_tmp/dc-platform" ./cmd/platform)
git -C "$SEA_WIKI_DC_ROOT" rev-parse HEAD > "$wiki_cancel_tmp/dc.sha"
DATABASE_URL="postgres://sea_wiki_cancel_test@127.0.0.1:$wiki_cancel_pg_port/dc?sslmode=disable" \
  PLATFORM_SERVICE_TOKEN=runtime-fixture "$wiki_cancel_tmp/dc-platform" -listen "127.0.0.1:$wiki_cancel_dc_port" -migrate > "$wiki_cancel_tmp/dc.log" 2>&1 &
wiki_cancel_dc_pid=$!
export SEA_TEST_DC_URL="http://127.0.0.1:$wiki_cancel_dc_port"
python3 - <<'PY'
import os, time, urllib.request, urllib.error
endpoint = os.environ['SEA_TEST_DC_URL'] + '/v1/jobs/not-a-job'
deadline = time.monotonic() + 30
while True:
    try:
        urllib.request.urlopen(endpoint, timeout=1).close()
        break
    except urllib.error.HTTPError:
        break
    except OSError:
        if time.monotonic() > deadline:
            raise
        time.sleep(.1)
PY

wiki_cancel_repo="$(cd "$(dirname "$0")/../.." && pwd)"
(cd "$wiki_cancel_repo" && GOMAXPROCS=2 go test -mod=readonly -race -p=1 -count=1 \
  -run '^TestWikiCompileWorkerRealDCJobsCancellation$' ./internal/app) > "$wiki_cancel_tmp/test.log" 2>&1
shasum -a 256 "$wiki_cancel_tmp/test.log" "$wiki_cancel_tmp/dc.sha" "$wiki_cancel_tmp/dc.log" > "$wiki_cancel_tmp/evidence.sha256"
