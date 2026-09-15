#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
: "${SEA_DC_ROOT:?SEA_DC_ROOT must point to the reviewed DataCenter checkout}"
: "${TEST_PREDICTION_DATABASE_URL:?TEST_PREDICTION_DATABASE_URL must point to an isolated local PostgreSQL admin database}"
[ "$#" -eq 1 ] || { printf '%s\n' "usage: $0 EVIDENCE_DIRECTORY" >&2; exit 2; }
evidence=$1
[ ! -e "$evidence" ] || { printf '%s\n' "evidence directory already exists: $evidence" >&2; exit 2; }
mkdir -m 700 "$evidence"

dc_commit=5702d3b22ccde706cc9e5661cc0aaee0514acf3a
actual_dc=$(git -C "$SEA_DC_ROOT" rev-parse HEAD)
[ "$actual_dc" = "$dc_commit" ] || {
	printf '%s\n' "DataCenter checkout must be exactly $dc_commit, got $actual_dc" >&2
	exit 2
}
btw_commit=$(git -C "$root" rev-parse HEAD)
python=$root/training/.venv/bin/python
[ -x "$python" ] || { printf '%s\n' "training Python 3.12 environment is missing" >&2; exit 2; }
[ "$($python -c 'import sys;print(f"{sys.version_info.major}.{sys.version_info.minor}")')" = 3.12 ] || {
	printf '%s\n' "training exporter requires Python 3.12" >&2
	exit 2
}

scratch=$(mktemp -d /private/tmp/sea-btw-prediction.XXXXXX)
runtime=$scratch/dc-runtime.json
done_file=$scratch/done
harness_pid=
cleanup() {
	if [ -n "$harness_pid" ] && kill -0 "$harness_pid" 2>/dev/null; then
		touch "$done_file"
		kill "$harness_pid" 2>/dev/null || true
		wait "$harness_pid" 2>/dev/null || true
	fi
	case "$scratch" in
	/private/tmp/sea-btw-prediction.*) find "$scratch" -depth -delete 2>/dev/null || true ;;
	esac
}
trap cleanup EXIT HUP INT TERM

PYTHONPATH=$root/training "$python" "$root/integration/prediction/generate_candidate.py" \
	"$evidence/candidate-build" >"$evidence/exporter-result.json" 2>"$evidence/exporter.log"

mkdir -p "$scratch/dc"
git -C "$SEA_DC_ROOT" archive "$dc_commit" | tar -xf - -C "$scratch/dc"
cp "$root/integration/prediction/dc_harness_test.go.tmpl" \
	"$scratch/dc/internal/api/prediction_external_harness_test.go"
(
	cd "$scratch/dc"
	TEST_PREDICTION_DATABASE_URL=$TEST_PREDICTION_DATABASE_URL \
	PREDICTION_HARNESS_RUNTIME=$runtime PREDICTION_HARNESS_DONE=$done_file \
	PREDICTION_HARNESS_DC_COMMIT=$dc_commit \
	go test -count=1 -run '^TestBTWPredictionExternalHarness$' -v ./internal/api
) >"$evidence/dc-harness.log" 2>&1 &
harness_pid=$!

attempt=0
while [ ! -s "$runtime" ]; do
	attempt=$((attempt + 1))
	if ! kill -0 "$harness_pid" 2>/dev/null; then
		wait "$harness_pid" || true
		printf '%s\n' "DataCenter prediction harness exited before readiness" >&2
		exit 1
	fi
	[ "$attempt" -lt 300 ] || { printf '%s\n' "DataCenter prediction harness readiness timed out" >&2; exit 1; }
	sleep 0.1
done

acceptance_code=0
BTW_PREDICTION_DC_RUNTIME=$runtime \
BTW_PREDICTION_CANDIDATE="$evidence/candidate-build/serving" \
BTW_PREDICTION_EVIDENCE_DIR=$evidence \
BTW_PREDICTION_EXPECTED_COMMIT=$btw_commit \
go test -race -count=1 -run '^TestPredictionCandidateThroughRealDC$' -v ./internal/recommend \
	>"$evidence/btw-test.log" 2>&1 || acceptance_code=$?
touch "$done_file"
harness_code=0
wait "$harness_pid" || harness_code=$?
harness_pid=
if [ "$acceptance_code" -ne 0 ] || [ "$harness_code" -ne 0 ]; then
	cat "$evidence/btw-test.log" >&2
	cat "$evidence/dc-harness.log" >&2
	exit 1
fi

(
	cd "$evidence"
	find . -type f ! -name checksums.txt -print | LC_ALL=C sort | while IFS= read -r file; do
		shasum -a 256 "$file"
	done >checksums.txt
)
printf '%s\n' "$evidence"
