"""Run the real RTW/DC community source through PG, CH/dbt, and S3."""
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
REPO = WAREHOUSE.parent
sys.path.insert(0, str(WAREHOUSE / "scripts"))
from engine import ClickHouse  # noqa: E402
from local_server import start as start_clickhouse  # noqa: E402
from local_s3 import start as start_s3  # noqa: E402


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode()


def digest(body: bytes) -> str:
    return hashlib.sha256(body).hexdigest()


def put_fixed(url: str, body: bytes) -> None:
    request = urllib.request.Request(url, data=body, method="PUT")
    with urllib.request.urlopen(request, timeout=30) as response:
        if response.status not in (200, 201):
            raise AssertionError(f"S3 PUT status {response.status}")
    with urllib.request.urlopen(url, timeout=30) as response:
        if response.read() != body:
            raise AssertionError("S3 read-after-write bytes differ")


def get(url: str) -> bytes:
    with urllib.request.urlopen(url, timeout=30) as response:
        return response.read()


def run_go(output: Path, s3_prefix: str) -> tuple[bytes, dict]:
    ods_path, publication_path = output / "ods.jsonl", output / "coverage.json"
    env = os.environ.copy()
    env.update(
        COMMUNITY_WAREHOUSE_S3_PREFIX=s3_prefix,
        COMMUNITY_WAREHOUSE_ODS_OUTPUT=str(ods_path),
        COMMUNITY_WAREHOUSE_PUBLICATION_OUTPUT=str(publication_path),
        GOFLAGS="-p=2",
        GOMAXPROCS="2",
    )
    with (output / "warehouse-go.log").open("wb") as log:
        subprocess.run(
            ["go", "test", "-mod=readonly", "-race", "-count=1", "-v", "-run", "^TestRealCommunityWarehouseChain$",
             "./internal/warehouse/communitysource"],
            cwd=REPO,
            env=env,
            stdout=log,
            stderr=subprocess.STDOUT,
            check=True,
            timeout=180,
        )
    records = []
    for line in (output / "warehouse-go.log").read_text().splitlines():
        if not line.startswith("{"):
            continue
        record = json.loads(line)
        required = {"timestamp", "level", "service", "environment", "service_version", "instance_id",
                    "component", "log_source", "event", "message"}
        if required - record.keys():
            raise AssertionError(f"warehouse structured log fields missing: {required - record.keys()}")
        records.append(record)
    finished = [record for record in records if record.get("event") in
                ("warehouse.community.batch.finished", "warehouse.community.coverage.finished")]
    if not finished or any("outcome" not in record or "duration_ms" not in record for record in finished):
        raise AssertionError("warehouse terminal structured logs missing")
    return ods_path.read_bytes(), json.loads(publication_path.read_text())


def verify_ods(source: bytes) -> list[dict]:
    rows = [json.loads(line) for line in source.splitlines()]
    if len(rows) != 6:
        raise AssertionError(f"expected six ODS rows, got {len(rows)}")
    by_producer = {
        producer: [row for row in rows if row["producer"] == producer]
        for producer in ("rtw.comment-rpc", "rtw.like-mq")
    }
    if [row["source_offset"] for row in by_producer["rtw.comment-rpc"]] != [1, 2, 3, 4]:
        raise AssertionError("comment offsets are not a complete independent prefix")
    if [row["source_offset"] for row in by_producer["rtw.like-mq"]] != [1, 2]:
        raise AssertionError("like offsets are not a complete independent prefix")
    if [row["operation"] for row in by_producer["rtw.comment-rpc"]] != ["create", "like", "unlike", "retract"]:
        raise AssertionError("comment transitions differ from source order")
    if [row["operation"] for row in by_producer["rtw.like-mq"]] != ["like", "unlike"]:
        raise AssertionError("target reaction transitions differ from source order")
    forbidden = {"tenant_id", "realm", "impression_id", "label", "training_label", "content_revision"}
    for row in rows:
        if forbidden.intersection(row):
            raise AssertionError(f"ODS exposed forbidden fields: {forbidden.intersection(row)}")
        if row["issuer"] != "rtw.identity" or not row["subject_id"].isdigit() or row["subject_id"].startswith("0"):
            raise AssertionError("ODS SubjectRef v2 differs")
        if row["target_revision"] is not None or row["revision_status"] != "unknown":
            raise AssertionError("ODS guessed a target revision")
        if row["producer"] == "rtw.comment-rpc" and row["search_evidence"] is not False:
            raise AssertionError("comment was promoted to search evidence")
        if row["producer"] == "rtw.like-mq" and row["search_evidence"] is not None:
            raise AssertionError("target reaction invented search evidence")
        event = json.loads(row["event_spec"])
        receipt = json.loads(row["technical_receipt"])
        if digest(canonical(event)) != row["source_event_hash"] or receipt["input_hash"] != row["source_event_hash"]:
            raise AssertionError("EventSpec JCS or receipt hash differs")
        if receipt["event_id"] != row["event_id"] or receipt["producer"] != row["producer"] or receipt["offset"] != row["source_offset"]:
            raise AssertionError("technical receipt differs from ODS position")
    return rows


