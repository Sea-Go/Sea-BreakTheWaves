import unittest
from pathlib import Path

from freeze import canonical, sha, validate_rows, validate_source, validate_synthetic_source_files


def recipe(kind="synthetic", coverage="synthetic_fixture_complete", receipt=""):
    return {
        "ads_definition_revision": "ads-exposure-r1",
        "ads_source_kind": kind,
        "ads_source_coverage_state": coverage,
        "ads_coverage_receipt_sha256": receipt,
        "ads_evaluation_cutoff": "2026-09-01T13:20:00Z",
        "observed_until": "2026-09-01T13:35:00Z",
        "event_watermark": "2026-09-01T13:20:00Z",
        "source_batches": ["v1"],
    }


class FreezeContractTest(unittest.TestCase):
    def test_only_contiguous_fixed_source_can_claim_fixture_coverage(self):
        warehouse = Path(__file__).resolve().parents[2]
        corrected = warehouse / "ads/fixtures/ads_v1.jsonl"
        later = warehouse / "ads/fixtures/ads_v2.jsonl"
        self.assertEqual(len(validate_synthetic_source_files([corrected])), 35)
        self.assertEqual(len(validate_synthetic_source_files([corrected, later])), 37)
        with self.assertRaisesRegex(ValueError, "missing position"):
            validate_synthetic_source_files([warehouse / "fixtures/v1.jsonl"])

    def test_observed_requires_approved_coverage_receipt(self):
        batch = [{"batch_id": "v1", "sha256": "a" * 64}]
        validate_source(recipe(), batch)
        with self.assertRaisesRegex(ValueError, "authoritative H09"):
            validate_source(recipe("observed", "unverified"), batch)
        receipt = sha(canonical({"source": "H09 contract fixture", "coverage": "complete"}))
        observed = recipe("observed", "verified_complete", receipt)
        observed["ads_source_watermarks"] = [{"partition": "source-1", "contiguous_sequence": 3,
                                               "complete": True, "event_time": "2026-09-01T13:20:00Z"}]
        with self.assertRaisesRegex(ValueError, "authoritative H09"):
            validate_source(observed, batch)
        validate_source(observed, batch, lambda got, _recipe, _batches: got == receipt)
        with self.assertRaisesRegex(ValueError, "authoritative H09"):
            validate_source(observed, batch, lambda *_: False)
        with self.assertRaisesRegex(ValueError, "unknown ADS"):
            validate_source(recipe("unknown", "verified_complete", receipt), batch)

    def test_cutoff_and_batches_are_part_of_source_authority(self):
        batch = [{"batch_id": "v1", "sha256": "a" * 64}]
        future = recipe()
        future["ads_evaluation_cutoff"] = "2026-09-01T14:00:00Z"
        with self.assertRaisesRegex(ValueError, "cutoff"):
            validate_source(future, batch)
        with self.assertRaisesRegex(ValueError, "batch list"):
            validate_source(recipe(), [{"batch_id": "v2", "sha256": "a" * 64}])

    def test_non_exposure_and_pending_never_gain_a_zero_label(self):
        positive = {
            "authority_id": "rtw", "tenant_id": "platform", "subject_id": "1",
            "impression_id": "i1", "served_event_id": "c1", "window_mature": True,
            "impression_source_partition": "p1", "served_source_partition": "p1",
            "evaluation_state": "mature_positive", "mature_label": 1,
            "source_kind": "synthetic", "source_coverage_state": "synthetic_fixture_complete",
            "coverage_receipt_sha256": "", "cohort": "unassigned",
            "assignment_state": "missing_authoritative_assignment",
        }
        pending = dict(positive, impression_id="i2", evaluation_state="pending", mature_label=None)
        rollup = {
            "visible_impressions": 2, "served_visible_impressions": 2,
            "mature_positive_impressions": 1, "mature_negative_impressions": 0,
            "mature_evaluable_denominator": 1,
            "experiment_effect": None, "experiment_state": "not_evaluable_assignment_missing",
        }
        validate_rows([positive, pending], [rollup], recipe())
        with self.assertRaisesRegex(ValueError, "became a negative"):
            validate_rows([positive, dict(pending, mature_label=0)], [rollup], recipe())
        with self.assertRaisesRegex(ValueError, "rollup differs"):
            validate_rows([positive, pending], [dict(rollup, mature_evaluable_denominator=2)], recipe())
        with self.assertRaisesRegex(ValueError, "experiment effect"):
            validate_rows([positive, pending], [dict(rollup, experiment_effect=0.1)], recipe())


if __name__ == "__main__":
    unittest.main()
