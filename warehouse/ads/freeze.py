"""Freeze one WS07-E ADS generation after a complete dbt build.

This module owns a local acceptance receipt, not H12's formal EvalReport or
recommend's activation pointer. Observed H09 input requires an injected
authority verifier; no such production adapter exists in this slice.
"""
from __future__ import annotations

import datetime as dt
import hashlib
import json
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Callable


NAME = re.compile(r"^[a-z][a-z0-9_]{0,62}$")
TOKEN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$")
HASH = re.compile(r"^[a-f0-9]{64}$")
MATURE = {"mature_positive": 1, "mature_negative": 0}


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()


def sha(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def file_sha(path: Path) -> str:
    return sha(path.read_bytes())


def validate_source(recipe: dict, batches: list[dict],
                    verify_observed: Callable[[str, dict, list[dict]], bool] | None = None) -> None:
    if not TOKEN.fullmatch(recipe["ads_definition_revision"]) or not NAME.fullmatch(recipe["ads_source_kind"]):
        raise ValueError("ADS definition or source kind is invalid")
    if recipe["ads_source_kind"] == "synthetic":
        if recipe["ads_source_coverage_state"] != "synthetic_fixture_complete" or recipe.get("ads_coverage_receipt_sha256", ""):
            raise ValueError("synthetic ADS requires its complete fixed fixture and no observed receipt")
    elif recipe["ads_source_kind"] == "observed":
        receipt = recipe.get("ads_coverage_receipt_sha256", "")
        watermarks = recipe.get("ads_source_watermarks", [])
        if (recipe["ads_source_coverage_state"] != "verified_complete" or
                not HASH.fullmatch(receipt) or not watermarks or verify_observed is None or
                not verify_observed(receipt, recipe, batches)):
            raise ValueError("observed ADS requires an authoritative H09 coverage and maturity receipt")
        for watermark in watermarks:
            if (not watermark.get("partition") or type(watermark.get("contiguous_sequence")) is not int or
                    watermark["contiguous_sequence"] <= 0 or watermark.get("complete") is not True):
                raise ValueError("observed ADS source watermark is not complete")
    else:
        raise ValueError("unknown ADS source kind")
    cutoff = dt.datetime.fromisoformat(recipe["ads_evaluation_cutoff"].replace("Z", "+00:00"))
    observed = dt.datetime.fromisoformat(recipe["observed_until"].replace("Z", "+00:00"))
    watermark = dt.datetime.fromisoformat(recipe["event_watermark"].replace("Z", "+00:00"))
    if cutoff.tzinfo is None or observed.tzinfo is None or watermark.tzinfo is None or cutoff > observed or cutoff > watermark:
        raise ValueError("ADS evaluation cutoff exceeds available source or event watermark")
    if [batch["batch_id"] for batch in batches] != recipe["source_batches"]:
        raise ValueError("ADS source batch list differs from dbt recipe")
    if len({batch["batch_id"] for batch in batches}) != len(batches):
        raise ValueError("ADS source batch repeated")
    if any(not HASH.fullmatch(batch["sha256"]) for batch in batches):
        raise ValueError("ADS source batch hash missing")


def validate_synthetic_source_files(files: list[Path]) -> list[dict]:
    """A fixture is complete only with every source position in its prefix."""
    positions: dict[str, dict[int, str]] = {}
    events: list[dict] = []
    for path in files:
        for line in path.read_text().splitlines():
            event = json.loads(line)
            body = {k: v for k, v in event.items() if k not in ("batch_id", "payload_hash")}
            actual = sha(json.dumps(body, sort_keys=True).encode())
            partition, sequence = event.get("source_partition"), event.get("source_sequence")
            if (event.get("batch_id") != path.stem or event.get("payload_hash") != actual or
                    not partition or type(sequence) is not int or sequence <= 0):
                raise ValueError("synthetic ADS source identity or hash is incomplete")
            known = positions.setdefault(partition, {})
            if sequence in known and known[sequence] != actual:
                raise ValueError("synthetic ADS source position has conflicting evidence")
            known[sequence] = actual
            events.append(event)
    if not events or any(set(found) != set(range(1, max(found) + 1))
                         for found in positions.values()):
        raise ValueError("synthetic ADS source has a missing position")
    return events


def validate_rows(rows: list[dict], rollups: list[dict], recipe: dict) -> None:
    seen: set[tuple[str, str, str, str]] = set()
    counts = dict(visible=0, served=0, positive=0, negative=0)
    for row in rows:
        subject = tuple(row[key] for key in ("authority_id", "tenant_id", "subject_id", "impression_id"))
        if any(not value for value in subject) or subject in seen:
            raise ValueError("ADS exposed impression identity missing or multiplied")
        seen.add(subject)
        if row["source_kind"] != recipe["ads_source_kind"] or row["source_coverage_state"] != recipe["ads_source_coverage_state"]:
            raise ValueError("ADS row source contract differs from frozen recipe")
        if row["coverage_receipt_sha256"] != recipe.get("ads_coverage_receipt_sha256", ""):
            raise ValueError("ADS coverage receipt differs from frozen recipe")
        state, label = row["evaluation_state"], row["mature_label"]
        if state in MATURE:
            if label != MATURE[state] or not row["served_event_id"] or not row["window_mature"] or not row["impression_source_partition"] or not row["served_source_partition"]:
                raise ValueError("mature ADS row lacks visible served/source/label evidence")
            counts["positive" if label == 1 else "negative"] += 1
        elif label is not None:
            raise ValueError("unshown, pending, excluded or unsourced row became a negative")
        counts["visible"] += 1
        if row["served_event_id"]:
            counts["served"] += 1
        if row["cohort"] != "unassigned" or row["assignment_state"] != "missing_authoritative_assignment":
            raise ValueError("unverified assignment cannot make an experiment cohort")
    if sum(int(r["visible_impressions"]) for r in rollups) != counts["visible"] or +            sum(int(r["served_visible_impressions"]) for r in rollups) != counts["served"] or +            sum(int(r["mature_positive_impressions"]) for r in rollups) != counts["positive"] or +            sum(int(r["mature_negative_impressions"]) for r in rollups) != counts["negative"] or +            sum(int(r["mature_evaluable_denominator"]) for r in rollups) != counts["positive"] + counts["negative"]:
        raise ValueError("ADS rollup differs from per-impression evidence")
    if any(r["experiment_effect"] is not None or r["experiment_state"] != "not_evaluable_assignment_missing"
           for r in rollups):
        raise ValueError("ADS reported an experiment effect without authoritative assignment")


def verify_build(build_dir: Path, recipe: dict) -> dict:
    manifest_path, results_path = build_dir / "target/manifest.json", build_dir / "target/run_results.json"
    manifest, results = json.loads(manifest_path.read_text()), json.loads(results_path.read_text())
    if manifest["metadata"]["invocation_id"] != results["metadata"]["invocation_id"] or results["args"]["vars"] != recipe:
        raise ValueError("ADS dbt execution differs from frozen recipe")
    statuses = {row["unique_id"]: row["status"] for row in results["results"]}
    for name in ("ads_positive_evidence", "ads_recommendation_exposures", "ads_recommendation_quality"):
        if statuses.get(f"model.sea_warehouse.{name}") != "success":
            raise ValueError(f"ADS dbt model {name} did not complete")
    if any(status not in ("success", "pass") for status in statuses.values()):
        raise ValueError("ADS dbt quality test or upstream model failed")
    return {"dbt_manifest_sha256": file_sha(manifest_path),
            "dbt_run_results_sha256": file_sha(results_path),
            "dbt_invocation_id": manifest["metadata"]["invocation_id"]}


def put_fixed(prefix: str, generation: str, kind: str, data: bytes) -> dict:
    parsed = urllib.parse.urlsplit(prefix)
    if parsed.scheme != "http" or parsed.hostname != "127.0.0.1" or not NAME.fullmatch(generation):
        raise ValueError("only isolated task-owned S3 is supported by this acceptance adapter")
    digest = sha(data)
    suffix = "jsonl" if kind == "rows" else "json"
    url = f"{prefix}/ads/{generation}/{kind}/{digest}.{suffix}"
    try:
        with urllib.request.urlopen(url, timeout=30) as response:
            existing = response.read()
        if existing != data:
            raise ValueError("content-addressed ADS S3 object changed")
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
        request = urllib.request.Request(url, data=data, method="PUT")
        with urllib.request.urlopen(request, timeout=30) as response:
            response.read()
    with urllib.request.urlopen(url, timeout=30) as response:
        if response.read() != data:
            raise ValueError("ADS S3 readback differs from frozen bytes")
    return {"url": url, "sha256": digest, "size_bytes": len(data)}


def register_pg(psql: str, dsn: str, schema: Path, manifest: dict, manifest_hash: str) -> None:
    subprocess.run([psql, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", dsn, "-f", str(schema)],
                   check=True, stdout=subprocess.DEVNULL)
    variables = {
        "ads_generation": manifest["generation"],
        "ads_revision": str(manifest["data_revision"]),
        "ads_source_kind": manifest["source_kind"],
        "ads_definition_revision": manifest["definition_revision"],
        "ads_data_as_of": manifest["data_as_of"],
        "ads_input_hash": manifest["input_sha256"],
        "ads_rows_hash": manifest["files"]["rows"]["sha256"],
        "ads_rollup_hash": manifest["files"]["rollup"]["sha256"],
        "ads_manifest_hash": manifest_hash,
        "ads_experiment_state": manifest["experiment_state"],
    }
    command = [psql, "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-d", dsn]
    command.extend(f"-v{name}={value}" for name, value in variables.items())
    sql = """INSERT INTO warehouse_ads_generations
      (generation,revision,source_kind,definition_revision,data_as_of,input_sha256,
       rows_sha256,rollup_sha256,manifest_sha256,experiment_state)
      VALUES (:'ads_generation', :'ads_revision'::bigint, :'ads_source_kind',
       :'ads_definition_revision', :'ads_data_as_of'::timestamptz, :'ads_input_hash',
       :'ads_rows_hash', :'ads_rollup_hash', :'ads_manifest_hash', :'ads_experiment_state')
      ON CONFLICT (generation) DO NOTHING;
      SELECT trim(manifest_sha256) FROM warehouse_ads_generations
      WHERE generation=:'ads_generation';
    """
    result = subprocess.run(command, input=sql, text=True, capture_output=True, check=True)
    if result.stdout.strip().splitlines()[-1:] != [manifest_hash]:
        raise ValueError("ADS PostgreSQL generation conflicts with frozen manifest")


def freeze(ch, namespace: str, recipe: dict, build_dir: Path, source_files: list[Path],
           destination: Path, s3_prefix: str, psql: str, pg_dsn: str,
           project_revision: str,
           verify_observed: Callable[[str, dict, list[dict]], bool] | None = None) -> dict:
    if not NAME.fullmatch(namespace):
        raise ValueError("ADS generation name invalid")
    if destination.exists():
        raise ValueError("immutable ADS output directory already exists")
    batches = [{"batch_id": path.stem, "sha256": file_sha(path)} for path in source_files]
    validate_source(recipe, batches, verify_observed)
    if recipe["ads_source_kind"] == "synthetic":
        synthetic_events = validate_synthetic_source_files(source_files)
        by_partition: dict[str, int] = {}
        for event in synthetic_events:
            partition = event["source_partition"]
            by_partition[partition] = max(by_partition.get(partition, 0), event["source_sequence"])
        source_watermarks = [
            {"partition": partition, "contiguous_sequence": position, "complete": True,
             "event_time": recipe["event_watermark"]}
            for partition, position in sorted(by_partition.items())
        ]
    else:
        source_watermarks = recipe["ads_source_watermarks"]
    dbt = verify_build(build_dir, recipe)
    rows_bytes = ch.query(f"SELECT * FROM {namespace}.ads_recommendation_exposures "
                          "ORDER BY authority_id,tenant_id,subject_id,impression_id FORMAT JSONEachRow")
    rows = [json.loads(line) for line in rows_bytes.splitlines()]
    rollups = ch.rows(f"SELECT * FROM {namespace}.ads_recommendation_quality "
                      "ORDER BY cohort,source_kind,impression_source_partition")
    validate_rows(rows, rollups, recipe)
    rollup_bytes = canonical(rollups) + b"\n"
    destination.mkdir(parents=True, exist_ok=False)
    (destination / "rows.jsonl").write_bytes(rows_bytes)
    (destination / "rollup.json").write_bytes(rollup_bytes)
    files = {"rows": put_fixed(s3_prefix, namespace, "rows", rows_bytes),
             "rollup": put_fixed(s3_prefix, namespace, "rollup", rollup_bytes)}
    input_sha256 = sha(canonical({"recipe": recipe, "batches": batches,
                                  "project_revision": project_revision}))
    manifest = {
        "schema_version": "sea.ads.recommendation-quality.v1",
        "generation": namespace, "domain": "recommendation",
        "data_revision": recipe["label_revision"],
        "source_kind": recipe["ads_source_kind"],
        "source_coverage_state": recipe["ads_source_coverage_state"],
        "coverage_receipt_sha256": recipe.get("ads_coverage_receipt_sha256", ""),
        "definition_revision": recipe["ads_definition_revision"],
        "data_as_of": recipe["ads_evaluation_cutoff"],
        "event_watermark": recipe["event_watermark"],
        "source_watermarks": source_watermarks,
        "observed_until": recipe["observed_until"],
        "label_rule_version": recipe["label_rule_version"],
        "project_revision": project_revision,
        "input_sha256": input_sha256, "batches": batches, "dbt": dbt,
        "row_count": len(rows), "cohort": "unassigned",
        "experiment_state": "not_evaluable_assignment_missing",
        "counts": {"visible": sum(int(r["visible_impressions"]) for r in rollups),
                   "served_visible": sum(int(r["served_visible_impressions"]) for r in rollups),
                   "mature_positive": sum(int(r["mature_positive_impressions"]) for r in rollups),
                   "mature_negative": sum(int(r["mature_negative_impressions"]) for r in rollups),
                   "mature_evaluable_denominator": sum(int(r["mature_evaluable_denominator"]) for r in rollups)},
        "files": files,
    }
    manifest_bytes = canonical(manifest) + b"\n"
    (destination / "manifest.json").write_bytes(manifest_bytes)
    manifest_ref = put_fixed(s3_prefix, namespace, "manifest", manifest_bytes)
    register_pg(psql, pg_dsn, Path(__file__).with_name("schema.sql"), manifest, manifest_ref["sha256"])
    return {"manifest": manifest, "manifest_ref": manifest_ref}