def verify_coverage(publication: dict) -> dict:
    negative = publication.get("negative_checks", {})
    if not negative or not all(negative.values()):
        raise AssertionError(f"negative/replay checks incomplete: {negative}")
    summary = {}
    for name, producer, through, subjects in (
        ("comment", "rtw.comment-rpc", "4", 2),
        ("like", "rtw.like-mq", "2", 1),
    ):
        item = publication[name]
        prefix = item["prefix"]
        manifest = prefix["manifest"]
        if manifest["producer"] != producer or manifest["through_offset"] != through or manifest["origin"] != "1":
            raise AssertionError(f"{name} prefix scope differs")
        manifest_body = get(prefix["manifest_url"])
        if digest(manifest_body) != prefix["manifest_sha256"] or json.loads(manifest_body) != manifest:
            raise AssertionError(f"{name} prefix manifest differs")
        index_body = get(manifest["event_index_url"])
        batch_body = get(manifest["batch_evidence_url"])
        if digest(index_body) != manifest["event_index_sha256"] or digest(batch_body) != manifest["batch_evidence_sha256"]:
            raise AssertionError(f"{name} prefix object hash differs")
        index = [json.loads(line) for line in index_body.splitlines()]
        if len(index) != int(through) or [row["offset"] for row in index] != [str(i) for i in range(1, int(through) + 1)]:
            raise AssertionError(f"{name} event index is incomplete")
        if any(row["producer"] != producer or set(row["subject_ref"]) != {"issuer", "subject_id"} for row in index):
            raise AssertionError(f"{name} index mixed producer or identity shape")
        if len(item["subjects"]) != subjects:
            raise AssertionError(f"{name} subject slice count differs")
        for subject in item["subjects"]:
            coverage = subject["coverage"]
            receipt_body = get(subject["receipt_url"])
            sparse_body = get(coverage["sparse_index_url"])
            if digest(receipt_body) != subject["receipt_sha256"] or json.loads(receipt_body) != coverage:
                raise AssertionError(f"{name} subject receipt differs")
            if digest(sparse_body) != coverage["sparse_index_sha256"] or len(sparse_body.splitlines()) != coverage["event_count"]:
                raise AssertionError(f"{name} subject sparse index differs")
        summary[name] = {
            "producer": producer,
            "through_offset": int(through),
            "prefix_manifest_sha256": prefix["manifest_sha256"],
            "subject_receipts": [subject["receipt_sha256"] for subject in item["subjects"]],
        }
    if publication["comment"]["prefix"]["manifest_sha256"] == publication["like"]["prefix"]["manifest_sha256"]:
        raise AssertionError("two producer prefixes were merged")
    return {"prefixes": summary, "negative_checks": negative}


