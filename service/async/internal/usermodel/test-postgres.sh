#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../../.."
usermodel_pg_bin="${USERMODEL_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$usermodel_pg_bin/initdb"
usermodel_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sea-usermodel-acceptance.XXXXXX")"
usermodel_started=false
finish() {
  if "$usermodel_started"; then "$usermodel_pg_bin/pg_ctl" -D "$usermodel_tmp/data" -m immediate -w stop >/dev/null; fi
  if [[ "${USERMODEL_KEEP_EVIDENCE:-0}" == 1 ]]; then
    echo "Evidence directory: $usermodel_tmp"
  else
    rm -rf -- "$usermodel_tmp"
  fi
}
trap finish EXIT
usermodel_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$usermodel_pg_bin/initdb" -D "$usermodel_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$usermodel_tmp/initdb.log"
"$usermodel_pg_bin/pg_ctl" -D "$usermodel_tmp/data" -l "$usermodel_tmp/postgres.log" -o "-h 127.0.0.1 -p $usermodel_port -k $usermodel_tmp" -w start >/dev/null
usermodel_started=true
export USERMODEL_TEST_POSTGRES_DSN="postgres://$(id -un)@127.0.0.1:$usermodel_port/postgres?sslmode=disable"
go test -race -count=1 -v ./service/async/internal/usermodel | tee "$usermodel_tmp/go-test.log"
go vet ./service/async/internal/usermodel
