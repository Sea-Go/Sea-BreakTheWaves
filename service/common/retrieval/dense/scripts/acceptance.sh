#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd -- "$script_dir/../../../.." && pwd)
cd "$repo_dir"
go test ./internal/retrieval/dense
go test -race ./internal/retrieval/dense
go vet ./internal/retrieval/dense
if [[ "${1:-}" == "--lite" ]]; then
  evidence_dir=$(mktemp -d -t sea-dense-acceptance)
  uv venv --python 3.12 "$evidence_dir/.venv"
  uv pip install --python "$evidence_dir/.venv/bin/python" -r "$script_dir/lite-requirements.txt"
  "$evidence_dir/.venv/bin/python" "$script_dir/lite_acceptance.py" "$evidence_dir/evidence"
  printf 'Retained acceptance evidence: %s\n' "$evidence_dir/evidence"
fi
