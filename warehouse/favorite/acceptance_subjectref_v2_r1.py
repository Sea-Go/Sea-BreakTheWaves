"""Isolated native CH/dbt/S3 gate for the historic-v1 SubjectRef sidecar read."""
from __future__ import annotations

import argparse
import copy
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.request
from pathlib import Path
import jsonschema

HERE = Path(__file__).resolve().parent
WAREHOUSE = HERE.parent
sys.path.insert(0, str(WAREHOUSE / "scripts"))
from engine import ClickHouse  # noqa: E402
from local_server import start as start_clickhouse  # noqa: E402
from local_s3 import start as start_s3  # noqa: E402
from projection_subjectref_v2_r1 import (  # noqa: E402
    ODS_FIELDS, canonical, digest, fixture_mapping, parse_lines, project,
)


def fixed_put(url: str, body: bytes) -> None:
    with urllib.request.urlopen(urllib.request.Request(url, data=body, method="PUT"), timeout=30):
        pass
    with urllib.request.urlopen(url, timeout=30) as response:
        if response.read() != body:
            raise AssertionError("content-addressed object readback differs")


def build(dbt: Path, endpoint: str, output: Path, schema: str,
          landing: str, v2: bool) -> None:
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=schema,
               DBT_SEND_ANONYMOUS_USAGE_STATS="false", DBT_USE_COLORS="false")
    vars = {"favorite_landing_schema": landing, "favorite_subjectref_v2_read": v2}
    with (output / f"dbt-{schema}.log").open("wb") as log:
        result = subprocess.run([str(dbt), "build", "--project-dir", str(HERE),
                                 "--profiles-dir", str(WAREHOUSE / "environment"),
                                 "--target-path", str(output / f"target-{schema}"),
                                 "--log-path", str(output / f"logs-{schema}"),
                                 "--vars", json.dumps(vars), "--no-partial-parse"],
                                env=env, stdout=log, stderr=subprocess.STDOUT, timeout=180)
    if result.returncode:
        raise AssertionError(f"dbt {schema} failed ({result.returncode}); inspect dbt log")


def fixture_second_uid(source: bytes) -> bytes:
    rows = parse_lines(source, ODS_FIELDS)
    if len(rows) != 2 or [r["operation"] for r in rows] != ["assert", "retract"]:
        raise ValueError("two-subject fixture needs a true two-version source")
    copy_rows = []
    assert_id = rows[0]["event_id"] + ".subject2"
    for index, source_row in enumerate(rows):
        row = copy.deepcopy(source_row)
        row["source_offset"] = index + 3
        row["subject_id"] = "1002"
        row["event_id"] = assert_id if index == 0 else rows[1]["event_id"] + ".subject2"
        row["predecessor_event_id"] = "" if index == 0 else assert_id
        spec = json.loads(row["event_spec"])
        spec["event_id"] = row["event_id"]
        spec["operation_id"] = row["event_id"]
        spec["payload"]["event_id"] = row["event_id"]
        spec["payload"]["subject_ref"]["subject_id"] = "1002"
        row["event_spec"] = canonical(spec).decode()
        row["source_event_hash"] = digest(canonical(spec))
        receipt = json.loads(row["technical_receipt"])
        receipt["offset"] = row["source_offset"]
        receipt["event_id"] = row["event_id"]
        receipt["input_hash"] = row["source_event_hash"]
        receipt["receipt_id"] += ".fixture.subject2"
        row["technical_receipt"] = canonical(receipt).decode()
        copy_rows.append(row)
    # This is an isolated SQL collision fixture, not a second RTW business fact.
    return b"".join(canonical(row) + b"\n" for row in rows + copy_rows)


