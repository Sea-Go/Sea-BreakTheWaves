"""Run WS07-E against task-owned PostgreSQL, ClickHouse/dbt and SeaweedFS."""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
import urllib.request
from pathlib import Path

PROJECT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT / "scripts"))

from engine import ClickHouse, build  # noqa: E402
from local_server import free_port, start as start_clickhouse  # noqa: E402
from local_s3 import start as start_s3  # noqa: E402
from receipt import project_revision  # noqa: E402
from verify import parameters  # noqa: E402
from freeze import canonical, file_sha, freeze, register_pg, sha, validate_source, validate_synthetic_source_files  # noqa: E402


def recipe(revision: int, *, source_kind: str = "synthetic",
           coverage: str = "synthetic_fixture_complete", receipt: str = "") -> dict:
    value = parameters(revision)
    value["source_batches"] = ["ads_v1"] if revision == 1 else ["ads_v1", "ads_v2"]
    value.update(landing_schema="ads_fixture_landing",
                 ads_evaluation_cutoff=value["event_watermark"],
                 ads_source_kind=source_kind,
                 ads_source_coverage_state=coverage,
                 ads_coverage_receipt_sha256=receipt,
                 ads_definition_revision="ads-exposure-r1")
    return value


def assert_states(ch: ClickHouse, generation: str, expected: dict[str, str]) -> list[dict]:
    rows = ch.rows(f"SELECT impression_id,evaluation_state,mature_label,cohort,assignment_state "
                   f"FROM {generation}.ads_recommendation_exposures ORDER BY impression_id")
    if {r["impression_id"]: r["evaluation_state"] for r in rows} != expected:
        raise AssertionError(f"{generation} ADS states differ from hand calculation: {rows}")
    if any(r["mature_label"] is not None for r in rows if not r["evaluation_state"].startswith("mature_")):
        raise AssertionError("non-evaluable exposure became an observed negative")
    if any(r["cohort"] != "unassigned" or r["assignment_state"] != "missing_authoritative_assignment"
           for r in rows):
        raise AssertionError("unknown assignment was filled with an experiment cohort")
    return rows


