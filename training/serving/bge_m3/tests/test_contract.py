import copy
import json
import math
import pytest

from sea_bge_m3.contracts import ContractError, MODEL_ID, PROFILES, validate_request, validate_values
from sea_bge_m3.server import decode_request


def request(kind="dense", texts=None):
    profile = PROFILES[kind]
    return {
        "model": MODEL_ID,
        "configuration_id": "00000000-0000-4000-8000-000000000001",
        "output_contract": profile["output_contract"],
        "representation_contract_id": profile["representation_contract"]["id"],
        "representation_space": profile["representation_space"],
        "role": "query",
        "input": [{"id": str(i), "text": text} for i, text in enumerate(texts or ["鲸鱼是什么？"])],
    }


@pytest.mark.parametrize("kind", PROFILES)
def test_explicit_profiles(kind):
    q = request(kind)
    assert validate_request(q) == kind
    bad = copy.deepcopy(q)
    bad["representation_space"] += "-other"
    with pytest.raises(ContractError):
        validate_request(bad)
    bad = copy.deepcopy(q)
    bad["representation_contract_id"] += "-other"
    with pytest.raises(ContractError):
        validate_request(bad)


@pytest.mark.parametrize("change", [
    {"model": "unfixed"}, {"role": "other"}, {"configuration_id": "00000000-0000-0000-0000-000000000000"},
    {"configuration_id": "Not-a-uuid"}, {"input": []}, {"input": [{"id": "a", "text": " "}]},
    {"input": [{"id": "a", "text": "yes"}] * 2}, {"extra": True},
])
def test_request_rejections(change):
    q = request()
    q.update(change)
    with pytest.raises(ContractError):
        validate_request(q)


def test_numeric_and_duplicate_json_errors():
    for raw in [b'{"a":1,"a":2}', b'{"a":NaN}', b'{"a":1}{}', b'not json']:
        with pytest.raises(ContractError):
            decode_request(raw)
    for values in [[], [0.0], [math.nan], [math.inf]]:
        with pytest.raises(ContractError):
            validate_values(values)
    with pytest.raises(ContractError):
        validate_values([0.5], normalized=True)


def test_profile_json_is_generated_from_constants():
    from pathlib import Path
    assert json.loads((Path(__file__).parents[1] / "profiles.json").read_text()) == PROFILES
    assert PROFILES["token_matrix"]["representation_contract"]["aggregation"] == "mean_maxsim"
