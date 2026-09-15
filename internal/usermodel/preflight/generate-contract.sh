#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."

preflight_pg_bin="${USERMODEL_PG_BIN:-/opt/homebrew/opt/postgresql@16/bin}"
test -x "$preflight_pg_bin/initdb"
preflight_tmp="$(mktemp -d /tmp/sea-srpf-contract.XXXXXX)"
preflight_started=false
finish() {
  if "$preflight_started"; then
    "$preflight_pg_bin/pg_ctl" -D "$preflight_tmp/data" -m immediate -w stop >/dev/null
  fi
  if [[ "${USERMODEL_PREFLIGHT_KEEP_EVIDENCE:-0}" == 1 ]]; then
    printf 'Fixture evidence directory: %s\n' "$preflight_tmp" >&2
  else
    rm -rf -- "$preflight_tmp"
  fi
}
trap finish EXIT

preflight_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
"$preflight_pg_bin/initdb" -D "$preflight_tmp/data" --no-locale --encoding=UTF8 --auth=trust >"$preflight_tmp/initdb.log"
"$preflight_pg_bin/pg_ctl" -D "$preflight_tmp/data" -l "$preflight_tmp/postgres.log" \
  -o "-h 127.0.0.1 -p $preflight_port -k $preflight_tmp" -w start >/dev/null
preflight_started=true
export USERMODEL_PREFLIGHT_FIXTURE_DSN="postgres://$(id -un)@127.0.0.1:$preflight_port/postgres?sslmode=disable"

preflight_files=(
  migrations/usermodel/001_facts.sql
  migrations/usermodel/002_coverage_verification.sql
  migrations/usermodel/002_ontology.sql
  migrations/usermodel/003_features.sql
  migrations/usermodel/004_serving.sql
  migrations/usermodel/005_covered_baseline.sql
  migrations/usermodel/006_covered_snapshot.sql
)
for preflight_file in "${preflight_files[@]}"; do
  "$preflight_pg_bin/psql" -X -v ON_ERROR_STOP=1 "$USERMODEL_PREFLIGHT_FIXTURE_DSN" \
    -f "$preflight_file" >"$preflight_tmp/migration.log"
done
GOMAXPROCS=2 go run -mod=readonly -p=2 ./cmd/usermodel-subjectref-preflight \
  --run --mode catalog --dsn-env USERMODEL_PREFLIGHT_FIXTURE_DSN \
  >"$preflight_tmp/catalog.json" 2>"$preflight_tmp/cli.log"
python3 - "$preflight_tmp/catalog.json" "internal/usermodel/preflight/contract.json" "${preflight_files[@]}" <<'PY'
import hashlib
import json
import pathlib
import sys

catalog_path, output_path, *source_files = sys.argv[1:]
catalog = json.loads(pathlib.Path(catalog_path).read_text())
if len(catalog['tables']) != 23:
    raise SystemExit('isolated catalog does not contain 23 scoped tables')
source = hashlib.sha256()
for name in source_files:
    source.update(name.encode())
    source.update(b'\0')
    source.update(pathlib.Path(name).read_bytes())
    source.update(b'\0')
pathlib.Path(output_path).write_text(json.dumps({
    'source_sha256': source.hexdigest(), 'catalog': catalog,
}, ensure_ascii=False, indent=2, sort_keys=True) + '\n')
PY
