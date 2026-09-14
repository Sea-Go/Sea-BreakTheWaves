"""One isolated RTW/DC -> warehouse DWD -> PG FeatureSnapshot/Bundle gate."""
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


def run_go(root: Path, log: Path, env: dict[str, str], package: str, pattern: str) -> None:
    with log.open("wb") as output:
        result = subprocess.run(["go", "test", "-mod=readonly", "-race", "-count=1", "-v",
                                 "-run", pattern, package], cwd=root, env=env,
                                stdout=output, stderr=subprocess.STDOUT, check=False)
    if result.returncode:
        raise RuntimeError(f"{package} failed; inspect {log}:\n{log.read_text()[-3000:]}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--ready", required=True)
    parser.add_argument("--pg-dsn", required=True)
    args = parser.parse_args()
    runtime, output = Path(args.runtime).resolve(), Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise RuntimeError(f"locked runtime dependency missing: {name}")
    # The RTW fixture's ready file has mode 0600 and is never echoed.
    ready = json.loads(Path(args.ready).read_text())
    env = os.environ.copy()
    env.update({"SEA_FACT_AUTHORITY_URL": ready["authority_url"],
                "SEA_FACT_AUTHORITY_TOKEN": ready["authority_token"],
                "SEA_FACT_DC_URL": ready["dc_url"], "SEA_FACT_DC_TOKEN": ready["dc_token"],
                "SEA_FACT_ASSERT_EVENT_ID": ready["assert_event_id"],
                "SEA_FACT_RETRACT_EVENT_ID": ready["retract_event_id"],
                "FAVORITE_TEST_DSN": args.pg_dsn, "USERMODEL_TEST_POSTGRES_DSN": args.pg_dsn,
                "WAREHOUSE_FAVORITE_ODS_OUTPUT": str(output / "ods.jsonl"),
                "GOFLAGS": "-p=2", "GOMAXPROCS": "2"})
    ch_proc = s3_proc = None
    try:
        ch_proc, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        s3_proc, prefix = start_s3(runtime / "weed", output / "seaweed")
        env.update(USERMODEL_TEST_CH_URL=endpoint, USERMODEL_TEST_S3_PREFIX=prefix,
                   USERMODEL_TEST_DBT=str(runtime / ".venv/bin/dbt"))
        run_go(ROOT, output / "warehouse-test.log", env,
               "./internal/warehouse/favoritesource", "^TestRealRTWFavoriteWarehouse$")
        source = (output / "ods.jsonl").read_bytes()
        rows = [json.loads(line) for line in source.splitlines()]
        if len(rows) != 2 or [r["source_offset"] for r in rows] != [1, 2] or \
                [r["operation"] for r in rows] != ["assert", "retract"]:
            raise AssertionError("warehouse ODS did not preserve two RTW/DC source versions")
        ch = ClickHouse(endpoint)
        ch.query("CREATE DATABASE favorite_landing")
        ch.query((WAREHOUSE / "favorite/landing.sql").read_text().format(database="favorite_landing"))
        ods_url = f"{prefix}/warehouse-favorite/ods/{sha(source)}.jsonl"
        with urllib.request.urlopen(urllib.request.Request(ods_url, data=source, method="PUT"), timeout=30):
            pass
        with urllib.request.urlopen(ods_url, timeout=30) as response:
            if response.read() != source:
                raise AssertionError("S3 favorite ODS source changed")
        ch.query("INSERT INTO favorite_landing.ods_favorite_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
        dbt_env = env.copy()
        dbt_env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
                       WAREHOUSE_GENERATION_SCHEMA="favorite_candidate_dwd",
                       DBT_SEND_ANONYMOUS_USAGE_STATS="false", DBT_USE_COLORS="false")
        with (output / "dwd-dbt.log").open("wb") as log:
            subprocess.run([str(runtime / ".venv/bin/dbt"), "build", "--project-dir", str(WAREHOUSE / "favorite"),
                            "--profiles-dir", str(WAREHOUSE / "environment"),
                            "--target-path", str(output / "dwd-dbt"), "--log-path", str(output / "dwd-dbt-logs"),
                            "--vars", json.dumps({"favorite_landing_schema": "favorite_landing"}),
                            "--no-partial-parse"], stdout=log, stderr=subprocess.STDOUT,
                           env=dbt_env, check=True, timeout=180)
        facts = ch.rows("SELECT favorite_id,folder_id,target_revision,operation,favorite_state_delta,active_after "
                        "FROM favorite_candidate_dwd.dwd_favorite_transition ORDER BY source_offset")
        if [(f["operation"], f["favorite_state_delta"], f["active_after"]) for f in facts] != \
                [("assert", 1, 1), ("retract", -1, 0)] or \
                any(f["favorite_id"] != "9007199254741993" or
                    f["target_revision"] != "article-shared-authority:r1" for f in facts):
            raise AssertionError("real DWD lost high ID, target revision or reversible state")
        dwd = ch.query("SELECT * FROM favorite_candidate_dwd.dwd_favorite_transition "
                       "ORDER BY source_offset FORMAT JSONEachRow")
        (output / "dwd.jsonl").write_bytes(dwd)
        dwd_url = f"{prefix}/warehouse-favorite/dwd/{sha(dwd)}.jsonl"
        with urllib.request.urlopen(urllib.request.Request(dwd_url, data=dwd, method="PUT"), timeout=30):
            pass
        with urllib.request.urlopen(dwd_url, timeout=30) as response:
            if response.read() != dwd:
                raise AssertionError("S3 frozen DWD source changed")
        env.update(FAVORITE_CANDIDATE_DWD_URL=dwd_url, FAVORITE_CANDIDATE_DWD_SHA256=sha(dwd))
        run_go(ROOT, output / "feature-test.log", env,
               "./internal/warehouse/featurebaseline",
               "^(TestRealFavoriteCandidateFromDWD|TestGlobalOffsetIsNotSubjectSequence|TestFavoriteDWDPrefixAndOrphan)$")
        report = {"status": "passed", "evidence_level": "L2_isolated_real_RTW_DC_CH_S3_PG_candidate",
                  "activation": "default_off", "ods_sha256": sha(source), "dwd_sha256": sha(dwd),
                  "source_offsets": [1, 2], "source_subject": "rtw.identity/platform/1001",
                  "favorite_count_generations": [1, 0], "model_effect": None,
                  "impression_denominator": None, "mature_label": None,
                  "mixed_subject_global_offset": "deterministic_gap_counterexample"}
        (output / "report.json").write_text(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
        print(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2), flush=True)
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