def run_dbt(runtime: Path, output: Path, endpoint: str, source: bytes) -> tuple[bytes, dict]:
    ch = ClickHouse(endpoint)
    ch.query("CREATE DATABASE community_landing")
    ch.query((HERE / "landing.sql").read_text().format(database="community_landing"))
    ch.query("INSERT INTO community_landing.ods_community_event SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", source)
    env = os.environ.copy()
    env.update(
        WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
        WAREHOUSE_GENERATION_SCHEMA="community_gen_v1",
        DBT_SEND_ANONYMOUS_USAGE_STATS="false",
        DBT_USE_COLORS="false",
    )
    target = output / "dbt"
    with (output / "dbt.log").open("wb") as log:
        subprocess.run(
            [str(runtime / ".venv/bin/dbt"), "build", "--project-dir", str(HERE),
             "--profiles-dir", str(WAREHOUSE / "environment"), "--target-path", str(target),
             "--log-path", str(output / "dbt_logs"), "--vars", json.dumps({"community_landing_schema": "community_landing"}),
             "--no-partial-parse"],
            stdout=log,
            stderr=subprocess.STDOUT,
            env=env,
            check=True,
            timeout=180,
        )
    results = json.loads((target / "run_results.json").read_text())
    if len(results["results"]) != 5 or any(result["status"] not in ("success", "pass") for result in results["results"]):
        raise AssertionError("dbt did not pass one model and four tests")
    rows = ch.rows("SELECT producer,source_offset,transition_type,issuer,subject_id,target_revision,revision_status,"
                   "search_evidence,predecessor_event_id FROM community_gen_v1.dwd_community_transition "
                   "ORDER BY producer,source_offset")
    expected = [
        ("rtw.comment-rpc", 1, "comment_create"),
        ("rtw.comment-rpc", 2, "comment_like"),
        ("rtw.comment-rpc", 3, "comment_unlike"),
        ("rtw.comment-rpc", 4, "comment_delete"),
        ("rtw.like-mq", 1, "target_like"),
        ("rtw.like-mq", 2, "target_unlike"),
    ]
    if [(row["producer"], row["source_offset"], row["transition_type"]) for row in rows] != expected:
        raise AssertionError(f"DWD transitions differ: {rows}")
    for row in rows:
        if row["issuer"] != "rtw.identity" or row["target_revision"] is not None or row["revision_status"] != "unknown":
            raise AssertionError("DWD identity or revision precision differs")
        if row["producer"] == "rtw.comment-rpc" and row["search_evidence"] is not False:
            raise AssertionError("DWD comment search evidence differs")
        if row["producer"] == "rtw.like-mq" and row["search_evidence"] is not None:
            raise AssertionError("DWD target reaction search evidence differs")
    columns = {row["name"] for row in ch.rows("SELECT name FROM system.columns WHERE database='community_gen_v1' "
                                              "AND table='dwd_community_transition'")}
    forbidden = {"tenant_id", "realm", "impression_id", "label", "training_label", "sample_id", "content_revision"}
    if columns.intersection(forbidden):
        raise AssertionError(f"DWD exposed forbidden semantics: {columns.intersection(forbidden)}")
    dwd = ch.query("SELECT * FROM community_gen_v1.dwd_community_transition ORDER BY producer,source_offset FORMAT JSONEachRow")
    return dwd, {
        "dbt_manifest_sha256": digest((target / "manifest.json").read_bytes()),
        "dbt_run_results_sha256": digest((target / "run_results.json").read_bytes()),
        "transition_count": len(rows),
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    runtime, output = Path(args.runtime).resolve(), Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for relative in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / relative).exists():
            raise RuntimeError(f"locked runtime missing: {relative}")
    ch_process = s3_process = None
    try:
        ch_process, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        # This isolated instance reserves 1GiB before becoming read-only;
        # SeaweedFS's default 1% of a large host volume would exceed its
        # remaining free space despite our three 100MiB test-volume limit.
        s3_process, s3_prefix = start_s3(runtime / "weed", output / "seaweed", min_free_space="1GiB")
        source, publication = run_go(output, s3_prefix)
        rows = verify_ods(source)
        coverage = verify_coverage(publication)
        ods_hash = digest(source)
        ods_url = f"{s3_prefix}/warehouse-community/ods/{ods_hash}.jsonl"
        put_fixed(ods_url, source)
        dwd, dbt = run_dbt(runtime, output, endpoint, source)
        dwd_hash = digest(dwd)
        dwd_url = f"{s3_prefix}/warehouse-community/dwd/{dwd_hash}.jsonl"
        put_fixed(dwd_url, dwd)
        report = {
            "schema_version": "sea.community-warehouse-acceptance.v1",
            "outcome": "passed",
            "datacenter_sha": os.environ["COMMUNITY_WAREHOUSE_DC_SHA"],
            "ridethewind_sha": os.environ["COMMUNITY_WAREHOUSE_RTW_SHA"],
            "breakthewaves_sha": os.environ["COMMUNITY_WAREHOUSE_BTW_SHA"],
            "source_rows": len(rows),
            "producer_offsets": {"rtw.comment-rpc": [1, 2, 3, 4], "rtw.like-mq": [1, 2]},
            "subject_ref": {"wire_version": 2, "issuer": "rtw.identity", "additional_partition": None},
            "source_precision": {"target_revision": None, "revision_status": "unknown", "comment_search_evidence": False},
            "ods_sha256": ods_hash,
            "ods_s3_url": ods_url,
            "dwd_sha256": dwd_hash,
            "dwd_s3_url": dwd_url,
            "coverage": coverage,
            "dbt": dbt,
            "excluded_outputs": ["dws_feature", "sample", "training_label", "impression", "unclicked_negative", "recommendation_activation"],
        }
        (output / "report.json").write_bytes(canonical(report) + b"\n")
        print(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2), flush=True)
    finally:
        for process in (s3_process, ch_process):
            if process is not None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)


if __name__ == "__main__":
    main()
