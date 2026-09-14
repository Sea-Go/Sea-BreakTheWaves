"""Run the fixture-only CPU search baseline against a pinned local manifest."""

from __future__ import annotations

import argparse
import json

from .dataset import open_snapshot
from .trainer import TrainConfig, train


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("manifest")
    parser.add_argument("--expected-sha256", required=True)
    parser.add_argument("--checkpoint", required=True)
    parser.add_argument("--candidate")
    parser.add_argument("--resume", action="store_true")
    parser.add_argument("--max-steps", type=int)
    parser.add_argument("--epochs", type=int, default=5)
    parser.add_argument("--learning-rate", type=float, default=0.05)
    parser.add_argument("--l2", type=float, default=0.0)
    parser.add_argument("--seed", type=int, default=7)
    args = parser.parse_args()
    snapshot = open_snapshot(args.manifest, expected_manifest_sha256=args.expected_sha256)
    config = TrainConfig(epochs=args.epochs, learning_rate=args.learning_rate, l2=args.l2, seed=args.seed)
    report = train(snapshot, config, args.checkpoint, resume=args.resume,
                   max_steps=args.max_steps, candidate_path=args.candidate)
    print(json.dumps(report, sort_keys=True, ensure_ascii=False, allow_nan=False))


if __name__ == "__main__":
    main()
