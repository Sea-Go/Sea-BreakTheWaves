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
    strict_json,
)


def fixed_put(url: str, body: bytes) -> None:
    with urllib.request.urlopen(urllib.request.Request(url, data=body, method="PUT"), timeout=30):
        pass
    with urllib.request.urlopen(url, timeout=30) as response:
        if response.read() != body:
            raise AssertionError("content-addressed object readback differs")


def build(dbt: Path, endpoint: str, output: Path, schema: str,
          landing: str, v2: bool, through: int) -> None:
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=schema,
               DBT_SEND_ANONYMOUS_USAGE_STATS="false", DBT_USE_COLORS="false")
    vars = {"favorite_landing_schema": landing, "favorite_subjectref_v2_read": v2,
            "favorite_subjectref_v2_through_offset": through}
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
             output: Path, label: str, source: bytes, expected_uid: list[str],
             mapping: bytes | None = None) -> dict:
    landing = f"favorite_{label}_landing"
    old_gen = f"favorite_{label}_old"
    new_gen = f"favorite_{label}_v2_r1"
    ch.query(f"CREATE DATABASE {landing}")
    ch.query((HERE / "landing.sql").read_text().format(database=landing))
    old_create = ch.query(f"SHOW CREATE TABLE {landing}.ods_favorite_event")
    old_url = f"{prefix}/warehouse-favorite/ods/v1/{digest(source)}.jsonl"
    fixed_put(old_url, source)
    ch.query(f"INSERT INTO {landing}.ods_favorite_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
    build(runtime / ".venv/bin/dbt", endpoint, output, old_gen, landing, False,
          len(parse_lines(source, ODS_FIELDS)))
    old_dwd = ch.query(f"SELECT * FROM {old_gen}.dwd_favorite_transition ORDER BY source_offset FORMAT JSONEachRow")
    old_dwd_sha = digest(old_dwd)

    mapping_from_pg = mapping is not None
    if mapping is None:
        mapping = fixture_mapping(source)  # Isolated CH fixture, never a PG export.
    projected = project(source, mapping)
    contract = json.loads((HERE / "contracts/ods-subjectref-v2-r1.schema.json").read_text())
    for row in parse_lines(projected, ODS_FIELDS + ("issuer", "subject_uid", "origin_ods_sha256")):
        jsonschema.validate(row, contract)
    new_url = f"{prefix}/warehouse-favorite/ods/subjectref-v2-r1/{digest(projected)}.jsonl"
    fixed_put(new_url, projected)
    ch.query((HERE / "landing_subjectref_v2_r1.sql").read_text().format(database=landing))
    new_create = ch.query(f"SHOW CREATE TABLE {landing}.ods_favorite_event_subjectref_v2_r1")
    ch.query(f"INSERT INTO {landing}.ods_favorite_event_subjectref_v2_r1 SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", projected)
    build(runtime / ".venv/bin/dbt", endpoint, output, new_gen, landing, True,
          len(parse_lines(source, ODS_FIELDS)))
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
    (output / f"{label}-mapping-{'pg' if mapping_from_pg else 'fixture'}.jsonl").write_bytes(mapping)
    return {"source_kind": ("local_PG_official_locked_preflight" if mapping_from_pg and
                            label == "real" else "real_RTW_DC_PG" if label == "real"
                            else "synthetic_second_UID"),
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
    for label, bad in (("duplicate_JSON_key", b'{"source_offset":1,"source_offset":2}\n'),
                       ("boolean_offset", source.replace(b'"source_offset":1',
                                                         b'"source_offset":true', 1)),
                       ("float_offset", source.replace(b'"source_offset":1',
                                                       b'"source_offset":1.0', 1)),
                       ("NaN_offset", source.replace(b'"source_offset":1',
                                                     b'"source_offset":NaN', 1))):
        try:
            parse_lines(bad, ODS_FIELDS)
        except ValueError:
            checks.append(label)
        else:
            raise AssertionError(f"JSONL accepted {label}")
    try:
        strict_json('{"event_id":"first","event_id":"second"}')
    except ValueError:
        checks.append("duplicate_EventSpec_key")
    else:
        raise AssertionError("EventSpec accepted a duplicate key")
    return checks


def reject_ch_candidate(ch: ClickHouse, endpoint: str, runtime: Path,
                        output: Path, label: str, source: bytes, projected: bytes,
                        through: int) -> str:
    landing = f"favorite_{label}_landing"
    ch.query(f"CREATE DATABASE {landing}")
    ch.query((HERE / "landing.sql").read_text().format(database=landing))
    ch.query((HERE / "landing_subjectref_v2_r1.sql").read_text().format(database=landing))
    ch.query(f"INSERT INTO {landing}.ods_favorite_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
    ch.query(f"INSERT INTO {landing}.ods_favorite_event_subjectref_v2_r1 SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", projected)
    schema = f"favorite_{label}_candidate"
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=schema,
               DBT_SEND_ANONYMOUS_USAGE_STATS="false", DBT_USE_COLORS="false")
    vars = {"favorite_landing_schema": landing, "favorite_subjectref_v2_read": True,
            "favorite_subjectref_v2_through_offset": through}
    log_path = output / f"dbt-{label}-rejected.log"
    with log_path.open("wb") as log:
        result = subprocess.run([str(runtime / ".venv/bin/dbt"), "build",
                                 "--project-dir", str(HERE), "--profiles-dir",
                                 str(WAREHOUSE / "environment"), "--target-path",
                                 str(output / f"target-{label}"), "--log-path",
                                 str(output / f"logs-{label}"), "--vars",
                                 json.dumps(vars), "--no-partial-parse"],
                                env=env, stdout=log, stderr=subprocess.STDOUT,
                                timeout=180)
    text = log_path.read_text()
    if (result.returncode == 0 or
            "favorite_subjectref_v2_source_integrity" not in text or
            "FAIL 1 favorite_subjectref_v2_source_integrity" not in text or
            "SKIP relation " + schema + ".dwd_favorite_transition_subjectref_v2_r1" not in text):
        raise AssertionError(f"{label} candidate did not fail before its v2 DWD")
    return label + "_rejected_by_dbt"


def reject_ch_incomplete_sources(ch: ClickHouse, endpoint: str, runtime: Path,
                                 output: Path, source: bytes, mapping: bytes) -> list[str]:
    old = parse_lines(source, ODS_FIELDS)
    projected = parse_lines(project(source, mapping), ODS_FIELDS +
                            ("issuer", "subject_uid", "origin_ods_sha256"))
    bad_hash = [dict(row) for row in projected]
    bad_hash[0]["source_event_hash"] = "f" * 64
    serialize = lambda rows: b"".join(canonical(r) + b"\n" for r in rows)
    gap_index = 1 if len(old) > 2 else len(old) - 1
    old_gap = [r for i, r in enumerate(old) if i != gap_index]
    projected_gap = [r for i, r in enumerate(projected) if i != gap_index]
    return [
        reject_ch_candidate(ch, endpoint, runtime, output, "hash_conflict",
                            source, serialize(bad_hash), len(old)),
        reject_ch_candidate(ch, endpoint, runtime, output, "missing_mapping",
                            source, serialize(projected[:-1]), len(old)),
        reject_ch_candidate(ch, endpoint, runtime, output, "prefix_gap",
                            serialize(old_gap), serialize(projected_gap), len(old)),
    ]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--ods", required=True)
    parser.add_argument("--mapping")
    parser.add_argument("--ready")
    parser.add_argument("--skip-synthetic", action="store_true")
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
    if (len(rows) < 2 or [r["source_offset"] for r in rows] != list(range(1, len(rows) + 1))):
        raise ValueError("frozen old source must be a contiguous offset prefix")
    if bool(args.mapping) != bool(args.ready):
        raise ValueError("official PG mapping and ready report must be supplied together")
    ready = None
    mapping = None
    if args.mapping:
        ready = json.loads(Path(args.ready).read_text())
        mapping = Path(args.mapping).read_bytes()
        if (ready.get("status") != "official_apply_passed" or
                ready.get("through_offset") != len(rows) or
                ready.get("old_ods_sha256") != digest(source) or
                ready.get("mapping_sha256") != digest(mapping) or
                len(ready.get("preflight_snapshot_sha256", "")) != 64 or
                len(ready.get("verified_coverage_roots", [])) != 1 or
                len(ready.get("objects", [])) != 5):
            raise ValueError("official locked PG handoff does not match frozen prefix")
        for obj in ready["objects"]:
            parsed = urllib.parse.urlsplit(obj["url"])
            if parsed.hostname != "127.0.0.1" or len(obj["sha256"]) != 64:
                raise ValueError("official PG coverage object handoff must be isolated loopback")
            with urllib.request.urlopen(obj["url"], timeout=10) as response:
                if digest(response.read()) != obj["sha256"]:
                    raise ValueError("official PG coverage original object SHA differs")
    proc_ch = proc_s3 = None
    try:
        proc_ch, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        proc_s3, prefix = start_s3(runtime / "weed", output / "seaweed")
        ch = ClickHouse(endpoint)
        real = run_case(ch, endpoint, prefix, runtime, output, "real", source,
                        [r["subject_id"] for r in rows], mapping)
        two = None
        if not args.skip_synthetic:
            synthetic = fixture_second_uid(source)
            two = run_case(ch, endpoint, prefix, runtime, output, "two_uid", synthetic,
                           ["1001", "1001", "1002", "1002"])
            if (len({r["favorite_id"] for r in parse_lines(synthetic, ODS_FIELDS)}) != 1 or
                    len({r["folder_id"] for r in parse_lines(synthetic, ODS_FIELDS)}) != 1):
                raise AssertionError("same favorite/folder cross-subject test was not exercised")
        rejected = reject_bad_projection(source)
        rejected.extend(reject_ch_incomplete_sources(ch, endpoint, runtime, output,
                                                     source, mapping or fixture_mapping(source)))
        report = {"status": "passed", "scope": ("local_official_PG_apply_to_CH_dbt_S3" if mapping
                  else "isolated_native_CH_dbt_S3_historic_v1_sidecar_read"),
                  "mapping_source": ("official_local_PG_locked_preflight_sidecar" if mapping
                  else "explicit_test_fixture_not_PG_export"),
                  "producer": "rtw.community.favorite", "real": real, "two_uid": two,
                  "rejected": rejected,
                  "pg_preflight_snapshot_sha256": ready["preflight_snapshot_sha256"] if ready else None,
                  "pg_coverage_objects_verified": len(ready["objects"]) if ready else None,
                  "pg_source_kind": ready["source_kind"] if ready else None,
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
