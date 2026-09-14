"""Run a verified synthetic RTW/DC/PG qrel substream through CH/dbt.

The one-query live fixture correctly stops at ODS/DWD with zero visible rows
after withdrawal. An independent multi-query synthetic PG fixture may publish
all three Parquet splits and run the authoritative reader in the same run.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import sys
import urllib.parse

from pyarrow import fs

from acceptance import (HERE, LANDING, ROOT, ClickHouse, canonical, dbt_build, digest,
                        export, file_digest, put, read_source, start_clickhouse, start_s3)


SCHEMA = "sea.search.qrel-substream.v1"
PRODUCER = "ridethewind.knowledge"
PARTITION = "ridethewind.knowledge:qrel-substream:v1"


def verify_source(directory: Path) -> tuple[dict, Path, list[dict]]:
    manifest_path = directory / "manifest.json"
    manifest = json.loads(manifest_path.read_bytes())
    if (manifest.get("schema_version") != SCHEMA or manifest.get("producer") != PRODUCER or
            manifest.get("data_kind") != "synthetic" or manifest.get("activation") != "none" or
            manifest.get("judgment_scope_complete") is not False or
            manifest.get("source_partition") != PARTITION or
            not manifest.get("fixture_provenance") or manifest.get("from_offset") != 1 or
            manifest.get("qrel_count", 0) < 1 or manifest.get("through_offset") !=
            manifest.get("qrel_count", 0) + manifest.get("technical_skip_count", 0)):
        raise ValueError("explicit synthetic complete-prefix manifest contract differs")
    grouping = json.loads((directory / "grouping.json").read_bytes())
    if (grouping.get("schema_version") != "sea.search.qrel-grouping.v1" or
            grouping.get("data_kind") != "synthetic" or
            grouping.get("fixture_provenance") != manifest["fixture_provenance"] or
            grouping.get("policy_id") != manifest["grouping_policy_id"] or
            digest(canonical(grouping)) != manifest["grouping_policy_sha256"]):
        raise ValueError("versioned explicit family/near-duplicate mapping differs")
    assignments = {item["search_id"]: item for item in grouping["assignments"]}
    if len(assignments) != len(grouping["assignments"]):
        raise ValueError("duplicate query grouping assignment")
    coverage = (directory / "coverage.jsonl").read_bytes()
    landing = directory / f"{manifest['landing_batch_id']}.jsonl"
    if digest(coverage) != manifest["coverage_sha256"] or file_digest(landing) != manifest["landing_sha256"]:
        raise ValueError("coverage or landing bytes differ from PG exporter")
    root = digest((SCHEMA + ":" + PRODUCER).encode())
    qrels = []
    rows = coverage.splitlines()
    if len(rows) != manifest["through_offset"] or not coverage.endswith(b"\n"):
        raise ValueError("original DC producer prefix is incomplete")
    for offset, raw in enumerate(rows, 1):
        item = json.loads(raw)
        if item["source_offset"] != offset or item["previous_root"] != root:
            raise ValueError("original DC offset or prior root differs")
        if item["status"] == "qrel_revision":
            qrels.append(item)
            if (item["qrel_ordinal"] != len(qrels) or not item["authority_event_sha256"] or
                    not item["landing_payload_sha256"]):
                raise ValueError("qrel ordinal or RTW authority proof differs")
        elif (item["status"] != "technical_skip" or item["qrel_ordinal"] is not None or
              item["authority_event_sha256"] is not None or item["landing_payload_sha256"] is not None):
            raise ValueError("non-qrel DC offset was hidden or relabeled")
        leaf = {key: item[key] for key in ("source_offset", "event_id", "event_type", "status",
                                          "dc_input_hash", "dc_receipt_id",
                                          "authority_event_sha256", "qrel_ordinal",
                                          "landing_payload_sha256")}
        if digest(canonical(leaf)) != item["leaf_sha256"]:
            raise ValueError("original DC receipt coverage leaf differs")
        root = digest((root + ":" + item["leaf_sha256"]).encode())
        if item["coverage_root"] != root:
            raise ValueError("original DC producer coverage chain differs")
    if root != manifest["coverage_root"] or len(qrels) != manifest["qrel_count"]:
        raise ValueError("substream root or qrel count differs")
    _, events = read_source(landing, set())
    if len(events) != len(qrels):
        raise ValueError("qrel landing row count differs from covered ordinals")
    for ordinal, (event, covered) in enumerate(zip(events, qrels), 1):
        if (event["source_sequence"] != ordinal or event["source_partition"] != PARTITION or
                event["event_id"] != covered["event_id"] or
                event["judgment_source_hash"] != covered["authority_event_sha256"] or
                event["payload_hash"] != covered["landing_payload_sha256"] or
                event["judgment_source"] != "synthetic_fixture"):
            raise ValueError("qrel landing row not bound to covered RTW/DC event")
        assigned = assignments.get(event["query_id"])
        if (assigned is None or assigned["query_text_sha256"] != event["query_text_sha256"] or
                assigned["query_family_id"] != event["query_family_id"] or
                assigned["near_duplicate_cluster_id"] != event["near_duplicate_cluster_id"]):
            raise ValueError("landing group differs from explicit versioned assignment")
        if covered["event_type"].endswith("withdrawn.v1"):
            if (event["status"], event["relevance_grade"], event["judged_mask"]) != (
                    "retracted", None, False):
                raise ValueError("RTW null-grade withdrawal became a negative label")
        elif event["status"] != "active" or event["relevance_grade"] not in (0, 1, 2, 3):
            raise ValueError("graded fixture qrel differs from source")
    return manifest, landing, events


def validate_plan(plan: dict, cutoff: str) -> None:
    if list(plan) != ["train", "validation", "test"]:
        raise ValueError("split plan must list train/validation/test in order")
    previous = None
    for split in plan.values():
        start = datetime.fromisoformat(split["start"].replace("Z", "+00:00"))
        end = datetime.fromisoformat(split["end"].replace("Z", "+00:00"))
        if start.tzinfo is None or end.tzinfo is None or start >= end or previous and start < previous:
            raise ValueError("split plan overlaps or lacks UTC time")
        previous = end
    observed_cutoff = datetime.fromisoformat(cutoff.replace("Z", "+00:00"))
    if observed_cutoff.tzinfo is None or observed_cutoff.utcoffset() != timezone.utc.utcoffset(observed_cutoff):
        raise ValueError("ingest cutoff must be UTC")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", type=Path, required=True)
    parser.add_argument("--export-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--contracts", type=Path, required=True)
    parser.add_argument("--split-plan", type=Path, required=True)
    parser.add_argument("--cutoff", required=True)
    parser.add_argument("--publish-parquet", action="store_true")
    parser.add_argument("--reader-root", type=Path)
    args = parser.parse_args()
    runtime, output = args.runtime.resolve(), args.output.resolve()
    source, landing, events = verify_source(args.export_dir.resolve())
    plan = json.loads(args.split_plan.read_bytes())
    validate_plan(plan, args.cutoff)
    if args.publish_parquet and args.reader_root is None:
        parser.error("full Parquet publication requires an independent reader root")
    output.mkdir(parents=True, exist_ok=False)
    ch_proc = s3_proc = None
    try:
        ch_proc, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        ch = ClickHouse(endpoint)
        ch.query(f"CREATE DATABASE {LANDING}")
        ddl = (HERE / "landing.sql").read_text().format(database=LANDING)
        ch.query(ddl)
        raw = landing.read_bytes()
        if args.publish_parquet:
            s3_proc, prefix = start_s3(runtime / "weed", output / "seaweed")
            archive_url = f"{prefix}/search-qrel/substream/{source['coverage_root']}/{landing.name}"
            put(archive_url, raw)
            structure = ddl.split("(\n", 1)[1].split(") ENGINE", 1)[0].replace("'", "''")
            ch.query(f"INSERT INTO {LANDING}.qrel_history SELECT * FROM s3('{archive_url}', NOSIGN, "
                     f"'JSONEachRow', '{structure}') SETTINGS date_time_input_format='best_effort'")
        else:
            ch.query(f"INSERT INTO {LANDING}.qrel_history "
                     "SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", raw)
        generation = "sea_search_qrel_substream_g1"
        build = output / "build"
        dbt_build(runtime / ".venv/bin/dbt", endpoint, generation,
                  {"landing_schema": LANDING, "ingest_cutoff": args.cutoff, "splits": plan}, build)
        ods = ch.rows(f"SELECT count() AS n FROM {generation}.ods_qrel_events")[0]["n"]
        visible = ch.rows(f"SELECT count() AS n FROM {generation}.ds_search_qrels")[0]["n"]
        tombstones = ch.rows(f"SELECT count() AS n FROM {LANDING}.qrel_history "
                             "WHERE status='retracted' AND relevance_grade IS NULL AND judged_mask=false")[0]["n"]
        bad_tombstones = ch.rows(f"SELECT count() AS n FROM {LANDING}.qrel_history "
                                 "WHERE status='retracted' AND (relevance_grade IS NOT NULL OR judged_mask=true)")[0]["n"]
        columns = ch.rows(f"DESCRIBE TABLE {generation}.ds_search_qrels")
        expected = json.loads((args.contracts / "search-qrel.columns.v1.json").read_bytes())
        if ([column["name"] for column in columns] != [column["name"] for column in expected] or
                len(columns) != 23 or next(column["type"] for column in columns
                                            if column["name"] == "relevance_grade") != "UInt8" or
                ods != source["qrel_count"] or bad_tombstones != 0 or
                tombstones != sum(event["status"] == "retracted" for event in events)):
            raise ValueError("CH/dbt 23-column or null-grade tombstone contract differs")
        splits = {name: ch.rows(f"SELECT count() AS n FROM {generation}.ds_search_qrels "
                                f"WHERE split='{name}'")[0]["n"] for name in plan}
        dataset = "not_produced_insufficient_qrels"
        reader = "not_run"
        if args.publish_parquet:
            if min(splits.values()) < 1:
                raise ValueError("cannot publish 23-column Parquet with empty split")
            manifest_path, _ = export(ch, prefix, generation, 1, [landing], events,
                                      args.contracts, build, output / "dataset", None,
                                      substream=source, split_plan=plan, cutoff=args.cutoff)
            dataset = {"path": str(manifest_path), "sha256": file_digest(manifest_path)}
            sys.path.insert(0, str(args.reader_root.resolve() / "training" / "src"))
            from sea_training.search_dataset import open_search_dataset
            parsed = urllib.parse.urlsplit(prefix)
            filesystem = fs.S3FileSystem(anonymous=True, region="us-east-1", scheme="http",
                                         endpoint_override=f"{parsed.hostname}:{parsed.port}")
            uri = parsed.path.lstrip("/") + f"/search-qrel/{generation}/manifest.json"
            with open_search_dataset(uri, expected_manifest_sha256=dataset["sha256"],
                                     schema_dir=args.contracts, filesystem=filesystem) as snapshot:
                reader = snapshot.report
                if reader["validated_rows"] != visible or reader["status"] != "validated_synthetic_fixture":
                    raise ValueError("independent live S3 qrel reader differs")
        elif visible != 0 or source["qrel_count"] != 2:
            raise ValueError("non-published fixture must be the single withdrawn query")
        report = {"status": "passed", "data_kind": "synthetic", "activation": "none",
                  "original_dc_offset": source["through_offset"], "qrel_ordinal": source["qrel_count"],
                  "technical_skips": source["technical_skip_count"],
                  "coverage_root": source["coverage_root"],
                  "source_manifest_sha256": file_digest(args.export_dir / "manifest.json"),
                  "landing_sha256": source["landing_sha256"],
                  "dbt_manifest_sha256": file_digest(build / "target" / "manifest.json"),
                  "dbt_run_results_sha256": file_digest(build / "target" / "run_results.json"),
                  "ods_rows": ods, "visible_rows": visible, "split_rows": splits,
                  "null_grade_tombstones": tombstones, "dataset_manifest": dataset,
                  "independent_reader": reader, "human_label_claim": None,
                  "judgment_scope_complete": False}
        (output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
        print(json.dumps(report, sort_keys=True), flush=True)
    finally:
        for proc in (s3_proc, ch_proc):
            if proc is not None:
                proc.terminate()
                try:
                    proc.wait(timeout=15)
                except Exception:
                    proc.kill()
                    proc.wait(timeout=10)


if __name__ == "__main__":
    main()
