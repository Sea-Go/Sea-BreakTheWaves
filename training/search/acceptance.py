"""One live CH/dbt -> SeaweedFS -> pinned-reader -> lexical candidate gate.

This reuses the warehouse/search producer implementation as a read-only
dependency. The training slice owns only its own orchestration and candidate.
"""

from __future__ import annotations

import argparse
from hashlib import sha256
import importlib.util
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlsplit

from pyarrow import fs

from sea_training.dataset import DatasetError

from search_training.authoritative import open_authoritative_pairs
from search_training.trainer import TrainConfig, train


ROOT = Path(__file__).resolve().parents[2]
PRODUCER_PATH = ROOT / "warehouse" / "search" / "acceptance.py"


def _run_logged(command: list[str], *, env: dict[str, str], log_path: Path) -> None:
    with log_path.open("wb") as log:
        subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)


def _three_lane_acceptance(manifest_key: str, expected_qrel_sha256: str,
                           s3_endpoint: str, output: Path, *,
                           model_directory: Path, bge_python: Path) -> dict:
    """Freeze official BGE-M3 values, score each qrel split, and check Go parity.

    The same live SeaweedFS snapshot used by the graded-pair acceptance remains
    online for every independent qrel read. All outputs are default-off evidence.
    """
    if not model_directory.is_dir() or not bge_python.is_file():
        raise RuntimeError("locked BGE-M3 model directory or interpreter is missing")
    py = ROOT / "training" / ".venv" / "bin" / "python"
    if not py.is_file():
        raise RuntimeError("locked training interpreter is missing")
    output.mkdir()
    representation_dir = output / "representations"
    env = os.environ.copy()
    env["PYTHONPATH"] = os.pathsep.join((str(ROOT / "training" / "src"),
                                          str(ROOT / "training" / "search")))
    env["HF_HUB_OFFLINE"] = "1"
    env["TRANSFORMERS_OFFLINE"] = "1"
    _run_logged([
        str(py), str(ROOT / "training" / "search" / "three_lane_encoder.py"),
        "--manifest-uri", manifest_key, "--expected-manifest-sha256", expected_qrel_sha256,
        "--s3-endpoint", s3_endpoint, "--model-directory", str(model_directory),
        "--bge-python", str(bge_python), "--output", str(representation_dir),
    ], env=env, log_path=output / "encoder.log")
    manifests = list((representation_dir / "manifest").glob("*.json"))
    if len(manifests) != 1:
        raise RuntimeError("BGE-M3 encoder did not freeze exactly one manifest")
    representation_manifest = manifests[0]
    representation_sha256 = sha256(representation_manifest.read_bytes()).hexdigest()
    if representation_manifest.stem != representation_sha256:
        raise RuntimeError("BGE-M3 representation manifest content address differs")
    scores: dict[str, dict] = {}
    for split in ("train", "validation", "test"):
        score_path = output / f"scores-{split}.json"
        _run_logged([
            str(py), str(ROOT / "training" / "search" / "three_lane_scoring.py"),
            "--representation-manifest", str(representation_manifest),
            "--representation-sha256", representation_sha256,
            "--qrel-manifest-uri", manifest_key, "--qrel-sha256", expected_qrel_sha256,
            "--s3-endpoint", urlsplit(s3_endpoint).netloc,
            "--split", split, "--top-k", "3",
            "--output", str(score_path),
        ], env=env, log_path=output / f"scoring-{split}.log")
        report = json.loads(score_path.read_bytes())
        def no_formal_metrics(value: dict) -> bool:
            return value.get("status") == "not_evaluable" and all(
                value.get(name, "missing") is None for name in ("recall_at_k", "mrr_at_k", "ndcg_at_k"))

        if report.get("status") != "candidate_default_off" or \
                report.get("data_kind") != "synthetic" or \
                report.get("qrel_reader_status") != "validated_synthetic_fixture" or \
                report.get("candidate_pool_scope") != "all_frozen_chunks_available_at_query_time" or \
                not no_formal_metrics(report.get("evaluation", {})) or \
                report.get("representation_manifest_sha256") != representation_sha256 or \
                report.get("qrel_manifest_sha256") != expected_qrel_sha256 or \
                report.get("split") != split or report.get("top_k") != 3 or \
                not report.get("queries") or any(
                    not query.get("candidate_set") or
                    set(query.get("lanes", {})) != {"dense", "sparse", "token_matrix"} or
                    any(not lane.get("raw_scores") or not lane.get("top_k") or
                        lane.get("judged_coverage", {}).get("candidate_total") != len(lane["raw_scores"]) or
                        lane.get("judged_coverage", {}).get("top_k_total") != len(lane["top_k"]) or
                        not no_formal_metrics(lane.get("evaluation", {}))
                        for lane in query["lanes"].values()) for query in report["queries"]):
            raise RuntimeError("synthetic qrel scoring was misreported as complete or active")
        scores[split] = {"sha256": sha256(score_path.read_bytes()).hexdigest(),
                         "queries": len(report.get("queries", [])),
                         "evaluation_status": "not_evaluable"}
    go_report_path = output / "go-parity.json"
    go_env = env.copy()
    go_env.update({"SEA_BGE_THREE_LANE_MANIFEST": str(representation_manifest),
                   "SEA_BGE_THREE_LANE_SCORES": str(output / "scores-test.json"),
                   "SEA_BGE_THREE_LANE_SCORES_SHA256": scores["test"]["sha256"],
                   "SEA_BGE_THREE_LANE_REPORT": str(go_report_path),
                   "GOFLAGS": "-mod=readonly -p=2", "GOMAXPROCS": "2"})
    _run_logged(["go", "test", "-race", "-count=1", "-run", "^TestFrozenBGEThreeLaneParity$",
                 "./internal/retrieval/three_lane_parity"],
                env=go_env, log_path=output / "go-parity.log")
    go_report = json.loads(go_report_path.read_bytes())
    if go_report.get("status") != "passed" or \
            go_report.get("representation_manifest_sha256") != representation_sha256 or \
            go_report.get("python_scores_sha256") != scores["test"]["sha256"]:
        raise RuntimeError("Go exact retrieval did not match frozen Python lane scores")
    lanes = go_report.get("lanes", {})
    if set(lanes) != {"dense", "sparse", "token_matrix"} or \
            any(not lane.get("topk_equal") or lane.get("compared_scores", 0) < 1 or
                lane.get("max_abs_delta", 1) > 1e-5 for lane in lanes.values()) or \
            lanes["sparse"].get("candidate_semantics") != "positive_intersection_only" or \
            any(not lanes[name].get("python_full_topk_equal") for name in ("dense", "token_matrix")):
        raise RuntimeError("Go lane parity report is incomplete or misstates sparse semantics")
    return {"status": "passed", "data_kind": "synthetic", "activation": "none",
            "model_quality": None, "business_improvement": None,
            "representation_manifest_sha256": representation_sha256,
            "qrel_manifest_sha256": expected_qrel_sha256,
            "splits": scores, "go_parity_report_sha256": sha256(go_report_path.read_bytes()).hexdigest(),
            "go_lanes": lanes, "limits": go_report.get("limits", [])}


