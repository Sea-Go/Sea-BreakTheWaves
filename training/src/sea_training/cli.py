"""Dataset verification CLI. Validation is not model quality acceptance."""

import argparse
import json
from pathlib import Path
import sys

from .dataset import DatasetError, open_dataset


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", help="Fixed manifest file or Arrow filesystem URI")
    parser.add_argument("--schema-dir", type=Path)
    parser.add_argument("--expected-sha256")
    args = parser.parse_args()
    try:
        with open_dataset(args.manifest, schema_dir=args.schema_dir,
                          expected_manifest_sha256=args.expected_sha256) as snapshot:
            print(json.dumps(snapshot.report, ensure_ascii=False, sort_keys=True))
    except DatasetError as exc:
        print(json.dumps({"status": "rejected", "reason": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 1
    return 0
