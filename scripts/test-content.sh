#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
content_pg_bin="${CONTENT_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$content_pg_bin/initdb"
content_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-content-acceptance.XXXXXX")"
content_started=false
finish() {
  if "$content_started"; then "$content_pg_bin/pg_ctl" -D "$content_tmp/data" -m immediate -w stop >/dev/null; fi
  if [[ "${CONTENT_KEEP_EVIDENCE:-0}" == 1 ]]; then
    echo "Evidence directory: $content_tmp"
  else
    rm -rf -- "$content_tmp"
  fi
}
trap finish EXIT
content_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$content_pg_bin/initdb" -D "$content_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$content_tmp/initdb.log"
"$content_pg_bin/pg_ctl" -D "$content_tmp/data" -l "$content_tmp/postgres.log" -o "-h 127.0.0.1 -p $content_port -k $content_tmp" -w start >/dev/null
content_started=true
export CONTENT_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$content_port/postgres?sslmode=disable"
go test -race -count=1 -v ./service/async/internal/content ./service/common/artifacts | tee "$content_tmp/go-test.log"
go vet ./service/async/internal/content ./service/common/artifacts ./service/common/corpus ./migrations/content