def run_case(ch: ClickHouse, endpoint: str, prefix: str, runtime: Path,
             output: Path, label: str, source: bytes, expected_uid: list[str]) -> dict:
    landing = f"favorite_{label}_landing"
    old_gen = f"favorite_{label}_old"
    new_gen = f"favorite_{label}_v2_r1"
    ch.query(f"CREATE DATABASE {landing}")
    ch.query((HERE / "landing.sql").read_text().format(database=landing))
    old_create = ch.query(f"SHOW CREATE TABLE {landing}.ods_favorite_event")
    old_url = f"{prefix}/warehouse-favorite/ods/v1/{digest(source)}.jsonl"
    fixed_put(old_url, source)
    ch.query(f"INSERT INTO {landing}.ods_favorite_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
    build(runtime / ".venv/bin/dbt", endpoint, output, old_gen, landing, False)
    old_dwd = ch.query(f"SELECT * FROM {old_gen}.dwd_favorite_transition ORDER BY source_offset FORMAT JSONEachRow")
    old_dwd_sha = digest(old_dwd)

    mapping = fixture_mapping(source)  # Acceptance-only; production input comes from approved PG sidecar.
    projected = project(source, mapping)
    contract = json.loads((HERE / "contracts/ods-subjectref-v2-r1.schema.json").read_text())
    for row in parse_lines(projected, ODS_FIELDS + ("issuer", "subject_uid", "origin_ods_sha256")):
        jsonschema.validate(row, contract)
    new_url = f"{prefix}/warehouse-favorite/ods/subjectref-v2-r1/{digest(projected)}.jsonl"
    fixed_put(new_url, projected)
    ch.query((HERE / "landing_subjectref_v2_r1.sql").read_text().format(database=landing))
    new_create = ch.query(f"SHOW CREATE TABLE {landing}.ods_favorite_event_subjectref_v2_r1")
    ch.query(f"INSERT INTO {landing}.ods_favorite_event_subjectref_v2_r1 SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", projected)
    build(runtime / ".venv/bin/dbt", endpoint, output, new_gen, landing, True)
    old_again = ch.query(f"SELECT * FROM {new_gen}.dwd_favorite_transition ORDER BY source_offset FORMAT JSONEachRow")
    if old_again != old_dwd or ch.query(f"SHOW CREATE TABLE {landing}.ods_favorite_event") != old_create:
        raise AssertionError("old ODS DDL or old DWD bytes changed")
    v2_rows = ch.rows(f"SELECT producer,source_offset,event_id,issuer,subject_uid,operation,"
                      f" favorite_id,folder_id,representation_version,favorite_state_delta,active_after,"
                      f" row_contract FROM {new_gen}.dwd_favorite_transition_subjectref_v2_r1"
                      f" ORDER BY source_offset")
    old_rows = parse_lines(source, ODS_FIELDS)
    if len(v2_rows) != len(old_rows) or [r["subject_uid"] for r in v2_rows] != expected_uid:
        raise AssertionError("v1/v2 representations doubled or crossed subjects")
    if [(r["favorite_state_delta"], r["active_after"]) for r in v2_rows] != \
            [(1 if r["operation"] == "assert" else -1,
              1 if r["operation"] == "assert" else 0) for r in old_rows]:
        raise AssertionError("favorite assert/retract transition doubled")
    if any(r["issuer"] != "rtw.identity" or r["representation_version"] != 2 or
           r["row_contract"] != "favorite.transition.subjectref.v2.r1" or
           r["favorite_id"] != old_rows[i]["favorite_id"] or
           r["folder_id"] != old_rows[i]["folder_id"] for i, r in enumerate(v2_rows)):
        raise AssertionError("canonical subject or immutable favorite key changed")
    v2_dwd = ch.query(f"SELECT * FROM {new_gen}.dwd_favorite_transition_subjectref_v2_r1"
                       f" ORDER BY source_offset FORMAT JSONEachRow")
    dwd_url = f"{prefix}/warehouse-favorite/dwd/subjectref-v2-r1/{digest(v2_dwd)}.jsonl"
    fixed_put(dwd_url, v2_dwd)
    (output / f"{label}-old-dwd.jsonl").write_bytes(old_dwd)
    (output / f"{label}-v2-dwd.jsonl").write_bytes(v2_dwd)
    (output / f"{label}-ods-v1.jsonl").write_bytes(source)
    (output / f"{label}-ods-v2-r1.jsonl").write_bytes(projected)
    (output / f"{label}-mapping-fixture.jsonl").write_bytes(mapping)
    return {"source_kind": "real_RTW_DC_PG" if label == "real" else "synthetic_second_UID",
            "old_ods_sha256": digest(source), "old_dwd_sha256": old_dwd_sha,
            "new_ods_sha256": digest(projected), "new_dwd_sha256": digest(v2_dwd),
            "new_ods_url": new_url, "new_dwd_url": dwd_url,
            "old_ods_ddl_sha256": digest(old_create), "new_ods_ddl_sha256": digest(new_create),
            "logical_transitions": len(v2_rows), "subject_uids": expected_uid,
            "active_after": v2_rows[-1]["active_after"]}


def reject_bad_projection(source: bytes) -> list[str]:
    mapping_rows = parse_lines(fixture_mapping(source),
                               ("producer", "source_offset", "event_id", "authority_id",
                                "tenant_id", "subject_id", "issuer", "subject_uid"))
    checks = []
    for label, wrong in (("wrong_issuer", dict(mapping_rows[0], issuer="other")),
                         ("cross_subject", dict(mapping_rows[0], subject_uid="1002")),
                         ("wrong_anchor", dict(mapping_rows[0], event_id="wrong"))):
        rows = [wrong] + mapping_rows[1:]
        try:
            project(source, b"".join(canonical(r) + b"\n" for r in rows))
        except ValueError:
            checks.append(label)
        else:
            raise AssertionError(f"projection accepted {label}")
    old_rows = parse_lines(source, ODS_FIELDS)
    for label, changed in (("wrong_event_spec", dict(old_rows[0], event_spec="{}")),
                           ("wrong_receipt", dict(old_rows[0], technical_receipt="{}"))):
        rows = [changed] + old_rows[1:]
        try:
            project(b"".join(canonical(r) + b"\n" for r in rows), fixture_mapping(source))
        except ValueError:
            checks.append(label)
        else:
            raise AssertionError(f"projection accepted {label}")
    return checks


def reject_ch_hash_conflict(ch: ClickHouse, endpoint: str, runtime: Path,
                            output: Path, source: bytes) -> str:
    landing = "favorite_hash_conflict_landing"
    ch.query(f"CREATE DATABASE {landing}")
    ch.query((HERE / "landing.sql").read_text().format(database=landing))
    ch.query((HERE / "landing_subjectref_v2_r1.sql").read_text().format(database=landing))
    ch.query(f"INSERT INTO {landing}.ods_favorite_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
    good = project(source, fixture_mapping(source))
    bad_rows = parse_lines(good, ODS_FIELDS + ("issuer", "subject_uid", "origin_ods_sha256"))
    bad_rows[0]["source_event_hash"] = "f" * 64
    bad = b"".join(canonical(r) + b"\n" for r in bad_rows)
    ch.query(f"INSERT INTO {landing}.ods_favorite_event_subjectref_v2_r1 SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", bad)
    schema = "favorite_hash_conflict_candidate"
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=schema,
               DBT_SEND_ANONYMOUS_USAGE_STATS="false", DBT_USE_COLORS="false")
    vars = {"favorite_landing_schema": landing, "favorite_subjectref_v2_read": True}
    log_path = output / "dbt-hash-conflict-rejected.log"
    with log_path.open("wb") as log:
        result = subprocess.run([str(runtime / ".venv/bin/dbt"), "build",
                                 "--project-dir", str(HERE), "--profiles-dir",
                                 str(WAREHOUSE / "environment"), "--target-path",
                                 str(output / "target-hash-conflict"), "--log-path",
                                 str(output / "logs-hash-conflict"), "--vars",
                                 json.dumps(vars), "--no-partial-parse"],
                                env=env, stdout=log, stderr=subprocess.STDOUT,
                                timeout=180)
    if (result.returncode == 0 or
            "FAIL 1 favorite_subjectref_v2_source_integrity" not in log_path.read_text()):
        raise AssertionError("hash-conflict candidate did not fail its source test")
    return "source_event_hash_mismatch_rejected_by_dbt"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--ods", required=True)
    args = parser.parse_args()
    runtime, output = Path(args.runtime).resolve(), Path(args.output).resolve()
    if output.exists():
        raise ValueError("evidence directory already exists")
    output.mkdir(parents=True)
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise ValueError(f"locked runtime missing: {name}")
    source = Path(args.ods).read_bytes()
    rows = parse_lines(source, ODS_FIELDS)
    if (len(rows) != 2 or [r["source_offset"] for r in rows] != [1, 2] or
            [r["operation"] for r in rows] != ["assert", "retract"]):
        raise ValueError("real fixture must be a contiguous assert/retract pair")
    proc_ch = proc_s3 = None
    try:
        proc_ch, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        proc_s3, prefix = start_s3(runtime / "weed", output / "seaweed")
        ch = ClickHouse(endpoint)
        real = run_case(ch, endpoint, prefix, runtime, output, "real", source,
                        [r["subject_id"] for r in rows])
        synthetic = fixture_second_uid(source)
        two = run_case(ch, endpoint, prefix, runtime, output, "two_uid", synthetic,
                       ["1001", "1001", "1002", "1002"])
        if (len({r["favorite_id"] for r in parse_lines(synthetic, ODS_FIELDS)}) != 1 or
                len({r["folder_id"] for r in parse_lines(synthetic, ODS_FIELDS)}) != 1):
            raise AssertionError("same favorite/folder cross-subject test was not exercised")
        rejected = reject_bad_projection(source)
        rejected.append(reject_ch_hash_conflict(ch, endpoint, runtime, output, source))
        report = {"status": "passed", "scope": "isolated_native_CH_dbt_S3_historic_v1_sidecar_read",
                  "mapping_source": "explicit_test_fixture_not_PG_export",
                  "producer": "rtw.community.favorite", "real": real, "two_uid": two,
                  "rejected": rejected,
                  "old_active_pointer_changed": False, "recommendation_label": None,
                  "production_catalog_checked": False, "production_read_switch": "default_off"}
        (output / "report.json").write_bytes(canonical(report) + b"\n")
        print(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2), flush=True)
    finally:
        for proc in (proc_s3, proc_ch):
            if proc is not None:
                proc.terminate()
                try:
                    proc.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=10)


if __name__ == "__main__":
    main()
