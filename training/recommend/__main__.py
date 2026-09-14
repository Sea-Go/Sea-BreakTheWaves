"""Run only a candidate CPU training job against a pinned dataset."""

import argparse
import json

from .trainer import train


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest")
    parser.add_argument("--expected-sha256", required=True)
    parser.add_argument("--checkpoint", required=True)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--epochs", type=int, default=3)
    parser.add_argument("--learning-rate", type=float, default=0.05)
    parser.add_argument("--momentum", type=float, default=0.8)
    parser.add_argument("--max-updates", type=int)
    parser.add_argument("--resume", action="store_true")
    args = parser.parse_args()
    report = train(args.manifest, expected_manifest_sha256=args.expected_sha256,
                   checkpoint=args.checkpoint, seed=args.seed, epochs=args.epochs,
                   learning_rate=args.learning_rate, momentum=args.momentum,
                   max_updates=args.max_updates, resume=args.resume)
    print(json.dumps(report, sort_keys=True, allow_nan=False))


if __name__ == "__main__":
    main()