def producer_module():
    spec = importlib.util.spec_from_file_location("sea_search_qrel_producer", PRODUCER_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError("search-qrel warehouse producer unavailable")
    producer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(producer)
    return producer


def run(runtime: Path, output: Path, *, model_directory: Path | None = None,
        bge_python: Path | None = None) -> dict:
    if output.exists():
        raise RuntimeError("search training evidence directory already exists")
    if (model_directory is None) != (bge_python is None):
        raise RuntimeError("BGE-M3 model directory and interpreter must be specified together")
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
        if model_directory is not None and bge_python is not None:
            report["three_lane_frozen_inference"] = _three_lane_acceptance(
                manifest_key, expected, f"{parsed.scheme}://{parsed.hostname}:{parsed.port}",
                output / "three-lane", model_directory=model_directory,
                bge_python=bge_python)
            report["evidence_level"] = "L2_same_run_synthetic_qrel_BGE_CPU_Python_Go_exact"
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
    parser.add_argument("--three-lane-model-directory")
    parser.add_argument("--bge-python")
    args = parser.parse_args()
    result = run(Path(args.runtime).resolve(), Path(args.output).resolve(),
                 model_directory=Path(args.three_lane_model_directory).resolve()
                 if args.three_lane_model_directory else None,
                 bge_python=Path(args.bge_python).resolve() if args.bge_python else None)
    print(json.dumps(result, sort_keys=True, ensure_ascii=False, allow_nan=False))


if __name__ == "__main__":
    main()
