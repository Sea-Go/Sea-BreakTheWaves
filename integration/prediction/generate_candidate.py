#!/usr/bin/env python3
"""Build one deterministic synthetic candidate through the real BTW exporter."""

from __future__ import annotations

import argparse
from pathlib import Path
import json

from export.candidate import export_candidate
from export.contract import canonical, digest
from export.serving import PREPROCESSING, load_candidate
from recommend.tests.test_trainer import fixture
from recommend.trainer import train


CONFIG = {"seed": 7, "epochs": 2, "learning_rate": 0.05, "momentum": 0.8}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("output")
    args = parser.parse_args()
    root = Path(args.output).resolve()
    root.mkdir(parents=True, exist_ok=False)
    manifest, manifest_sha = fixture(root / "dataset")
    checkpoint = root / "checkpoint.json"
    training = train(manifest, expected_manifest_sha256=manifest_sha,
                     checkpoint=checkpoint, **CONFIG)
    preprocessing = root / "preprocessing.identity.json"
    preprocessing.write_bytes(canonical(PREPROCESSING))
    serving = root / "serving"
    exported = export_candidate(manifest, expected_manifest_sha256=manifest_sha,
                                checkpoint_path=checkpoint, expected_config=CONFIG,
                                preprocessing_path=preprocessing, output_dir=serving)
    loaded, _, _ = load_candidate(serving)
    if loaded != exported or training["status"] != "candidate":
        raise RuntimeError("real trainer/exporter candidate did not round trip")
    report = {
        "schema": "sea.btw.synthetic-prediction-export.v1",
        "source_data_kind": "synthetic",
        "business_activation": "none",
        "quality_claim": "not_evaluated",
        "dataset_manifest_sha256": manifest_sha,
        "checkpoint_sha256": digest(checkpoint.read_bytes()),
        "candidate_manifest_sha256": digest((serving / "manifest.json").read_bytes()),
        "pair_id": exported["pair_id"],
        "space_id": exported["space_id"],
        "serving_directory": str(serving),
    }
    (root / "generation-report.json").write_bytes(canonical(report) + b"\n")
    print(json.dumps(report, sort_keys=True))


if __name__ == "__main__":
    main()
