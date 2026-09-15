"""Frozen-source rejects before any isolated ClickHouse INSERT."""
import copy
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from acceptance import (HERE, SYNTHETIC_GOLDEN_MANIFEST, digest, jcs_fixture,
                        load_new_generation, read_bundle, read_jsonl)


class ExistingGeneration:
    def __init__(self):
        self.queries = []

    def rows(self, query):
        self.queries.append(query)
        return [{"name": "already_present"}]

    def query(self, query, data=None):
        raise AssertionError("existing generation reached ClickHouse INSERT")


class WikiSourceLoaderTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="wiki-source-loader-test-")
        self.root = Path(self.temp.name) / "cutoff-5"
        shutil.copytree(HERE / "fixtures" / "cutoff-5", self.root)

    def tearDown(self):
        self.temp.cleanup()

    def rehash_mutation(self, artifact, change):
        manifest_path = self.root / "manifest.json"
        manifest = read_bundle(HERE / "fixtures" / "cutoff-5",
                               SYNTHETIC_GOLDEN_MANIFEST[5])[0].copy()
        if artifact == "manifest":
            change(manifest)
        else:
            path = self.root / {"prefix": "prefix.jsonl",
                                "catalog": "catalog-facts.jsonl",
                                "judgment": "judgments.jsonl"}[artifact]
            rows = copy.deepcopy(read_jsonl(path.read_bytes()))
            change(rows)
            body = b"".join(jcs_fixture(row) + b"\n" for row in rows)
            path.write_bytes(body)
            manifest[{"prefix": "prefix_jsonl_sha256",
                      "catalog": "catalog_jsonl_sha256",
                      "judgment": "judgment_jsonl_sha256"}[artifact]] = digest(body)
        manifest_body = jcs_fixture(manifest)
        manifest_path.write_bytes(manifest_body)
        return digest(manifest_body)

    def test_existing_generation_rejected_before_insert(self):
        _, bodies, _ = read_bundle(self.root, SYNTHETIC_GOLDEN_MANIFEST[5])
        stub = ExistingGeneration()
        with self.assertRaisesRegex(ValueError, "already exists"):
            load_new_generation(stub, "wiki_landing_existing", bodies)
        self.assertEqual(len(stub.queries), 1)

    def test_manifest_unknown_tenant_and_untrusted_dbt_id_rejected(self):
        expected = self.rehash_mutation("manifest", lambda m: m.update(tenant_id="fiction"))
        with self.assertRaisesRegex(ValueError, "manifest literal"):
            read_bundle(self.root, expected)
        shutil.copytree(HERE / "fixtures" / "cutoff-5", self.root,
                        dirs_exist_ok=True)
        expected = self.rehash_mutation("manifest", lambda m: m.update(
            fact_set_revision_id="x' OR 1=1 --"))
        with self.assertRaisesRegex(ValueError, "untrusted dbt identifier"):
            read_bundle(self.root, expected)

    def test_typed_source_row_unknown_tenant_rejected_even_if_rehashed(self):
        expected = self.rehash_mutation("judgment", lambda rows: rows[0].update(
            tenant_id="not_in_RTW_quality_v1"))
        with self.assertRaisesRegex(ValueError, "row literal contract"):
            read_bundle(self.root, expected)

    def test_missing_position_and_forged_receipt_rejected_even_if_rehashed(self):
        expected = self.rehash_mutation("prefix", lambda rows: rows.pop(2))
        with self.assertRaisesRegex(ValueError, "producer prefix"):
            read_bundle(self.root, expected)
        shutil.copytree(HERE / "fixtures" / "cutoff-5", self.root,
                        dirs_exist_ok=True)

        def corrupt_receipt(rows):
            row = rows[1]
            import json
            receipt = json.loads(row["dc_receipt"])
            receipt["input_hash"] = "0" * 64
            row["dc_receipt"] = json.dumps(receipt, separators=(",", ":"))
            row["dc_receipt_sha256"] = digest(row["dc_receipt"].encode())

        expected = self.rehash_mutation("prefix", corrupt_receipt)
        with self.assertRaisesRegex(ValueError, "accepted receipt"):
            read_bundle(self.root, expected)

    def test_technical_skip_cannot_become_quality_by_rehashing(self):
        expected = self.rehash_mutation("prefix", lambda rows: rows[4].update(
            status="quality_verified"))
        with self.assertRaisesRegex(ValueError, "prefix/count"):
            read_bundle(self.root, expected)


if __name__ == "__main__":
    unittest.main()
