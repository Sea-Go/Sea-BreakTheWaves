#!/usr/bin/env bash
# Test module boundaries explicitly; the root pattern does not enter legacy modules.
set -euo pipefail
cd "$(dirname "$0")/.."
selection="${1:-all}"
case "$selection" in
  all) modules=(. recommendation agent_v2 agent_v3) ;;
  root) modules=(.) ;;
  recommendation|agent_v2|agent_v3) modules=("$selection") ;;
  *) echo "Usage: bash scripts/test-go-modules.sh [all|root|recommendation|agent_v2|agent_v3]" >&2; exit 2 ;;
esac
for module in "${modules[@]}"; do
  echo "Testing Go module: $module"
  (cd "$module"; go test ./...; go vet ./...)
done
