"""One live CH/dbt -> SeaweedFS -> pinned-reader -> lexical candidate gate.

This reuses the warehouse/search producer implementation as a read-only
dependency. The training slice owns only its own orchestration and candidate.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
from pathlib import Path
import subprocess
from urllib.parse import urlsplit

from pyarrow import fs

from sea_training.dataset import DatasetError

from search_training.authoritative import open_authoritative_pairs
from search_training.trainer import TrainConfig, train


ROOT = Path(__file__).resolve().parents[2]
PRODUCER_PATH = ROOT / "warehouse" / "search" / "acceptance.py"


def producer_module():
    spec = importlib.util.spec_from_file_location("sea_search_qrel_producer", PRODUCER_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError("search-qrel warehouse producer unavailable")
    producer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(producer)
    return producer


def run(runtime: Path, output: Path) -> dict:
    if output.exists():
        raise RuntimeError("search training evidence directory already exists")
    output.mkdir(parents=True)
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise RuntimeError(f"locked warehouse runtime missing {name}")
    producer = producer_module()
    ch_proc = s3_proc = None
    try:
        ch_proc, endpoint = producer.start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        s3_proc, prefix = producer.start_s3(runtime / "weed", output / "seaweed")
        ch = producer.ClickHouse(endpoint)
        ch.query(f"CREATE DATABASE {producer.LANDING}")
        ddl = (producer.HERE / "landing.sql").read_text().format(database=producer.LANDING)
        ch.query(ddl)
        structure = ddl.split("(\n", 1)[1].split(") ENGINE", 1)[0].replace("'", "''")
        parsed = urlsplit(prefix)
        filesystem = fs.S3FileSystem(anonymous=True, region="us-east-1", scheme="http",
                                     endpoint_override=f"{parsed.hostname}:{parsed.port}")
        bucket = parsed.path.lstrip("/")
        seen: set[int] = set()
        all_rows: list[dict] = []
        previous_manifest_hash = None
        frozen_v1: dict[str, bytes] = {}
        generations: dict[str, dict] = {}
        for revision in (1, 2):
            paths = [producer.HERE / "fixtures" / "v1.jsonl"]
            if revision == 2:
                paths.append(producer.HERE / "fixtures" / "v2.jsonl")
            raw, new_rows = producer.read_source(paths[-1], seen)
            all_rows.extend(new_rows)
            archive = f"{prefix}/search-qrel/archive/{paths[-1].stem}/{producer.digest(raw)}.jsonl"
            producer.put(archive, raw)
            ch.query(f"INSERT INTO {producer.LANDING}.qrel_history SELECT * FROM "
                     f"s3('{archive}', NOSIGN, 'JSONEachRow', '{structure}') "
                     "SETTINGS date_time_input_format='best_effort'")
            generation = f"sea_search_qrel_g{revision}"
            recipe = {"landing_schema": producer.LANDING, "ingest_cutoff": producer.CUTOFFS[revision],
                      "splits": producer.SPLITS}
            build = output / f"build-v{revision}"
            producer.dbt_build(runtime / ".venv/bin/dbt", endpoint, generation, recipe, build)
            dataset = output / f"dataset-v{revision}"
            manifest_path, manifest = producer.export(ch, prefix, generation, revision, paths,
                                                     all_rows, ROOT / "contracts" / "jsonschema",
                                                     build, dataset, previous_manifest_hash)
            expected = producer.file_digest(manifest_path)
            manifest_key = f"{bucket}/search-qrel/{generation}/manifest.json"
            with open_authoritative_pairs(manifest_key, expected_manifest_sha256=expected,
                                          schema_dir=ROOT / "contracts" / "jsonschema",
                                          filesystem=filesystem) as snapshot:
                if snapshot.reader_report["validated_rows"] != manifest["row_count"] or \
                        snapshot.reader_report["status"] != "validated_synthetic_fixture":
                    raise RuntimeError("independent live S3 reader rejected frozen producer rows")
                result_dir = output / f"candidate-v{revision}"
                full, resumed = result_dir / "full-checkpoint.json", result_dir / "resumed-checkpoint.json"
                full_candidate = result_dir / "full-candidate.json"
                resumed_candidate = result_dir / "resumed-candidate.json"
                config = TrainConfig(epochs=3, seed=31)
                finished = train(snapshot, config, full, candidate_path=full_candidate)
                interrupted = train(snapshot, config, resumed, max_steps=1, candidate_path=resumed_candidate)
                if interrupted["status"] != "INTERRUPTED" or resumed_candidate.exists():
                    raise RuntimeError("candidate produced before training completion")
                with open_authoritative_pairs(manifest_key, expected_manifest_sha256=expected,
                                              schema_dir=ROOT / "contracts" / "jsonschema",
                                              filesystem=filesystem) as reverified:
                    if reverified.pair_snapshot_sha256 != snapshot.pair_snapshot_sha256:
                        raise RuntimeError("reopened S3 reader changed pair input before recovery")
                    replayed = train(reverified, config, resumed, resume=True, candidate_path=resumed_candidate)
                candidate = json.loads(full_candidate.read_bytes())
                if finished != replayed or full.read_bytes() != resumed.read_bytes() or \
                        full_candidate.read_bytes() != resumed_candidate.read_bytes() or \
                        finished["status"] != "COMPLETED_CANDIDATE_SYNTHETIC" or \
                        finished["model_quality"] is not None or candidate["active"] is not False or \
                        candidate["pair_snapshot_sha256"] != snapshot.pair_snapshot_sha256:
                    raise RuntimeError("pinned qrel candidate, recovery or default-off contract differs")
                grades = {split: sorted({(pair["positive"]["grade"], pair["negative"]["grade"])
                                         for pair in snapshot.rows[split]}) for split in ("train", "validation", "test")}
                generations[f"v{revision}"] = {
                    "manifest_sha256": expected, "reader_status": snapshot.reader_report["status"],
                    "validated_rows": snapshot.reader_report["validated_rows"],
                    "pair_policy_id": snapshot.pair_policy_id,
                    "pair_snapshot_sha256": snapshot.pair_snapshot_sha256,
                    "eligible_pairs": finished["eligible_pairs"], "graded_pairs": grades,
                    "checkpoint_sha256": producer.file_digest(full),
                    "candidate_sha256": producer.file_digest(full_candidate),
                    "candidate_status": candidate["status"], "model_quality": None}
                # Wrong expected manifest hash must fail against the still-live S3 object.
                try:
                    with open_authoritative_pairs(manifest_key, expected_manifest_sha256="0" * 64,
                                                  schema_dir=ROOT / "contracts" / "jsonschema",
                                                  filesystem=filesystem):
                        pass
                except DatasetError as error:
                    if "manifest hash mismatch" not in str(error):
                        raise
                else:
                    raise RuntimeError("unversioned live S3 search manifest was accepted")
            if revision == 1:
                frozen_v1 = {path.name: path.read_bytes() for path in dataset.iterdir() if path.is_file()}
            else:
                if any((output / "dataset-v1" / name).read_bytes() != body for name, body in frozen_v1.items()):
                    raise RuntimeError("late qrel revision changed v1 frozen Parquet or manifest")
            previous_manifest_hash = expected
        if generations["v1"]["manifest_sha256"] == generations["v2"]["manifest_sha256"] or \
                generations["v1"]["pair_snapshot_sha256"] == generations["v2"]["pair_snapshot_sha256"]:
            raise RuntimeError("new judged revision reused prior training input hash")
        report = {"status": "passed", "evidence_level": "L2_synthetic_fixture_real_CH_dbt_SeaweedFS_reader_T1",
                  "data_kind": "synthetic", "judgment_source": "synthetic_fixture",
                  "activation": "none", "model_quality": None, "business_improvement": None,
                  "trained_representation": "lexical_pairwise_candidate_only",
                  "dense_sparse_multi_vector_training": "not_implemented",
                  "generations": generations}
        (output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
        return report
    finally:
        for process in (s3_proc, ch_proc):
            if process is not None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    result = run(Path(args.runtime).resolve(), Path(args.output).resolve())
    print(json.dumps(result, sort_keys=True, ensure_ascii=False, allow_nan=False))


if __name__ == "__main__":
    main()
