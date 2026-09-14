"""Isolated ClickHouse/dbt/SeaweedFS producer gate for judged search qrels."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.parse
import urllib.error
import urllib.request

import jsonschema
import pyarrow as pa
import pyarrow.parquet as pq

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT / "warehouse" / "scripts"))
from engine import ClickHouse  # noqa: E402
from local_server import start as start_clickhouse  # noqa: E402
from local_s3 import start as start_s3  # noqa: E402

LANDING = "sea_search_qrel_landing"
SPLITS = {
    "train": {"start": "2026-09-01T00:00:00Z", "end": "2026-09-03T00:00:00Z"},
    "validation": {"start": "2026-09-03T00:00:00Z", "end": "2026-09-05T00:00:00Z"},
    "test": {"start": "2026-09-05T00:00:00Z", "end": "2026-09-07T00:00:00Z"},
}
CUTOFFS = {1: "2026-09-07T00:00:00Z", 2: "2026-09-09T00:00:00Z"}


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode()


def digest(body: bytes) -> str:
    return sha256(body).hexdigest()


def file_digest(path: Path) -> str:
    return digest(path.read_bytes())


def read_source(path: Path, seen: set[int]) -> tuple[bytes, list[dict]]:
    raw = path.read_bytes()
    if not raw.endswith(b"\n"):
        raise ValueError("source batch lacks final newline")
    rows = []
    for line in raw.splitlines():
        event = json.loads(line)
        expected = digest(canonical({key: value for key, value in event.items() if key not in ("batch_id", "payload_hash")}))
        if event["batch_id"] != path.stem or event["payload_hash"] != expected:
            raise ValueError("source batch identity/hash mismatch")
        sequence = event["source_sequence"]
        if not isinstance(sequence, int) or sequence in seen:
            raise ValueError("source offset repeated or invalid")
        seen.add(sequence)
        rows.append(event)
    if sorted(seen) != list(range(1, max(seen) + 1)):
        raise ValueError("source global prefix has a gap")
    return raw, rows


def put(url: str, body: bytes) -> None:
    assert_absent(url)
    with urllib.request.urlopen(urllib.request.Request(url, data=body, method="PUT"), timeout=30) as response:
        response.read()
    with urllib.request.urlopen(url, timeout=30) as response:
        if response.read() != body:
            raise ValueError("immutable S3 object differs after put")


def assert_absent(url: str) -> None:
    try:
        with urllib.request.urlopen(urllib.request.Request(url, method="HEAD"), timeout=10) as response:
            response.read()
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return
        raise
    raise ValueError("immutable S3 generation object already exists")


def dbt_build(dbt: Path, endpoint: str, generation: str, recipe: dict, output: Path) -> None:
    output.mkdir(parents=True, exist_ok=False)
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=generation, DBT_SEND_ANONYMOUS_USAGE_STATS="false",
               DBT_USE_COLORS="false")
    command = [str(dbt), "build", "--project-dir", str(HERE),
               "--profiles-dir", str(ROOT / "warehouse" / "environment"),
               "--target-path", str(output / "target"), "--log-path", str(output / "logs"),
               "--vars", json.dumps(recipe, sort_keys=True), "--no-partial-parse"]
    with (output / "dbt.log").open("wb") as log:
        result = subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT)
    if result.returncode:
        raise RuntimeError(f"search qrel dbt build failed: {output / 'dbt.log'}")
    states = json.loads((output / "target" / "run_results.json").read_text())["results"]
    if not states or any(item["status"] not in ("success", "pass") for item in states):
        raise RuntimeError("search qrel dbt DAG/test status incomplete")


def export(ch: ClickHouse, prefix: str, generation: str, revision: int, batches: list[Path],
           rows: list[dict], contracts: Path, build: Path, output: Path, parent_sha: str | None,
           *, substream: dict | None = None, split_plan: dict | None = None,
           cutoff: str | None = None) -> tuple[Path, dict]:
    output.mkdir(parents=True, exist_ok=False)
    effective_splits = split_plan if split_plan is not None else SPLITS
    effective_cutoff = cutoff if cutoff is not None else CUTOFFS[revision]
    columns = json.loads((contracts / "search-qrel.columns.v1.json").read_text())
    selected = ", ".join(column["name"] for column in columns)
    files = []
    split_receipts = []
    for split, bounds in effective_splits.items():
        name = f"{split}-00000.parquet"
        url = f"{prefix}/search-qrel/{generation}/{name}"
        assert_absent(url)
        ch.query(f"INSERT INTO FUNCTION s3('{url}', NOSIGN, 'Parquet') "
                 f"SELECT {selected} FROM {generation}.ds_search_qrels WHERE split='{split}' "
                 "ORDER BY query_id, document_id, document_revision, chunk_id, judgment_revision "
                 "SETTINGS output_format_parquet_string_as_string=1, s3_truncate_on_insert=0, s3_create_new_file_on_insert=0")
        local = output / name
        with urllib.request.urlopen(url, timeout=30) as response:
            local.write_bytes(response.read())
        parquet = pq.ParquetFile(local)
        actual = [{"name": field.name, "arrow_type": "float64" if pa.types.is_float64(field.type) else str(field.type),
                   "nullable": field.nullable} for field in parquet.schema_arrow]
        if actual != columns or parquet.metadata.num_rows < 1:
            raise ValueError(f"search qrel Parquet schema/rows differ for {split}: {actual!r}")
        files.append({"path": name, "split": split, "shard_index": 0, "rows": parquet.metadata.num_rows,
                      "size_bytes": local.stat().st_size, "sha256": file_digest(local)})
        split_receipts.append({"name": split, **bounds, "rows": parquet.metadata.num_rows, "shard_count": 1})
    visible = ch.rows(f"SELECT judgment_id,judgment_revision,query_id,document_id,document_revision,chunk_id,"
                      f"chunk_text_sha256,content_available_at,relevance_grade,split "
                      f"FROM {generation}.ds_search_qrels ORDER BY judgment_id")
    content = sorted({(item["document_id"], item["document_revision"], item["chunk_id"],
                      item["chunk_text_sha256"], item["content_available_at"]) for item in visible})
    watermark = {"source": "synthetic-search-qrel", "partition": "fixture-0",
                 "position": max(item["source_sequence"] for item in rows),
                 "event_time": max(item["available_at"] for item in rows)}
    if substream is not None:
        watermark = {"source": substream["producer"], "partition": substream["source_partition"],
                     "position": substream["qrel_count"],
                     "event_time": max(item["available_at"] for item in rows)}
    recipe = {"cutoff": effective_cutoff, "splits": effective_splits,
              "batches": [path.stem for path in batches]}
    if substream is not None:
        recipe["substream"] = {key: substream[key] for key in
                               ("coverage_root", "coverage_sha256", "through_offset",
                                "technical_skip_count", "grouping_policy_sha256")}
    source = {
        "warehouse_run_id": generation, "project_revision": subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(), "generation": generation,
        "ingest_cutoff": effective_cutoff,
        "batches": [{"batch_id": path.stem, "sha256": file_digest(path)} for path in batches],
        "watermarks": [watermark],
        "judgment_snapshot_sha256": digest(canonical(visible)),
        "content_snapshot_sha256": digest(canonical(content)),
        "dbt_manifest_sha256": file_digest(build / "target" / "manifest.json"),
        "dbt_run_results_sha256": file_digest(build / "target" / "run_results.json"),
        "recipe_sha256": digest(canonical(recipe)),
    }
    manifest = {
        "schema_version": "sea.search-qrel-dataset.v1", "row_contract": "sea.search-qrel.v1",
        "dataset_id": "search-qrel-rtw-substream-synthetic-fixture" if substream is not None else
                      "search-qrel-synthetic-fixture", "revision": revision,
        "parent_revision": revision - 1 if parent_sha else None,
        "parent_manifest_sha256": parent_sha,
        "domain": "search", "data_kind": "synthetic",
        "created_at": datetime.now(timezone.utc).isoformat(),
        "judgment_policy_id": "rtw-graded-synthetic-fixture-v1" if substream is not None else
                              "synthetic-graded-qrel-v1",
        "split_policy_id": substream["grouping_policy_id"] if substream is not None else
                           "query-time-family-nearcluster-v1", "near_duplicate_scope": "query",
        "columns": columns, "source": source, "splits": split_receipts, "files": files,
        "row_count": sum(item["rows"] for item in files),
    }
    schema = json.loads((contracts / "search-qrel-dataset-manifest.v1.schema.json").read_text())
    jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker()).validate(manifest)
    path = output / "manifest.json"
    path.write_text(json.dumps(manifest, sort_keys=True, indent=2) + "\n")
    put(f"{prefix}/search-qrel/{generation}/manifest.json", path.read_bytes())
    return path, manifest


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--contracts", required=True)
    parser.add_argument("--reader-root", help="independent WS09-A checkout; verifies original live S3 bytes")
    args = parser.parse_args()
    runtime, output, contracts = Path(args.runtime).resolve(), Path(args.output).resolve(), Path(args.contracts).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise RuntimeError(f"locked runtime dependency missing: {name}")
    ch_proc = s3_proc = None
    try:
        ch_proc, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        s3_proc, prefix = start_s3(runtime / "weed", output / "seaweed")
        ch = ClickHouse(endpoint)
        ch.query(f"CREATE DATABASE {LANDING}")
        ddl = (HERE / "landing.sql").read_text().format(database=LANDING)
        ch.query(ddl)
        structure = ddl.split("(\n", 1)[1].split(") ENGINE", 1)[0].replace("'", "''")
        seen: set[int] = set()
        all_rows: list[dict] = []
        previous_manifest = None
        first_bytes: dict[str, bytes] = {}
        reports = {}
        for revision in (1, 2):
            paths = [HERE / "fixtures" / "v1.jsonl"] if revision == 1 else [HERE / "fixtures" / "v1.jsonl", HERE / "fixtures" / "v2.jsonl"]
            new_path = paths[-1]
            raw, new_rows = read_source(new_path, seen)
            all_rows.extend(new_rows)
            archive_url = f"{prefix}/search-qrel/archive/{new_path.stem}/{digest(raw)}.jsonl"
            put(archive_url, raw)
            ch.query(f"INSERT INTO {LANDING}.qrel_history SELECT * FROM s3('{archive_url}', NOSIGN, 'JSONEachRow', '{structure}') "
                     "SETTINGS date_time_input_format='best_effort'")
            generation = f"sea_search_qrel_g{revision}"
            recipe = {"landing_schema": LANDING, "ingest_cutoff": CUTOFFS[revision], "splits": SPLITS}
            build = output / f"build-v{revision}"
            dbt_build(runtime / ".venv/bin/dbt", endpoint, generation, recipe, build)
            manifest_path, manifest = export(ch, prefix, generation, revision, paths, all_rows,
                                             contracts, build, output / f"dataset-v{revision}", previous_manifest)
            if revision == 1:
                first_bytes = {p.name: p.read_bytes() for p in manifest_path.parent.iterdir() if p.is_file()}
                try:
                    export(ch, prefix, generation, revision, paths, all_rows, contracts,
                           build, output / "rejected-overwrite", None)
                except ValueError as error:
                    if "already exists" not in str(error):
                        raise
                else:
                    raise AssertionError("immutable search qrel generation was overwritten")
            else:
                for name, original in first_bytes.items():
                    if (output / "dataset-v1" / name).read_bytes() != original:
                        raise ValueError("v1 frozen dataset changed after v2")
            reports[f"v{revision}"] = {"manifest_sha256": file_digest(manifest_path),
                                       "row_count": manifest["row_count"],
                                       "splits": {item["name"]: item["rows"] for item in manifest["splits"]},
                                       "source_offset": max(seen)}
            previous_manifest = file_digest(manifest_path)
        replay_generation = "sea_search_qrel_g1_replay"
        replay_recipe = {"landing_schema": LANDING, "ingest_cutoff": CUTOFFS[1], "splits": SPLITS}
        dbt_build(runtime / ".venv/bin/dbt", endpoint, replay_generation, replay_recipe, output / "build-v1-replay")
        first_rows = ch.rows("SELECT * FROM sea_search_qrel_g1.ds_search_qrels ORDER BY judgment_id")
        replay_rows = ch.rows(f"SELECT * FROM {replay_generation}.ds_search_qrels ORDER BY judgment_id")
        if first_rows != replay_rows:
            raise ValueError("v1 historical qrels changed after v2 source arrived")
        negative = {"generation_overwrite": "rejected"}
        bad_file = output / "bad-source" / "v1.jsonl"
        bad_file.parent.mkdir()
        bad_raw = (HERE / "fixtures" / "v1.jsonl").read_bytes()
        bad_file.write_bytes(bad_raw.replace(b'"payload_hash":"', b'"payload_hash":"0', 1))
        try:
            read_source(bad_file, set())
        except ValueError as error:
            if "hash" not in str(error):
                raise
            negative["source_hash"] = "rejected"
        else:
            raise AssertionError("tampered source hash passed")
        leak = dict(next(item for item in all_rows if item["query_id"] == "q-test-coffee"))
        leak.update(batch_id="bad_split", event_id="judge.q-test-leak.leak-doc.r1",
                    judgment_id="judge.q-test-leak.leak-doc", query_id="q-test-leak",
                    query_family_id="family-leak", near_duplicate_cluster_id="near-train",
                    document_id="leak-doc", chunk_id="leak-doc:chunk-1", source_sequence=max(seen) + 1)
        leak["query_text"] = "synthetic leaked search query"
        leak["query_text_sha256"] = digest(leak["query_text"].encode())
        leak["chunk_text"] = "Synthetic leakage fixture passage."
        leak["chunk_text_sha256"] = digest(leak["chunk_text"].encode())
        leak["judgment_source_ref"] = "synthetic-search-qrel/judge.q-test-leak.leak-doc/r1"
        leak["judgment_source_hash"] = digest(canonical({"source_ref": leak["judgment_source_ref"], "grade": leak["relevance_grade"]}))
        leak["payload_hash"] = digest(canonical({key: value for key, value in leak.items() if key not in ("batch_id", "payload_hash")}))
        ch.query(f"INSERT INTO {LANDING}.qrel_history SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow",
                 canonical(leak) + b"\n")
        bad_build = output / "bad-split"
        try:
            dbt_build(runtime / ".venv/bin/dbt", endpoint, "sea_search_qrel_bad_split",
                      {"landing_schema": LANDING, "ingest_cutoff": CUTOFFS[2], "splits": SPLITS}, bad_build)
        except RuntimeError:
            if "no_split_leakage" not in (bad_build / "dbt.log").read_text():
                raise RuntimeError("bad split failed for the wrong reason")
            negative["query_near_duplicate_cross_split"] = "rejected"
        else:
            raise AssertionError("query near-duplicate cluster leaked across splits")
        reader_receipts = None
        if args.reader_root:
            sys.path.insert(0, str(Path(args.reader_root).resolve() / "training" / "src"))
            from sea_training.search_dataset import open_search_dataset
            from pyarrow import fs
            parsed = urllib.parse.urlsplit(prefix)
            filesystem = fs.S3FileSystem(anonymous=True, region="us-east-1", scheme="http",
                                         endpoint_override=f"{parsed.hostname}:{parsed.port}")
            bucket = parsed.path.lstrip("/")
            reader_receipts = {}
            for revision in (1, 2):
                name = f"v{revision}"
                uri = f"{bucket}/search-qrel/sea_search_qrel_g{revision}/manifest.json"
                with open_search_dataset(uri, expected_manifest_sha256=reports[name]["manifest_sha256"],
                                         schema_dir=contracts, filesystem=filesystem) as snapshot:
                    if snapshot.report["validated_rows"] != reports[name]["row_count"] or \
                            snapshot.report["status"] != "validated_synthetic_fixture":
                        raise ValueError("independent S3 reader did not accept exact synthetic generation")
                    reader_receipts[name] = snapshot.report
        report = {"status": "passed", "evidence_level": "L2_synthetic_fixture_real_CH_dbt_SeaweedFS",
                  "reader_acceptance": reader_receipts if reader_receipts is not None else "not_run", "generations": reports,
                  "activation": "none", "observed_qrels": None,
                  "historical_v1_replay": "equal_after_v2", "negative_gates": negative}
        (output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
        print(json.dumps(report, sort_keys=True), flush=True)
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
