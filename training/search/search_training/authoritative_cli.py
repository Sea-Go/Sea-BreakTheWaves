"""Train a default-off lexical candidate from a pinned H10.b Parquet manifest."""

from __future__ import annotations

import argparse
import json
from urllib.parse import urlsplit

from pyarrow import fs

from .authoritative import open_authoritative_pairs
from .trainer import TrainConfig, train


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("manifest", help="manifest URI, or bucket/key with --s3-endpoint")
    parser.add_argument("--expected-sha256", required=True)
    parser.add_argument("--s3-endpoint", help="isolated anonymous S3 endpoint, e.g. http://127.0.0.1:9000")
    parser.add_argument("--checkpoint", required=True)
    parser.add_argument("--candidate")
    parser.add_argument("--resume", action="store_true")
    parser.add_argument("--max-steps", type=int)
    parser.add_argument("--epochs", type=int, default=5)
    parser.add_argument("--learning-rate", type=float, default=0.05)
    parser.add_argument("--l2", type=float, default=0.0)
    parser.add_argument("--seed", type=int, default=7)
    args = parser.parse_args()
    filesystem = None
    if args.s3_endpoint:
        endpoint = urlsplit(args.s3_endpoint)
        if endpoint.scheme != "http" or endpoint.hostname not in ("127.0.0.1", "localhost") or \
                not endpoint.port or endpoint.username or endpoint.password or endpoint.path not in ("", "/") or \
                endpoint.query or endpoint.fragment:
            parser.error("anonymous S3 fixture endpoint must be loopback HTTP")
        filesystem = fs.S3FileSystem(anonymous=True, region="us-east-1", scheme="http",
                                     endpoint_override=f"{endpoint.hostname}:{endpoint.port}")
    with open_authoritative_pairs(args.manifest, expected_manifest_sha256=args.expected_sha256,
                                  filesystem=filesystem) as snapshot:
        report = train(snapshot, TrainConfig(epochs=args.epochs, learning_rate=args.learning_rate,
                                             l2=args.l2, seed=args.seed), args.checkpoint,
                       resume=args.resume, max_steps=args.max_steps, candidate_path=args.candidate)
    print(json.dumps(report, sort_keys=True, ensure_ascii=False, allow_nan=False))


if __name__ == "__main__":
    main()
