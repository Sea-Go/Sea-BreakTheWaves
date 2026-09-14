"""Real RTW/DC single-source and two-subject authority fixture coverage gate."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
WAREHOUSE = HERE.parent
ROOT = WAREHOUSE.parent
sys.path.insert(0, str(WAREHOUSE / "scripts"))
from engine import ClickHouse  # noqa: E402
from local_server import start as start_clickhouse  # noqa: E402
from local_s3 import start as start_s3  # noqa: E402


def sha(body: bytes) -> str:
    return hashlib.sha256(body).hexdigest()


def run_go(log: Path, env: dict[str, str], test_name: str) -> None:
    with log.open("wb") as output:
        result = subprocess.run(["go", "test", "-mod=readonly", "-race", "-count=1", "-v",
                                 "-run", f"^{test_name}$", "./internal/warehouse/favoritesource"],
                                cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT)
    if result.returncode:
        raise RuntimeError(f"{test_name} failed; inspect {log}:\n{log.read_text()[-3500:]}")


def fixed_put(url: str, body: bytes) -> None:
    with urllib.request.urlopen(urllib.request.Request(url, data=body, method="PUT"), timeout=30):
        pass
    with urllib.request.urlopen(url, timeout=30) as response:
        if response.read() != body:
            raise AssertionError("S3 archive readback differs")


def preserve_ref_artifacts(output: Path, label: str, refs: dict) -> None:
    """Keep exact immutable bytes after the task-owned S3 listener stops."""
    archive = output / "artifacts" / label
    archive.mkdir(parents=True, exist_ok=False)
    for key, ref in refs.items():
        if key.startswith("G"):
            manifest_url = ref["event_index_url"].split("/event-index/", 1)[0] + \
                "/manifest/" + ref["manifest_sha256"] + ".json"
            objects = (("event-index.jsonl", ref["event_index_url"], ref["event_index_sha256"]),
                       ("batch-evidence.jsonl", ref["batch_evidence_url"], ref["batch_evidence_sha256"]),
                       ("manifest.json", manifest_url, ref["manifest_sha256"]))
        else:
            receipt_url = ref["sparse_index_url"].split("/subject-index/", 1)[0] + \
                "/subject-receipt/" + ref["receipt_sha256"] + ".json"
            objects = (("sparse-index.jsonl", ref["sparse_index_url"], ref["sparse_index_sha256"]),
                       ("subject-receipt.json", receipt_url, ref["receipt_sha256"]))
        for name, url, expected in objects:
            with urllib.request.urlopen(url, timeout=30) as response:
                body = response.read()
            if sha(body) != expected:
                raise AssertionError(f"{label}/{key}/{name} archive hash differs")
            directory = archive / key
            directory.mkdir(exist_ok=True)
            (directory / name).write_bytes(body)


def build_dwd(ch: ClickHouse, endpoint: str, prefix: str, runtime: Path,
              output: Path, label: str, expected: list[tuple[str, str, int]]) -> dict:
    source = (output / f"{label}-ods.jsonl").read_bytes()
    rows = [json.loads(line) for line in source.splitlines()]
    if len(rows) != len(expected) or [(r["subject_id"], r["operation"], r["source_offset"])
                                     for r in rows] != expected:
        raise AssertionError(f"{label} ODS differs from hand calculation")
    landing = f"coverage_{label}_landing"
    generation = f"coverage_{label}_dwd"
    ch.query(f"CREATE DATABASE {landing}")
    ch.query((WAREHOUSE / "favorite/landing.sql").read_text().format(database=landing))
    ods_url = f"{prefix}/warehouse-coverage/ods/{label}/{sha(source)}.jsonl"
    fixed_put(ods_url, source)
    ch.query(f"INSERT INTO {landing}.ods_favorite_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=generation, DBT_SEND_ANONYMOUS_USAGE_STATS="false",
               DBT_USE_COLORS="false")
    with (output / f"{label}-dbt.log").open("wb") as log:
        subprocess.run([str(runtime / ".venv/bin/dbt"), "build", "--project-dir", str(WAREHOUSE / "favorite"),
                        "--profiles-dir", str(WAREHOUSE / "environment"),
                        "--target-path", str(output / f"{label}-dbt-target"),
                        "--log-path", str(output / f"{label}-dbt-logs"),
                        "--vars", json.dumps({"favorite_landing_schema": landing}),
                        "--no-partial-parse"], env=env, stdout=log, stderr=subprocess.STDOUT,
                       check=True, timeout=180)
    facts = ch.rows(f"SELECT subject_id,operation,favorite_state_delta,source_offset "
                    f"FROM {generation}.dwd_favorite_transition ORDER BY source_offset")
    if [(f["subject_id"], f["operation"], f["source_offset"]) for f in facts] != expected:
        raise AssertionError(f"{label} DWD changed subject attribution")
    if [f["favorite_state_delta"] for f in facts] != [1 if e[1] == "assert" else -1 for e in expected]:
        raise AssertionError(f"{label} favorite transitions differ")
    dwd = ch.query(f"SELECT * FROM {generation}.dwd_favorite_transition ORDER BY source_offset FORMAT JSONEachRow")
    dwd_url = f"{prefix}/warehouse-coverage/dwd/{label}/{sha(dwd)}.jsonl"
    fixed_put(dwd_url, dwd)
    return {"ods_sha256": sha(source), "dwd_sha256": sha(dwd), "rows": len(facts)}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--ready", required=True)
    parser.add_argument("--pg-dsn", required=True)
    parser.add_argument("--postgres-bin", required=True)
    args = parser.parse_args()
    runtime, output = Path(args.runtime).resolve(), Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise RuntimeError(f"locked runtime dependency missing: {name}")
    # Ready file contains test credentials and is never printed or copied into reports.
    ready = json.loads(Path(args.ready).read_text())
    parsed = urllib.parse.urlsplit(args.pg_dsn)
    if parsed.path != "/postgres":
        raise RuntimeError("isolated PostgreSQL source database must be postgres")
    two_dsn = urllib.parse.urlunsplit(parsed._replace(path="/coverage_two"))
    subprocess.run([str(Path(args.postgres_bin) / "psql"), "-X", "-q", "-d", args.pg_dsn,
                    "-c", "CREATE DATABASE coverage_two"], check=True, capture_output=True)
    ch_proc = s3_proc = None
    try:
        ch_proc, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        s3_proc, prefix = start_s3(runtime / "weed", output / "seaweed")
        env = os.environ.copy()
        env.update(SEA_FACT_AUTHORITY_URL=ready["authority_url"], SEA_FACT_AUTHORITY_TOKEN=ready["authority_token"],
                   SEA_FACT_DC_URL=ready["dc_url"], SEA_FACT_DC_TOKEN=ready["dc_token"],
                   COVERAGE_REAL_DSN=args.pg_dsn, COVERAGE_TWO_DSN=two_dsn, COVERAGE_S3_PREFIX=prefix,
                   COVERAGE_REAL_REPORT=str(output / "real-ref.json"),
                   COVERAGE_TWO_REPORT=str(output / "two-ref.json"),
                   COVERAGE_REAL_ODS_OUTPUT=str(output / "real-ods.jsonl"),
                   COVERAGE_TWO_ODS_OUTPUT=str(output / "two-ods.jsonl"),
                   GOFLAGS="-p=2", GOMAXPROCS="2")
        run_go(output / "real-test.log", env, "TestCoveragePublisherRealRTW")
        run_go(output / "two-user-test.log", env, "TestCoveragePublisherTwoSubjects")
        ch = ClickHouse(endpoint)
        real = build_dwd(ch, endpoint, prefix, runtime, output, "real",
                         [("1001", "assert", 1), ("1001", "retract", 2)])
        two = build_dwd(ch, endpoint, prefix, runtime, output, "two",
                        [("1001", "assert", 1), ("1002", "assert", 2), ("1001", "retract", 3)])
        real_refs = json.loads((output / "real-ref.json").read_text())
        two_refs = json.loads((output / "two-ref.json").read_text())
        preserve_ref_artifacts(output, "real", real_refs)
        preserve_ref_artifacts(output, "two", two_refs)
        if real_refs["U2"]["event_count"] != 0 or \
                [two_refs[key]["event_count"] for key in ("U1", "U2", "U3")] != [2, 1, 0]:
            raise AssertionError("published subject slices differ from hand calculation")
        report = {"status": "passed", "real_source": {"evidence_level": "L2_real_isolated_RTW_DC_PG_CH_S3",
                                                   "global_W": 2, **real},
                  "two_subjects": {"evidence_level": "L2_isolated_synthetic_authority_real_PG_CH_S3",
                                   "global_W": 3, "u1_offsets": [1, 3], "u2_offsets": [2],
                                   "u3_offsets": [], **two},
                  "model_or_recommendation_effect": None, "exposure_or_label": None}
        (output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
        print(json.dumps(report, sort_keys=True, indent=2), flush=True)
    finally:
        for proc in (s3_proc, ch_proc):
            if proc is not None:
                proc.terminate()
                try:
                    proc.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=10)


if __name__ == "__main__":
    main()
