"""Export verified H10.b qrel case identities for the local 12-path run.

The strict search-dataset reader owns the Parquet/SHA/split checks. This
export contains observed fixture rows only; it never asserts exhaustive
judgment coverage or invents a production subject identity.
"""

from __future__ import annotations

import argparse
from hashlib import sha256
import json
from pathlib import Path

from sea_training.search_dataset import open_search_dataset


def export(manifest_uri: str, manifest_sha256: str, split: str) -> dict:
    if split != "test":
        raise ValueError("first matrix execution is restricted to the frozen test split")
    with open_search_dataset(manifest_uri, expected_manifest_sha256=manifest_sha256) as snapshot:
        manifest = snapshot.manifest
        if manifest["data_kind"] != "synthetic" or manifest["revision"] != 2:
            raise ValueError("first matrix execution requires H10.b synthetic revision 2")
        cases: dict[str, dict] = {}
        for batch in snapshot.iter_batches(split):
            for row in batch.to_pylist():
                case = cases.setdefault(row["query_id"], {
                    "query_id": row["query_id"], "query_family_id": row["query_family_id"],
                    "near_duplicate_cluster_id": row["near_duplicate_cluster_id"],
                    "query_text": row["query_text"], "query_time": row["query_time"].isoformat(),
                    "judgments": [],
                })
                if (case["query_family_id"] != row["query_family_id"] or
                        case["near_duplicate_cluster_id"] != row["near_duplicate_cluster_id"] or
                        case["query_text"] != row["query_text"] or
                        case["query_time"] != row["query_time"].isoformat()):
                    raise ValueError("query changed within validated qrel snapshot")
                key = json.dumps([row["document_id"], row["document_revision"], row["chunk_id"]],
                                 ensure_ascii=False, separators=(",", ":"))
                case["judgments"].append({"chunk_key": key, "grade": row["relevance_grade"]})
        if not cases:
            raise ValueError("frozen test split is empty")
        for case in cases.values():
            case["judgments"].sort(key=lambda item: item["chunk_key"])
        return {
            "schema_version": "sea.search.matrix-source.v1",
            "dataset_manifest_sha256": snapshot.manifest_sha256,
            "data_kind": manifest["data_kind"],
            "reader_status": snapshot.report["status"],
            "split": split,
            "split_windows": manifest["splits"],
            "evaluation_cutoff": manifest["source"]["ingest_cutoff"],
            "judgment_scope_complete": False,
            "cases": [cases[key] for key in sorted(cases)],
        }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--qrel-manifest-uri", required=True)
    parser.add_argument("--qrel-sha256", required=True)
    parser.add_argument("--split", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists() or not args.output.parent.is_dir():
        parser.error("output must be a new file in an existing directory")
    payload = json.dumps(export(args.qrel_manifest_uri, args.qrel_sha256, args.split),
                         sort_keys=True, ensure_ascii=False, separators=(",", ":"),
                         allow_nan=False).encode("utf-8")
    with args.output.open("xb") as target:
        target.write(payload)
    print(json.dumps({"path": str(args.output), "sha256": sha256(payload).hexdigest()}, sort_keys=True))


if __name__ == "__main__":
    main()
