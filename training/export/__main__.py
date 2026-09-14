"""Explicit candidate export; no registry, active pointer, or network calls."""

import argparse
import json
import logging
from pathlib import Path
import sys

from .candidate import export_candidate
from .contract import ExportError, read_json


class _JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        payload = {"service": "sea-breakthewaves-training",
                   "component": "training.export", "source": "offline_job",
                   "event": record.event, "level": record.levelname.lower(),
                   "status": record.status,
                   "dataset_manifest_sha256": record.dataset_manifest_sha256}
        if hasattr(record, "reason"):
            payload["reason"] = record.reason
        return json.dumps(payload, sort_keys=True)


def _logger() -> logging.Logger:
    logger = logging.getLogger("sea.training.export")
    logger.setLevel(logging.INFO)
    logger.propagate = False
    if not logger.handlers:
        handler = logging.StreamHandler(sys.stderr)
        handler.setFormatter(_JsonFormatter())
        logger.addHandler(handler)
    return logger


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("manifest")
    parser.add_argument("--expected-manifest-sha256", required=True)
    parser.add_argument("--checkpoint", required=True)
    parser.add_argument("--expected-config", required=True, help="JSON file of original training options")
    parser.add_argument("--preprocessing", required=True, help="exact identity transform JSON")
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    logger = _logger()
    log_fields = {"dataset_manifest_sha256": args.expected_manifest_sha256}
    logger.info("export started", extra={**log_fields, "event": "training.export.started",
                                         "status": "running"})
    try:
        result = export_candidate(args.manifest,
                                  expected_manifest_sha256=args.expected_manifest_sha256,
                                  checkpoint_path=args.checkpoint,
                                  expected_config=read_json(Path(args.expected_config).read_bytes()),
                                  preprocessing_path=args.preprocessing,
                                  output_dir=args.output_dir)
    except (ExportError, OSError, ValueError) as exc:
        logger.error("export rejected", extra={**log_fields, "event": "training.export.rejected",
                                               "status": "rejected", "reason": str(exc)})
        raise SystemExit(2) from None
    logger.info("export completed", extra={**log_fields, "event": "training.export.completed",
                                           "status": "candidate"})
    # stdout is the result contract, while all operational events use structured stderr logging.
    sys.stdout.write(json.dumps(result, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
