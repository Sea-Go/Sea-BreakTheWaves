"""Synthetic Wiki FactSet provenance through isolated ClickHouse/dbt only.

The authoritative PG→CH exporter is Go wikiqualitydwd.Freeze/Write, which
first calls SourceProof's actual RTW/ODS/DC ACK reader. This script exercises
the fixed synthetic byte contract and a fresh-generation CH loader. It cannot
stand in for a live RTW/DC/PG source run or observed D07 quality.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

HERE = Path(__file__).resolve().parent
WAREHOUSE = HERE.parent
sys.path.insert(0, str(WAREHOUSE / "scripts"))
from engine import ClickHouse  # noqa: E402
from local_server import start as start_clickhouse  # noqa: E402

RUNTIME_FILES = ("clickhouse", ".venv/bin/dbt")
MANIFEST_KEYS = {
    "schema", "producer", "consumer", "module_id", "page_id",
    "wiki_revision_id", "fact_set_revision_id", "source_scope_revision",
    "cutoff_offset", "acknowledged_at_least", "committed_at_least",
    "ods_evidence_sha256", "dc_index_sha256", "prefix_jsonl_sha256",
    "catalog_jsonl_sha256", "judgment_jsonl_sha256", "prefix_rows",
    "catalog_facts", "judgment_revisions", "technical_skips",
    "transported_fact_count", "evidence_level", "quality_state", "activation",
}
SHA = re.compile(r"^[a-f0-9]{64}$")
ID = re.compile(r"^[A-Za-z0-9._-]{1,200}$")
SCOPE = re.compile(r"^scope_[a-f0-9]{64}$")
DATABASE = re.compile(r"^[a-z][a-z0-9_]{1,63}$")
SYNTHETIC_GOLDEN_MANIFEST = {
    3: "9f79112c2271c1209b89fa0f7a07a80760e09efb0705f80f21cd804314649735",
    5: "6697b6fc2fcb08f245ba65393f108a7e9002733c355448e96f6912bc34ea29e9",
}
ROW_KEYS = {
    "prefix": {"acknowledged_cutoff_offset", "dc_index_sha256", "dc_receipt",
               "dc_receipt_sha256", "event_id", "event_jcs_sha256",
               "event_spec_jcs", "event_type", "fact_set_payload_jcs",
               "fact_set_payload_jcs_sha256", "ods_evidence_sha256", "producer",
               "rtw_original_event", "rtw_original_event_sha256", "source_offset",
               "status"},
    "catalog": {"acknowledged_cutoff_offset", "catalog_dc_receipt_sha256",
                "catalog_event_id", "catalog_event_jcs_sha256",
                "catalog_event_raw_sha256", "catalog_offset", "conflict_group",
                "declaration_source", "fact_id", "fact_set_payload_jcs_sha256",
                "fact_set_revision_id", "facts_complete_declared", "locator",
                "module_id", "page_id", "producer", "quality_state", "required",
                "rtw_actor_id", "source_byte_end", "source_byte_start",
                "source_content_sha256", "source_quote", "source_quote_sha256",
                "source_revision_id", "source_scope_revision", "wiki_revision_id"},
    "judgment": {"acknowledged_cutoff_offset", "assessment",
                 "base_judge_revision_id", "claimed_citation_present",
                 "claimed_grade", "dc_receipt_sha256", "event_id",
                 "event_jcs_sha256", "event_raw_sha256", "fact_id",
                 "fact_set_revision_id", "judge_revision", "judge_revision_id",
                 "judgment_id", "module_id", "page_id", "producer",
                 "quality_state", "rtw_actor_id", "source_offset",
                 "source_quote_sha256", "source_revision_id",
                 "source_scope_revision", "wiki_revision_id"},
}


def digest(body: bytes) -> str:
    return hashlib.sha256(body).hexdigest()


def exact_json(raw: bytes) -> object:
    def pairs(items: list[tuple[str, object]]) -> dict:
        result: dict[str, object] = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate JSON key in frozen source")
            result[key] = value
        return result

    return json.loads(raw, object_pairs_hook=pairs,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError("nonfinite JSON")))


def jcs_fixture(value: object) -> bytes:
    # This checks the committed synthetic Go-JCS fixture byte form. The Go
    # exporter owns actual RFC 8785 canonicalization before publishing.
    return json.dumps(value, sort_keys=True, ensure_ascii=False,
                      separators=(",", ":")).encode("utf-8")


def read_jsonl(body: bytes) -> list[dict]:
    if not body or not body.endswith(b"\n"):
        raise ValueError("source JSONL must be nonempty and LF terminated")
    rows = []
    for line in body.splitlines():
        value = exact_json(line)
        if not isinstance(value, dict) or line != jcs_fixture(value):
            raise ValueError("source JSONL differs from frozen Go-JCS byte form")
        rows.append(value)
    return rows


def read_bundle(directory: Path, expected_manifest_sha256: str) -> tuple[
        dict, dict[str, bytes], dict[str, list[dict]]]:
    raw = (directory / "manifest.json").read_bytes()
    if (not SHA.fullmatch(expected_manifest_sha256) or
            digest(raw) != expected_manifest_sha256):
        raise ValueError("caller-pinned Wiki source manifest SHA differs")
    manifest = exact_json(raw)
    if (not isinstance(manifest, dict) or set(manifest) != MANIFEST_KEYS or
            raw != jcs_fixture(manifest)):
        raise ValueError("Wiki source manifest literal/JCS contract differs")
    if (manifest["schema"] != "sea.wiki.fact-set-offline-source.v1" or
            manifest["producer"] != "ridethewind.knowledge" or
            manifest["consumer"] != "btw-warehouse-wiki-quality" or
            manifest["evidence_level"] != "rtw_dc_source_proof_only" or
            manifest["quality_state"] != "not_evaluable" or
            manifest["activation"] != "none"):
        raise ValueError("source-only provenance was promoted")
    for key in ("module_id", "wiki_revision_id", "fact_set_revision_id"):
        if not isinstance(manifest[key], str) or not ID.fullmatch(manifest[key]):
            raise ValueError(f"untrusted dbt identifier {key}")
    if not isinstance(manifest["source_scope_revision"], str) or not SCOPE.fullmatch(
            manifest["source_scope_revision"]):
        raise ValueError("untrusted dbt SourceScopeRevision")
    if not isinstance(manifest["page_id"], str) or not manifest["page_id"]:
        raise ValueError("missing source PageID")
    cutoff = manifest["cutoff_offset"]
    if (type(cutoff) is not int or cutoff < 1 or cutoff > 4096 or
            any(type(manifest[name]) is not int or manifest[name] < cutoff
                for name in ("acknowledged_at_least", "committed_at_least"))):
        raise ValueError("unverified cutoff/ACK watermark")
    for key in ("ods_evidence_sha256", "dc_index_sha256", "prefix_jsonl_sha256",
                "catalog_jsonl_sha256", "judgment_jsonl_sha256"):
        if not isinstance(manifest[key], str) or not SHA.fullmatch(manifest[key]):
            raise ValueError("invalid frozen source digest")
    names = {"prefix": "prefix.jsonl", "catalog": "catalog-facts.jsonl",
             "judgment": "judgments.jsonl"}
    bodies = {key: (directory / filename).read_bytes() for key, filename in names.items()}
    rows = {key: read_jsonl(body) for key, body in bodies.items()}
    for name, values in rows.items():
        if any(set(row) != ROW_KEYS[name] for row in values):
            raise ValueError("typed Wiki source row literal contract differs")
    for name, key in (("prefix", "prefix_jsonl_sha256"),
                      ("catalog", "catalog_jsonl_sha256"),
                      ("judgment", "judgment_jsonl_sha256")):
        if digest(bodies[name]) != manifest[key]:
            raise ValueError("frozen source JSONL SHA differs")
    if ([row["source_offset"] for row in rows["prefix"]] !=
            list(range(1, cutoff + 1)) or
            len(rows["prefix"]) != manifest["prefix_rows"] or
            len(rows["catalog"]) != manifest["catalog_facts"] or
            len(rows["judgment"]) != manifest["judgment_revisions"] or
            sum(row["status"] == "technical_skip" for row in rows["prefix"]) !=
            manifest["technical_skips"]):
        raise ValueError("frozen producer prefix/count differs")
    by_offset = {row["source_offset"]: row for row in rows["prefix"]}
    catalog_facts: set[str] = set()
    for row in rows["prefix"]:
        if (row["producer"] != manifest["producer"] or
                row["acknowledged_cutoff_offset"] != cutoff or
                row["ods_evidence_sha256"] != manifest["ods_evidence_sha256"] or
                row["dc_index_sha256"] != manifest["dc_index_sha256"] or
                digest(row["event_spec_jcs"].encode()) != row["event_jcs_sha256"] or
                digest(row["dc_receipt"].encode()) != row["dc_receipt_sha256"]):
            raise ValueError("source Event JCS/receipt/prefix SHA differs")
        receipt = exact_json(row["dc_receipt"].encode())
        if (not isinstance(receipt, dict) or
                receipt.get("producer") != row["producer"] or
                receipt.get("event_id") != row["event_id"] or
                receipt.get("offset") != row["source_offset"] or
                receipt.get("input_hash") != row["event_jcs_sha256"] or
                receipt.get("technical_status") != "accepted"):
            raise ValueError("DC accepted receipt differs from source position")
        if row["status"] == "technical_skip":
            if (row["rtw_original_event"] or row["rtw_original_event_sha256"] or
                    row["fact_set_payload_jcs"] or row["fact_set_payload_jcs_sha256"]):
                raise ValueError("technical skip became a Wiki quality source")
        elif (row["status"] not in ("fact_set_verified", "quality_verified") or
              digest(row["rtw_original_event"].encode()) != row["rtw_original_event_sha256"]):
            raise ValueError("verified Wiki Event original bytes differ")
        if (row["status"] != "technical_skip" and jcs_fixture(
                exact_json(row["rtw_original_event"].encode())) !=
                row["event_spec_jcs"].encode()):
            raise ValueError("RTW original Event JCS differs from DC whole Event")
    for row in rows["catalog"]:
        parent = by_offset.get(row["catalog_offset"])
        if (parent is None or parent["status"] != "fact_set_verified" or
                parent["event_id"] != row["catalog_event_id"] or
                parent["rtw_original_event_sha256"] != row["catalog_event_raw_sha256"] or
                parent["event_jcs_sha256"] != row["catalog_event_jcs_sha256"] or
                parent["dc_receipt_sha256"] != row["catalog_dc_receipt_sha256"] or
                parent["fact_set_payload_jcs_sha256"] != row["fact_set_payload_jcs_sha256"] or
                row["fact_set_revision_id"] != manifest["fact_set_revision_id"] or
                row["wiki_revision_id"] != manifest["wiki_revision_id"] or
                row["source_scope_revision"] != manifest["source_scope_revision"] or
                row["quality_state"] != "not_evaluable" or
                row["fact_id"] in catalog_facts):
            raise ValueError("typed Catalog Fact differs from accepted parent")
        catalog_facts.add(row["fact_id"])
    last: dict[str, dict] = {}
    for row in rows["judgment"]:
        parent = by_offset.get(row["source_offset"])
        if (parent is None or parent["status"] != "quality_verified" or
                parent["event_id"] != row["event_id"] or
                parent["rtw_original_event_sha256"] != row["event_raw_sha256"] or
                parent["event_jcs_sha256"] != row["event_jcs_sha256"] or
                parent["dc_receipt_sha256"] != row["dc_receipt_sha256"] or
                row["fact_id"] not in catalog_facts or
                row["fact_set_revision_id"] != manifest["fact_set_revision_id"] or
                row["wiki_revision_id"] != manifest["wiki_revision_id"] or
                row["source_scope_revision"] != manifest["source_scope_revision"] or
                row["quality_state"] != "not_evaluable"):
            raise ValueError("typed judgment differs from accepted parent/scope")
        if row["fact_id"] in last and row["source_offset"] <= last[row["fact_id"]]["source_offset"]:
            raise ValueError("transported judgment order differs")
        last[row["fact_id"]] = row
    if (len(last) != manifest["transported_fact_count"] or
            any(row["required"] and row["fact_id"] not in last for row in rows["catalog"])):
        raise ValueError("required Fact has no transported judgment at cutoff")
    return manifest, bodies, rows


def load_new_generation(ch: ClickHouse, database: str, bodies: dict[str, bytes]) -> None:
    if not DATABASE.fullmatch(database):
        raise ValueError("invalid isolated ClickHouse generation name")
    # MergeTree has no unique constraint. Creation MUST fail before INSERT if
    # this generation exists; replay is built in a distinct namespace.
    if ch.rows(f"SELECT name FROM system.databases WHERE name='{database}'"):
        raise ValueError("Wiki source generation already exists; no repeated INSERT")
    ch.query(f"CREATE DATABASE {database}")
    ddl = (HERE / "landing.sql").read_text().format(database=database)
    for statement in ddl.split(";"):
        if statement.strip():
            ch.query(statement)
    for name, table in (("prefix", "ods_wiki_prefix_event"),
                        ("catalog", "ods_wiki_catalog_fact"),
                        ("judgment", "ods_wiki_judgment_revision")):
        ch.query(f"INSERT INTO {database}.{table} FORMAT JSONEachRow", bodies[name])


def build_dbt(ch: ClickHouse, runtime: Path, output: Path,
              manifest: dict, landing: str, generation: str) -> dict:
    if not DATABASE.fullmatch(generation):
        raise ValueError("invalid dbt generation name")
    output.mkdir(parents=True, exist_ok=False)
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(ch.endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=generation,
               DBT_SEND_ANONYMOUS_USAGE_STATS="false", DBT_USE_COLORS="false")
    variables = {"wiki_quality_landing_schema": landing,
                 "wiki_revision_id": manifest["wiki_revision_id"],
                 "fact_set_revision_id": manifest["fact_set_revision_id"],
                 "source_scope_revision": manifest["source_scope_revision"],
                 "cutoff_offset": manifest["cutoff_offset"]}
    command = [str(runtime / ".venv/bin/dbt"), "build", "--project-dir", str(HERE),
               "--profiles-dir", str(WAREHOUSE / "environment"),
               "--target-path", str(output / "target"),
               "--log-path", str(output / "logs"),
               "--vars", json.dumps(variables), "--no-partial-parse"]
    with (output / "dbt.log").open("wb") as log:
        result = subprocess.run(command, env=env, stdout=log,
                                stderr=subprocess.STDOUT, timeout=180)
    if result.returncode:
        raise RuntimeError(f"Wiki quality dbt failed; inspect {output / 'dbt.log'}")
    run_results = json.loads((output / "target/run_results.json").read_text())
    if len(run_results["results"]) != 8 or any(
            row["status"] not in ("pass", "success") for row in run_results["results"]):
        raise AssertionError("Wiki quality dbt four-model/four-test DAG incomplete")
    ads = ch.rows(f"SELECT fact_id,required,transported_offset,judge_revision,"
                  f"quality_state,evidence_level,activation,admin_claimed_grade "
                  f"FROM {generation}.ads_wiki_fact_source_evidence ORDER BY fact_id")
    if len(ads) != manifest["catalog_facts"] or any(
            row["quality_state"] != "not_evaluable" or
            row["evidence_level"] != "rtw_dc_source_proof_only" or
            row["activation"] != "none" for row in ads):
        raise AssertionError("offline ADS source-only state/count differs")
    columns = {row["name"] for row in ch.rows(
        f"SELECT name FROM system.columns WHERE database='{generation}' "
        "AND table='ads_wiki_fact_source_evidence'")}
    if columns.intersection({"tenant_id", "user_center_uid", "d07_score",
                             "observed_quality", "model_quality", "training_label"}):
        raise AssertionError("offline ADS invented product/human quality semantics")
    return {"ads": ads,
            "dbt_manifest_sha256": digest((output / "target/manifest.json").read_bytes()),
            "dbt_run_results_sha256": digest((output / "target/run_results.json").read_bytes()),
            "dbt_log_sha256": digest((output / "dbt.log").read_bytes())}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--fixtures", default=str(HERE / "fixtures"))
    parser.add_argument("--bundle", help="one Go Frozen.Write directory from the same parent run")
    parser.add_argument("--expected-manifest-sha256",
                        help="caller-pinned Go Frozen.ManifestSHA for --bundle")
    args = parser.parse_args()
    runtime, output, fixtures = (Path(args.runtime).resolve(),
                                 Path(args.output).resolve(),
                                 Path(args.fixtures).resolve())
    if any(not (runtime / name).exists() for name in RUNTIME_FILES):
        raise RuntimeError("locked ClickHouse/dbt runtime unavailable")
    if shutil.disk_usage(output.parent).free < 1_200_000_000:
        raise RuntimeError("isolated ClickHouse evidence needs at least 1.2 GB free")
    if args.bundle:
        if not args.expected_manifest_sha256:
            raise ValueError("Go bundle needs caller-pinned manifest SHA")
        source = Path(args.bundle).resolve()
        pinned = read_bundle(source, args.expected_manifest_sha256)
        checked = {pinned[0]["cutoff_offset"]: pinned}
        source_root = {pinned[0]["cutoff_offset"]: source}
    else:
        if args.expected_manifest_sha256:
            raise ValueError("external manifest SHA requires --bundle")
        checked = {cutoff: read_bundle(fixtures / f"cutoff-{cutoff}",
                                       SYNTHETIC_GOLDEN_MANIFEST[cutoff])
                   for cutoff in (3, 5)}
        source_root = {cutoff: fixtures / f"cutoff-{cutoff}" for cutoff in (3, 5)}
    output.mkdir(parents=True, exist_ok=False)
    process = None
    try:
        process, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        ch = ClickHouse(endpoint)
        nonce = uuid.uuid4().hex[:10]
        results: dict[int, dict] = {}
        for cutoff in sorted(checked):
            manifest, bodies, source_rows = checked[cutoff]
            landing = f"wiki_landing_{nonce}_c{cutoff}"
            generation = f"wiki_dwd_{nonce}_c{cutoff}"
            load_new_generation(ch, landing, bodies)
            original_count = ch.rows(f"SELECT count() as n FROM {landing}.ods_wiki_prefix_event")[0]["n"]
            try:
                load_new_generation(ch, landing, bodies)
            except ValueError as error:
                if "already exists" not in str(error):
                    raise
            else:
                raise AssertionError("same generation accepted repeated MergeTree INSERT")
            if ch.rows(f"SELECT count() as n FROM {landing}.ods_wiki_prefix_event")[0]["n"] != original_count:
                raise AssertionError("replay doubled immutable landing prefix")
            result = build_dbt(ch, runtime, output / f"build-c{cutoff}",
                               manifest, landing, generation)
            expected_latest = {}
            for row in source_rows["judgment"]:
                expected_latest[row["fact_id"]] = row["judge_revision"]
            actual_latest = {row["fact_id"]: row["judge_revision"] for row in result["ads"]}
            if actual_latest != {row["fact_id"]: expected_latest.get(row["fact_id"])
                                 for row in source_rows["catalog"]}:
                raise AssertionError("last transported K at ACK cutoff differs")
            results[cutoff] = {"source_manifest_sha256": digest(
                (source_root[cutoff] / "manifest.json").read_bytes()),
                "prefix_jsonl_sha256": manifest["prefix_jsonl_sha256"],
                "catalog_jsonl_sha256": manifest["catalog_jsonl_sha256"],
                "judgment_jsonl_sha256": manifest["judgment_jsonl_sha256"],
                "prefix_rows": manifest["prefix_rows"],
                "technical_skips": manifest["technical_skips"],
                "judgment_revisions": manifest["judgment_revisions"],
                "ads_rows": len(result["ads"]),
                "last_transported_revisions": actual_latest,
                "dbt_manifest_sha256": result["dbt_manifest_sha256"],
                "dbt_run_results_sha256": result["dbt_run_results_sha256"],
                "dbt_log_sha256": result["dbt_log_sha256"]}
        report = {"status": "passed", "evidence_level": (
                  "caller_pinned_Go_bundle_real_CH_dbt_source_authority_separate" if args.bundle
                  else "L2_synthetic_Go_JCS_real_CH_dbt"),
                  "quality_state": "not_evaluable", "activation": "none",
                  "production_verified": False, "d07_evaluable": False,
                  "source_authority_independently_verified_here": False,
                  "cutoffs": results, "same_generation_reinsert_rejected": True}
        body = jcs_fixture(report)
        (output / "report.json").write_bytes(body)
        print(f"Wiki FactSet isolated CH/dbt PASS; report={output / 'report.json'} SHA256={digest(body)}")
    finally:
        if process is not None:
            process.terminate()
            process.wait(timeout=60)
            (output / "ch-stop-status.json").write_bytes(jcs_fixture({
                "exit_code": process.returncode, "process_exited": process.poll() is not None,
                "endpoint_unreachable": endpoint_closed(endpoint)}))


def endpoint_closed(endpoint: str) -> bool:
    try:
        urllib.request.urlopen(endpoint + "/ping", timeout=1)
    except (OSError, TimeoutError):
        return True
    return False


if __name__ == "__main__":
    main()
