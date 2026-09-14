"""The fixed provider profile; generation math is owned by official BGE-M3."""
from copy import deepcopy
import math
import re
import unicodedata

REVISION = "5617a9f61b028005a4858fdac845db406aefb181"
MODEL_ID = "BAAI/bge-m3@5617a9f61b02"
TOKENIZER_ID = "bge-m3-tokenizer@5617a9f61b02"
VOCABULARY_ID = "bge-m3-xlmr-vocabulary@5617a9f61b02"
MAX_LENGTH = 128
MAX_BATCH = 2
HIDDEN_SIZE = 1024
VOCABULARY_SIZE = 250002


def profiles():
    result = {}
    for kind in ["dense", "sparse", "token_matrix"]:
        contract = {
            "id": f"bge-m3-{kind.replace('_', '-')}@5617a9f61b02-v1",
            "kind": kind,
            "dimensions": VOCABULARY_SIZE if kind == "sparse" else HIDDEN_SIZE,
            "tokenizer_id": TOKENIZER_ID,
            "normalization": "none" if kind == "sparse" else "l2",
            "metric": "maxsim" if kind == "token_matrix" else "dot",
            "aggregation": {"dense": "none", "sparse": "max", "token_matrix": "mean_maxsim"}[kind],
        }
        if kind == "sparse":
            contract.update(vocabulary_id=VOCABULARY_ID, max_nonzero=MAX_LENGTH - 2)
        elif kind == "token_matrix":
            contract["max_tokens"] = MAX_LENGTH - 1
        result[kind] = {
            "model": MODEL_ID,
            "output_contract": f"sea.representation.{kind}.v1",
            "representation_space": f"bge-m3-{kind.replace('_', '-')}@5617a9f61b02-fp32-v1",
            "representation_contract": contract,
            "max_input_tokens": MAX_LENGTH,
            "max_batch": MAX_BATCH,
        }
    return result


PROFILES = profiles()


class ContractError(ValueError):
    pass


def identifier(value):
    return isinstance(value, str) and 0 < len(value) <= 256 and value.strip() == value and not any(
        unicodedata.category(c) == "Cc" for c in value
    )


def validate_request(request):
    keys = {"model", "configuration_id", "output_contract", "representation_contract_id", "representation_space", "role", "input"}
    if not isinstance(request, dict) or set(request) != keys:
        raise ContractError("exact representation request fields required")
    if request["model"] != MODEL_ID:
        raise ContractError("model does not match fixed model revision")
    config = request["configuration_id"]
    if not isinstance(config, str) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", config) or config == "00000000-0000-0000-0000-000000000000":
        raise ContractError("canonical nonzero configuration UUID required")
    kind = next((k for k, p in PROFILES.items() if p["output_contract"] == request["output_contract"]), None)
    if kind is None:
        raise ContractError("unknown output contract")
    profile = PROFILES[kind]
    if request["representation_space"] != profile["representation_space"] or request["representation_contract_id"] != profile["representation_contract"]["id"]:
        raise ContractError("fixed representation contract or space mismatch")
    if request["role"] not in ("query", "document"):
        raise ContractError("role must be query or document")
    inputs = request["input"]
    if not isinstance(inputs, list) or not 1 <= len(inputs) <= MAX_BATCH:
        raise ContractError(f"batch must contain 1..{MAX_BATCH} items")
    seen = set()
    for item in inputs:
        if not isinstance(item, dict) or set(item) != {"id", "text"} or not identifier(item["id"]) or item["id"] in seen:
            raise ContractError("unique input IDs and exact input fields required")
        if not isinstance(item["text"], str) or not item["text"].strip() or len(item["text"].encode("utf-8")) > 16384:
            raise ContractError("nonempty bounded UTF-8 text required")
        seen.add(item["id"])
    return kind


def validate_values(values, *, normalized=False):
    if not values or any(not math.isfinite(x) for x in values):
        raise ContractError("nonempty finite representation required")
    square = sum(x * x for x in values)
    if not math.isfinite(square) or square == 0:
        raise ContractError("nonzero finite norm required")
    if normalized and abs(square - 1) > 1e-4:
        raise ContractError("official L2 representation norm mismatch")


def response(request, kind, encoded, token_counts):
    profile = PROFILES[kind]
    output = {key: deepcopy(request[key]) for key in ["model", "configuration_id", "output_contract", "representation_contract_id", "representation_space", "role"]}
    output["tokenizer_id"] = TOKENIZER_ID
    if kind == "sparse":
        output["vocabulary_id"] = VOCABULARY_ID
    output["data"] = []
    output["usage"] = {"prompt_tokens": sum(token_counts), "total_tokens": sum(token_counts)}
    for index, item in enumerate(request["input"]):
        result = {"id": item["id"]}
        if kind == "dense":
            vector = encoded["dense_vecs"][index].tolist()
            if len(vector) != HIDDEN_SIZE:
                raise ContractError("dense dimension mismatch")
            validate_values(vector, normalized=True)
            result["dense"] = {"values": vector}
        elif kind == "sparse":
            # Official lexical_weights already removes special tokens and uses
            # max over repeated occurrences. Never sum, normalize, or run BM25.
            weights = encoded["lexical_weights"][index]
            pairs = sorted((int(token), float(weight)) for token, weight in weights.items())
            ids = [token for token, _ in pairs]
            values = [weight for _, weight in pairs]
            if len(ids) != len(set(ids)) or any(token < 0 or token >= VOCABULARY_SIZE for token in ids) or len(ids) > profile["representation_contract"]["max_nonzero"] or any(weight <= 0 for weight in values):
                raise ContractError("invalid official sparse weights")
            validate_values(values)
            result["sparse"] = {"indices": ids, "weights": values}
        else:
            matrix = encoded["colbert_vecs"][index].tolist()
            # Official encode removes CLS and padding, retains EOS. Every row
            # here is a real active token; no synthetic padding rows are added.
            if len(matrix) != token_counts[index] - 1 or not 1 <= len(matrix) <= MAX_LENGTH - 1:
                raise ContractError("official token matrix mask/length mismatch")
            for row in matrix:
                if len(row) != HIDDEN_SIZE:
                    raise ContractError("token matrix dimension mismatch")
                validate_values(row, normalized=True)
            result["token_matrix"] = {"shape": [len(matrix), HIDDEN_SIZE], "values": matrix, "mask": [True] * len(matrix)}
        output["data"].append(result)
    return output
