"""Derive task-owned contiguous synthetic sources without editing WS07 B/C."""
import hashlib
import json
from pathlib import Path


PROJECT = Path(__file__).resolve().parents[1]
DESTINATION = Path(__file__).resolve().parent / "fixtures"


def derive() -> None:
    DESTINATION.mkdir(exist_ok=True)
    for original_batch in ("v1", "v2"):
        original = [json.loads(line) for line in
                    (PROJECT / "fixtures" / f"{original_batch}.jsonl").read_text().splitlines()]
        corrected = []
        for source in original:
            if source["source_partition"] != "fixture-0" or source["source_sequence"] == 2:
                raise ValueError("upstream fixture source shape changed; review the ADS correction")
            event = dict(source, batch_id=f"ads_{original_batch}")
            if event["source_sequence"] > 2:
                event["source_sequence"] -= 1
            body = {k: v for k, v in event.items() if k not in ("batch_id", "payload_hash")}
            event["payload_hash"] = hashlib.sha256(json.dumps(body, sort_keys=True).encode()).hexdigest()
            corrected.append(event)
        (DESTINATION / f"ads_{original_batch}.jsonl").write_text(
            "".join(json.dumps(event, sort_keys=True) + "\n" for event in corrected))
    for batches in (("ads_v1",), ("ads_v1", "ads_v2")):
        events = [json.loads(line) for batch in batches for line in
                  (DESTINATION / f"{batch}.jsonl").read_text().splitlines()]
        positions = {event["source_sequence"] for event in events}
        if positions != set(range(1, max(positions) + 1)):
            raise ValueError("ADS synthetic fixture is not a complete source prefix")


if __name__ == "__main__":
    derive()
