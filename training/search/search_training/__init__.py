"""Experimental, fixture-only search text-ranking trainer."""

from .dataset import DatasetError, open_snapshot
from .trainer import TrainError, TrainConfig, train

__all__ = ["DatasetError", "TrainError", "TrainConfig", "open_snapshot", "train"]
