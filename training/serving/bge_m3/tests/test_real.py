"""Opt-in actual official weights; there is no fake encoder in these tests."""
from collections import Counter
import json
import os
from pathlib import Path
import threading
import urllib.request
import urllib.error

import pytest

from sea_bge_m3.contracts import MAX_LENGTH, MODEL_ID, PROFILES, ContractError
from sea_bge_m3.model import Provider
from sea_bge_m3.server import make_server
from test_contract import request

TEXTS = ["whale whale whale 是什么？", "鲸鱼是生活在海洋中的哺乳动物。"]


@pytest.fixture(scope="module")
def actual():
    directory = os.environ.get("SEA_BGE_MODEL_DIRECTORY")
    if not directory:
        pytest.skip("SEA_BGE_MODEL_DIRECTORY required for actual BGE-M3 weights")
    provider = Provider(directory, Path(__file__).parents[1] / "model.lock.json")
    import torch
    assert torch.get_num_threads() == 2
    assert next(provider.model.model.parameters()).dtype == torch.float32
    tokens = provider.model.tokenizer(TEXTS, padding=True, truncation=False, return_tensors="pt")
    provider.model.model.eval()
    with torch.inference_mode():
        raw = provider.model.model(tokens, return_dense=True, return_sparse=True, return_colbert_vecs=True, return_sparse_embedding=False)
    server = make_server(provider)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield provider, tokens, raw, f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def post(endpoint, q):
    req = urllib.request.Request(endpoint + "/v1/representations", data=json.dumps(q).encode(), headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=90) as result:
        return json.load(result)


def test_real_dense_sparse_matrix_and_math(actual):
    import numpy as np
    provider, tokens, raw, endpoint = actual
    output = {kind: post(endpoint, request(kind, TEXTS)) for kind in PROFILES}
    lengths = tokens["attention_mask"].sum(dim=1).tolist()
    unused = {provider.model.tokenizer.cls_token_id, provider.model.tokenizer.eos_token_id, provider.model.tokenizer.pad_token_id, provider.model.tokenizer.unk_token_id}
    repeated_verified = False
    matrices = []
    dense = []
    for i in range(2):
        for kind in PROFILES:
            assert output[kind]["configuration_id"] == request(kind)["configuration_id"]
            assert output[kind]["role"] == "query"
            assert output[kind]["model"] == MODEL_ID
            assert output[kind]["usage"] == {"prompt_tokens": sum(lengths), "total_tokens": sum(lengths)}
        vector = np.array(output["dense"]["data"][i]["dense"]["values"])
        assert vector.shape == (1024,)
        np.testing.assert_allclose(vector, raw["dense_vecs"][i].numpy(), rtol=1e-5, atol=1e-6)
        dense.append(vector)
        sparse = output["sparse"]["data"][i]["sparse"]
        expected = {}
        counts = Counter()
        sums = {}
        for weight, token in zip(raw["sparse_vecs"][i].numpy().reshape(-1), tokens["input_ids"][i].tolist()):
            if token not in unused and weight > 0:
                counts[token] += 1
                sums[token] = sums.get(token, 0.0) + float(weight)
                expected[token] = max(expected.get(token, 0.0), float(weight))
        assert sparse["indices"] == sorted(expected)
        np.testing.assert_allclose(sparse["weights"], [expected[x] for x in sorted(expected)], rtol=1e-5, atol=1e-6)
        repeated_verified |= any(count > 1 and sums[token] > expected[token] + 1e-6 for token, count in counts.items())
        matrix = output["token_matrix"]["data"][i]["token_matrix"]
        assert matrix["shape"] == [lengths[i] - 1, 1024]
        assert matrix["mask"] == [True] * (lengths[i] - 1)
        values = np.array(matrix["values"], dtype=np.float32)
        np.testing.assert_allclose(values, raw["colbert_vecs"][i, :lengths[i] - 1].numpy(), rtol=1e-5, atol=1e-6)
        np.testing.assert_allclose(np.linalg.norm(values, axis=1), 1, atol=1e-5)
        matrices.append(values)
    assert repeated_verified, "actual repeated token weights must demonstrate max differs from sum"
    expected = np.max(matrices[0] @ matrices[1].T, axis=1)
    official = float(provider.model.colbert_score(matrices[0], matrices[1]))
    assert official == pytest.approx(float(expected.mean()), abs=1e-6)
    assert official != pytest.approx(float(expected.sum()), abs=1e-3)
    first = dict(zip(output["sparse"]["data"][0]["sparse"]["indices"], output["sparse"]["data"][0]["sparse"]["weights"]))
    second = dict(zip(output["sparse"]["data"][1]["sparse"]["indices"], output["sparse"]["data"][1]["sparse"]["weights"]))
    lexical = sum(weight * second.get(token, 0) for token, weight in first.items())
    assert lexical == pytest.approx(float(provider.model.compute_lexical_matching_score(first, second)), abs=1e-6)
    print(json.dumps({"real_model": MODEL_ID, "dense_dot": float(dense[0] @ dense[1]), "sparse_dot": lexical, "mean_maxsim": official, "query_tokens": lengths[0] - 1, "document_tokens": lengths[1] - 1, "repeated_token_max_verified": repeated_verified}))


def test_actual_preflight_limits_without_truncation(actual):
    provider, _, _, endpoint = actual
    long = request(texts=["whale " * (MAX_LENGTH + 1)])
    with pytest.raises(ContractError, match="128-token"):
        provider.represent(long)
    with provider._gate:
        with pytest.raises(urllib.error.HTTPError) as caught:
            post(endpoint, request())
        assert caught.value.code == 429
    with urllib.request.urlopen(endpoint + "/v1/models") as result:
        assert json.load(result)["data"][0]["id"] == MODEL_ID