def source_gap_batch(destination: Path) -> Path:
    chain = {"feature-1", "request-1", "candidate-1", "impression-1", "read-1"}
    events = []
    for line in (PROJECT / "ads/fixtures/ads_v1.jsonl").read_text().splitlines():
        original = json.loads(line)
        if original["event_id"] not in chain:
            continue
        event = dict(original, batch_id="ads_gaps", event_id="ads-gap-" + original["event_id"],
                     subject_id="source-gap-user", source_partition="ads-gap",
                     source_sequence=len(events) + 1)
        for key, prefix in (("request_id", "ads-gap-"), ("candidate_id", "ads-gap-"),
                            ("impression_id", "ads-gap-"), ("feature_snapshot_ref", "ads-gap-")):
            if event[key]:
                event[key] = prefix + event[key]
        if event["event_type"] == "impression":
            event["source_partition"] = ""  # accepted event without an H09 source receipt
        event["payload_hash"] = sha(json.dumps(
            {k: v for k, v in event.items() if k not in ("batch_id", "payload_hash")},
            sort_keys=True).encode())
        events.append(event)
    path = destination / "ads_gaps.jsonl"
    path.write_text("".join(json.dumps(e, sort_keys=True) + "\n" for e in events))
    return path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--postgres-bin", default="/opt/homebrew/opt/postgresql@16/bin")
    args = parser.parse_args()
    runtime, root = Path(args.runtime).resolve(), Path(args.output).resolve()
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise SystemExit(f"missing locked warehouse runtime dependency: {name}")
    root.mkdir(parents=True, exist_ok=False)
    pg_bin = Path(args.postgres_bin)
    port = free_port()
    pg_dir = root / "postgres"
    with (root / "initdb.log").open("wb") as log:
        subprocess.run([str(pg_bin / "initdb"), "-D", str(pg_dir), "--no-locale",
                        "--encoding=UTF8", "--auth=trust", "-U", "sea_ads_test"], check=True, stdout=log)
    pg_started = False
    ch_process = s3_process = None
    try:
        subprocess.run([str(pg_bin / "pg_ctl"), "-D", str(pg_dir), "-l", str(root / "postgres.log"),
                        "-o", f"-h 127.0.0.1 -p {port} -k {root}", "-w", "start"], check=True)
        pg_started = True
        dsn = f"postgres://sea_ads_test@127.0.0.1:{port}/postgres?sslmode=disable"
        ch_process, endpoint = start_clickhouse(runtime / "clickhouse", root / "clickhouse")
        s3_process, s3_prefix = start_s3(runtime / "weed", root / "seaweed")
        ch = ClickHouse(endpoint)
        source_revision = project_revision()
        v1, v2 = PROJECT / "ads/fixtures/ads_v1.jsonl", PROJECT / "ads/fixtures/ads_v2.jsonl"
        validate_synthetic_source_files([v1])
        validate_synthetic_source_files([v1, v2])
        ch.load("ads_fixture_landing", v1, s3_prefix)
        r1 = recipe(1)
        build(runtime / ".venv/bin/dbt", endpoint, "ads_gen_v1", r1, root / "dbt_v1")
        assert_states(ch, "ads_gen_v1", {
            "i1": "mature_positive", "i2": "mature_negative",
            "i3": "mature_positive", "i4": "mature_negative",
            "i5": "pending", "i6": "excluded:future_feature", "i7": "pending",
        })
        first = freeze(ch, "ads_gen_v1", r1, root / "dbt_v1", [v1], root / "frozen_v1",
                       s3_prefix, str(pg_bin / "psql"), dsn, source_revision)
        if first["manifest"]["counts"] != {
                "visible": 7, "served_visible": 7, "mature_positive": 2,
                "mature_negative": 2, "mature_evaluable_denominator": 4}:
            raise AssertionError("first generation ADS counts differ from hand calculation")
        frozen_v1 = (root / "frozen_v1/rows.jsonl").read_bytes()
        remote_v1 = first["manifest"]["files"]["rows"]["url"]

        ch.load("ads_fixture_landing", v2, s3_prefix)
        r2 = recipe(2)
        build(runtime / ".venv/bin/dbt", endpoint, "ads_gen_v2", r2, root / "dbt_v2")
        assert_states(ch, "ads_gen_v2", {
            "i1": "mature_positive", "i2": "mature_negative",
            "i3": "mature_positive", "i4": "mature_negative",
            "i5": "mature_positive", "i6": "excluded:future_feature", "i7": "mature_negative",
        })
        second = freeze(ch, "ads_gen_v2", r2, root / "dbt_v2", [v1, v2], root / "frozen_v2",
                        s3_prefix, str(pg_bin / "psql"), dsn, source_revision)
        if first["manifest"]["files"]["rows"]["sha256"] == second["manifest"]["files"]["rows"]["sha256"]:
            raise AssertionError("late mature evidence did not create a new frozen ADS revision")
        if second["manifest"]["counts"] != {
                "visible": 7, "served_visible": 7, "mature_positive": 3,
                "mature_negative": 3, "mature_evaluable_denominator": 6}:
            raise AssertionError("second generation ADS counts differ from hand calculation")
        with urllib.request.urlopen(remote_v1, timeout=30) as response:
            if response.read() != frozen_v1 or file_sha(root / "frozen_v1/rows.jsonl") != sha(frozen_v1):
                raise AssertionError("late label changed first-generation frozen evidence")
        build(runtime / ".venv/bin/dbt", endpoint, "ads_gen_v1_replay", r1, root / "dbt_v1_replay")
        replay_rows = ch.query("SELECT * FROM ads_gen_v1_replay.ads_recommendation_exposures "
                               "ORDER BY authority_id,tenant_id,subject_id,impression_id FORMAT JSONEachRow")
        if replay_rows != frozen_v1:
            raise AssertionError("fixed v1 ADS recipe changed after later source batches arrived")
        receipt_query = subprocess.run(
            [str(pg_bin / "psql"), "-X", "-q", "-t", "-A", "-d", dsn, "-c",
             "SELECT generation,trim(manifest_sha256) FROM warehouse_ads_generations ORDER BY generation"],
            check=True, capture_output=True, text=True)
        pg_receipts = dict(line.split("|", 1) for line in receipt_query.stdout.strip().splitlines())
        if pg_receipts != {"ads_gen_v1": first["manifest_ref"]["sha256"],
                           "ads_gen_v2": second["manifest_ref"]["sha256"]}:
            raise AssertionError("PG ADS receipts differ from frozen S3 manifest hashes")
        try:
            register_pg(str(pg_bin / "psql"), dsn, Path(__file__).with_name("schema.sql"),
                        first["manifest"], "0" * 64)
            raise AssertionError("PG accepted a different manifest for a frozen ADS generation")
        except ValueError:
            pass

        unknown = recipe(1, source_kind="observed", coverage="unverified")
        build(runtime / ".venv/bin/dbt", endpoint, "ads_unapproved", unknown, root / "dbt_unapproved")
        unapproved = ch.rows("SELECT evaluation_state,mature_label FROM ads_unapproved.ads_recommendation_exposures")
        if any(row["mature_label"] is not None for row in unapproved):
            raise AssertionError("unapproved observed input produced a mature label")
        if {row["evaluation_state"] for row in unapproved} != {"coverage_unverified", "excluded:future_feature"}:
            raise AssertionError("unapproved source did not preserve an explicit unavailable state")
        try:
            validate_source(unknown, [{"batch_id": "ads_v1", "sha256": file_sha(v1)}])
            raise AssertionError("unapproved observed source passed the freeze gate")
        except ValueError:
            pass
        # Contract fixture only: exercise the future observed SQL branch with
        # an explicit authoritative-verifier callback, but NEVER freeze or
        # register these synthetic rows as observed H09 data.
        coverage_fixture = {"authority": "h09-contract-fixture", "status": "verified_complete",
                            "source_batches": ["ads_v1"], "watermark": r1["event_watermark"],
                            "source_watermarks": [{"partition": "fixture-0", "contiguous_sequence": 34,
                                                   "complete": True, "event_time": r1["event_watermark"]}]}
        receipt_hash = sha(canonical(coverage_fixture))
        observed_preview = recipe(1, source_kind="observed", coverage="verified_complete", receipt=receipt_hash)
        observed_preview["ads_source_watermarks"] = coverage_fixture["source_watermarks"]
        validate_source(observed_preview, [{"batch_id": "ads_v1", "sha256": file_sha(v1)}],
                        lambda digest, recipe, batches: digest == receipt_hash and
                        recipe["source_batches"] == coverage_fixture["source_batches"] and
                        recipe["ads_source_watermarks"] == coverage_fixture["source_watermarks"] and
                        len(batches) == 1)
        build(runtime / ".venv/bin/dbt", endpoint, "ads_observed_contract_preview",
              observed_preview, root / "dbt_observed_contract")
        preview = ch.rows("SELECT evaluation_state FROM ads_observed_contract_preview.ads_recommendation_exposures")
        if sum(row["evaluation_state"] in ("mature_positive", "mature_negative") for row in preview) != 4:
            raise AssertionError("approved observed-source SQL branch is unreachable")

        gap = source_gap_batch(root)
        ch.load("ads_fixture_landing", gap, s3_prefix)
        gap_recipe = recipe(1)
        gap_recipe["source_batches"] = ["ads_v1", "ads_gaps"]
        build(runtime / ".venv/bin/dbt", endpoint, "ads_source_gap", gap_recipe, root / "dbt_source_gap")
        gap_row = ch.rows("SELECT evaluation_state,mature_label FROM ads_source_gap.ads_recommendation_exposures "
                          "WHERE impression_id='ads-gap-i1'")
        if gap_row != [{"evaluation_state": "source_unavailable", "mature_label": None}]:
            raise AssertionError(f"missing impression provenance became an outcome: {gap_row}")

        report = {
            "status": "passed", "evidence_level": "L2_isolated_synthetic_cross_storage",
            "model_or_experiment_effect": None,
            "generations": {
                "v1": {"manifest_sha256": first["manifest_ref"]["sha256"],
                       "rows_sha256": first["manifest"]["files"]["rows"]["sha256"],
                       "counts": first["manifest"]["counts"]},
                "v2": {"manifest_sha256": second["manifest_ref"]["sha256"],
                       "rows_sha256": second["manifest"]["files"]["rows"]["sha256"],
                       "counts": second["manifest"]["counts"]},
            },
            "observed_preview": "contract_only_not_published",
            "unapproved_source": "not_evaluable",
            "source_gap": "not_evaluable",
            "pg_receipts": len(pg_receipts),
        }
        (root / "report.json").write_bytes(canonical(report) + b"\n")
        print(json.dumps(report, sort_keys=True, indent=2), flush=True)
    finally:
        for process in (s3_process, ch_process):
            if process is not None:
                process.terminate()
        try:
            for process in (s3_process, ch_process):
                if process is None:
                    continue
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        finally:
            if pg_started:
                subprocess.run([str(pg_bin / "pg_ctl"), "-D", str(pg_dir), "-m", "fast",
                                "-w", "stop"], check=True)


if __name__ == "__main__":
    main()
