"""Pinned official inference; construction never downloads model files."""
import hashlib
import json
import os
from pathlib import Path
import threading

from .contracts import ContractError, HIDDEN_SIZE, MAX_BATCH, MAX_LENGTH, REVISION, VOCABULARY_SIZE, response, validate_request


def verify_model(directory, lock_path):
    directory = Path(directory).resolve()
    lock = json.loads(Path(lock_path).read_text())
    if lock["repository"] != "BAAI/bge-m3" or lock["revision"] != REVISION:
        raise ContractError("unexpected pinned model revision")
    for item in lock["files"]:
        path = directory / item["path"]
        if path.stat().st_size != item["size_bytes"]:
            raise ContractError("model size mismatch: " + item["path"])
        digest = hashlib.sha256()
        with path.open("rb") as source:
            while chunk := source.read(4 << 20):
                digest.update(chunk)
        if digest.hexdigest() != item["sha256"]:
            raise ContractError("model checksum mismatch: " + item["path"])
    return directory


class BusyError(RuntimeError):
    pass


class Provider:
    def __init__(self, directory, lock_path):
        directory = verify_model(directory, lock_path)
        # These limits apply to this dedicated serving process, never global Python.
        for key in ["OMP_NUM_THREADS", "MKL_NUM_THREADS", "OPENBLAS_NUM_THREADS"]:
            os.environ[key] = "2"
        os.environ["TOKENIZERS_PARALLELISM"] = "false"
        os.environ["HF_HUB_OFFLINE"] = "1"
        os.environ["TRANSFORMERS_OFFLINE"] = "1"
        os.environ["HF_HOME"] = str(directory / ".runtime-cache")
        import torch
        torch.set_num_threads(2)
        torch.set_num_interop_threads(1)
        from FlagEmbedding import BGEM3FlagModel
        self.model = BGEM3FlagModel(
            str(directory), devices="cpu", use_fp16=False, normalize_embeddings=True,
            batch_size=MAX_BATCH, query_max_length=MAX_LENGTH, passage_max_length=MAX_LENGTH,
            trust_remote_code=False,
        )
        if self.model.model.model.config.hidden_size != HIDDEN_SIZE or self.model.tokenizer.vocab_size != VOCABULARY_SIZE:
            raise ContractError("fixed architecture mismatch")
        self._gate = threading.Lock()

    def represent(self, request):
        kind = validate_request(request)
        if not self._gate.acquire(blocking=False):
            raise BusyError("one bounded CPU inference is already active")
        try:
            texts = [item["text"] for item in request["input"]]
            lengths = [len(ids) for ids in self.model.tokenizer(texts, add_special_tokens=True, truncation=False, padding=False)["input_ids"]]
            if any(size > MAX_LENGTH for size in lengths):
                raise ContractError("input exceeds fixed 128-token profile; chunk before encoding")
            encoded = self.model.encode(
                texts, batch_size=MAX_BATCH, max_length=MAX_LENGTH,
                return_dense=True, return_sparse=True, return_colbert_vecs=True,
            )
            return response(request, kind, encoded, lengths)
        finally:
            self._gate.release()
