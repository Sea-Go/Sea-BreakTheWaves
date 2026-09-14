#!/usr/bin/env bash
set -euo pipefail
# Only disposable loopback PostgreSQL and provider processes are started here.
: "${SEA_DC_ROOT:?Set SEA_DC_ROOT to the fixed DataCenter checkout}"
: "${SEA_RTW_ROOT:?Set SEA_RTW_ROOT to the fixed RideTheWind checkout}"
sea_repo="$(cd "$(dirname "$0")/../.." && pwd)"
sea_pg="${SEA_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
sea_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-runtime-acceptance.XXXXXX")"
sea_dc_pid=''
sea_rtw_pid=''
sea_pg_started=false
cleanup() {
  [[ -z "$sea_dc_pid" ]] || kill "$sea_dc_pid" 2>/dev/null || true
  [[ -z "$sea_rtw_pid" ]] || kill "$sea_rtw_pid" 2>/dev/null || true
  [[ -z "$sea_dc_pid" ]] || wait "$sea_dc_pid" 2>/dev/null || true
  [[ -z "$sea_rtw_pid" ]] || wait "$sea_rtw_pid" 2>/dev/null || true
  if [[ "$sea_pg_started" == true ]]; then "$sea_pg/pg_ctl" -D "$sea_tmp/pg" -m fast stop > "$sea_tmp/stop.log" 2>&1 || true; fi
  printf 'Evidence directory: %s\n' "$sea_tmp"
}
trap cleanup EXIT
read -r sea_pg_port sea_dc_port sea_rtw_port < <(python3 - <<'PY'
import socket
socks=[socket.socket() for _ in range(3)]
for s in socks:s.bind(('127.0.0.1',0))
print(*(s.getsockname()[1] for s in socks))
for s in socks:s.close()
PY
)
"$sea_pg/initdb" -D "$sea_tmp/pg" -A trust --no-locale -U sea_runtime_test > "$sea_tmp/initdb.log"
"$sea_pg/pg_ctl" -D "$sea_tmp/pg" -l "$sea_tmp/postgres.log" -o "-h 127.0.0.1 -p $sea_pg_port -k $sea_tmp" start
sea_pg_started=true
for sea_db in dc knowledge sessions content;do "$sea_pg/createdb" -h 127.0.0.1 -p "$sea_pg_port" -U sea_runtime_test "$sea_db";done
export SEA_TEST_OBJECT_DIRECTORY="$sea_tmp/objects"
export SEA_TEST_CONTENT_DSN="postgres://sea_runtime_test@127.0.0.1:$sea_pg_port/content?sslmode=disable"
export SEA_RUNTIME_TEST_DSN="postgres://sea_runtime_test@127.0.0.1:$sea_pg_port/sessions?sslmode=disable"
export SEA_TEST_DC_URL="http://127.0.0.1:$sea_dc_port"
export SEA_TEST_RTW_URL="http://127.0.0.1:$sea_rtw_port"
export SEA_ACCEPTANCE_TEMP="$sea_tmp"
export SEA_ACCEPTANCE_PG_PORT="$sea_pg_port"
export SEA_ACCEPTANCE_RTW_PORT="$sea_rtw_port"
(cd "$SEA_DC_ROOT" && go build -mod=readonly -o "$sea_tmp/dc-platform" ./cmd/platform)
(cd "$SEA_RTW_ROOT" && go build -mod=readonly -o "$sea_tmp/rtw-knowledge" ./service/knowledge/api)
git -C "$SEA_DC_ROOT" rev-parse HEAD > "$sea_tmp/dc.sha"
git -C "$SEA_RTW_ROOT" rev-parse HEAD > "$sea_tmp/rtw.sha"
export SEA_ACCEPTANCE_RTW_SHA="$(cat "$sea_tmp/rtw.sha")"
DATABASE_URL="postgres://sea_runtime_test@127.0.0.1:$sea_pg_port/dc?sslmode=disable" PLATFORM_SERVICE_TOKEN=runtime-fixture "$sea_tmp/dc-platform" -listen "127.0.0.1:$sea_dc_port" -migrate > "$sea_tmp/dc.log" 2>&1 &
sea_dc_pid=$!
python3 - <<'PY'
import json,os,pathlib
root=pathlib.Path(os.environ['SEA_ACCEPTANCE_TEMP'])
config={'Name':'knowledge-runtime-test','Host':'127.0.0.1','Port':int(os.environ['SEA_ACCEPTANCE_RTW_PORT']),'Mode':'test','Timeout':15000,'Log':{'Mode':'console','Level':'info'},'Observability':{'Version':os.environ['SEA_ACCEPTANCE_RTW_SHA'],'SampleRatio':1},'Auth':{'AccessSecret':'runtime-fixture-secret','AccessExpire':3600},'AdministratorIDs':['runtime-admin'],'WorkerToken':'runtime-worker','Postgres':{'DSN':f"postgres://sea_runtime_test@127.0.0.1:{os.environ['SEA_ACCEPTANCE_PG_PORT']}/knowledge?sslmode=disable",'MaxConnections':4,'Migrate':True},'Objects':{'Backend':'local','LocalDirectory':str(root/'objects')},'Delivery':{'Enabled':False}}
(root/'knowledge.json').write_text(json.dumps(config))
PY
"$sea_tmp/rtw-knowledge" -f "$sea_tmp/knowledge.json" > "$sea_tmp/rtw.log" 2>&1 &
sea_rtw_pid=$!
python3 - <<'PY'
import os,time,urllib.request,urllib.error
for endpoint in [os.environ['SEA_TEST_DC_URL']+'/v1/events/none/none',os.environ['SEA_TEST_RTW_URL']+'/v1/knowledge/modules']:
 deadline=time.monotonic()+20
 while True:
  try: urllib.request.urlopen(endpoint,timeout=1).close();break
  except urllib.error.HTTPError:break
  except OSError:
   if time.monotonic()>deadline:raise
   time.sleep(.1)
PY
cd "$sea_repo"
go test -race -count=1 -v ./internal/runtime/... ./internal/clients/... | tee "$sea_tmp/tests.log"
go vet ./internal/runtime/... ./internal/clients/...
git diff --check
