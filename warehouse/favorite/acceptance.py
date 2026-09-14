"""Freeze a verified real RTW/DC/PG favorite fixture through isolated CH/dbt/S3."""
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
sys.path.insert(0, str(WAREHOUSE / "scripts"))
from engine import ClickHouse  # noqa: E402
from local_server import start as start_clickhouse  # noqa: E402
from local_s3 import start as start_s3  # noqa: E402


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode()


def sha(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--ods", required=True)
    args = parser.parse_args()
    root, runtime = Path(args.output).resolve(), Path(args.runtime).resolve()
    root.mkdir(parents=True, exist_ok=False)
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise RuntimeError(f"locked runtime missing: {name}")
    source = Path(args.ods).read_bytes()
    rows = [json.loads(line) for line in source.splitlines()]
    if len(rows) != 2 or [r["source_offset"] for r in rows] != [1, 2] or \
            [r["operation"] for r in rows] != ["assert", "retract"] or \
            len({r["favorite_id"] for r in rows}) != 1 or \
            rows[0]["event_id"] != rows[1]["predecessor_event_id"]:
        raise AssertionError("RTW/DC ODS differs from hand-calculated two-version source")
    for r in rows:
        if r["target_revision"] != "article-shared-authority:r1" or r["subject_id"] != "1001" or \
                r["favorite_id"] != "9007199254741993" or not r["event_spec"] or not r["technical_receipt"]:
            raise AssertionError("RTW identity, high identifier, revision or source proof was lost")
    ch_proc = s3_proc = None
    try:
        ch_proc, endpoint = start_clickhouse(runtime / "clickhouse", root / "clickhouse")
        s3_proc, prefix = start_s3(runtime / "weed", root / "seaweed")
        ch = ClickHouse(endpoint)
        ch.query("CREATE DATABASE favorite_landing")
        ch.query((HERE / "landing.sql").read_text().format(database="favorite_landing"))
        s3_url = f"{prefix}/warehouse-favorite/ods/{sha(source)}.jsonl"
        with urllib.request.urlopen(urllib.request.Request(s3_url, data=source, method="PUT"), timeout=30):
            pass
        with urllib.request.urlopen(s3_url, timeout=30) as response:
            if response.read() != source:
                raise AssertionError("S3 immutable ODS source differs")
        ch.query("INSERT INTO favorite_landing.ods_favorite_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
        dbt_out = root / "dbt"
        env = os.environ.copy()
        env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
                   WAREHOUSE_GENERATION_SCHEMA="favorite_gen_v1", DBT_SEND_ANONYMOUS_USAGE_STATS="false", DBT_USE_COLORS="false")
        with (root / "dbt.log").open("wb") as log:
            subprocess.run([str(runtime / ".venv/bin/dbt"), "build", "--project-dir", str(HERE),
                            "--profiles-dir", str(WAREHOUSE / "environment"), "--target-path", str(dbt_out),
                            "--log-path", str(root / "dbt_logs"), "--vars", json.dumps({"favorite_landing_schema": "favorite_landing"}),
                            "--no-partial-parse"], stdout=log, stderr=subprocess.STDOUT, env=env, check=True, timeout=180)
        facts = ch.rows("SELECT source_offset,operation,favorite_id,folder_id,favorite_state_delta,active_after,target_revision,available_at "
                        "FROM favorite_gen_v1.dwd_favorite_transition ORDER BY source_offset")
        if [(r["operation"], r["favorite_state_delta"], r["active_after"]) for r in facts] != \
                [("assert", 1, 1), ("retract", -1, 0)]:
            raise AssertionError(f"favorite transitions differ from hand calculation: {facts}")
        if any(r["target_revision"] != "article-shared-authority:r1" or
               r["favorite_id"] != "9007199254741993" or r["folder_id"] != "9007199254741991"
               for r in facts):
            raise AssertionError("DWD target revision or high Snowflake identifier changed")
        dwd = ch.query("SELECT * FROM favorite_gen_v1.dwd_favorite_transition ORDER BY source_offset FORMAT JSONEachRow")
        dwd_url = f"{prefix}/warehouse-favorite/dwd/{sha(dwd)}.jsonl"
        with urllib.request.urlopen(urllib.request.Request(dwd_url, data=dwd, method="PUT"), timeout=30):
            pass
        with urllib.request.urlopen(dwd_url, timeout=30) as response:
            if response.read() != dwd:
                raise AssertionError("S3 DWD archive differs")
        manifest = {"status": "passed", "evidence_level": "L2_isolated_real_RTW_DC_source",
                    "source_producer": "rtw.community.favorite", "warehouse_consumer": "btw-warehouse-favorite",
                    "source_offsets": [1, 2], "source_event_hashes": [r["source_event_hash"] for r in rows],
                    "ods_sha256": sha(source), "ods_s3_url": s3_url,
                    "dwd_sha256": sha(dwd), "dwd_s3_url": dwd_url,
                    "favorite_transitions": 2, "active_after": 0,
                    "recommendation_exposure_denominator": None, "recommendation_mature_label": None,
                    "model_or_experiment_effect": None}
        (root / "manifest.json").write_bytes(canonical(manifest) + b"\n")
        print(json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2), flush=True)
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
