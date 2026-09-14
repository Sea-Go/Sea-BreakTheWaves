import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
from engine import ClickHouse
from fixtures import make_events


class IngestTest(unittest.TestCase):
    def test_tampered_source_is_rejected_before_io(self):
        with tempfile.TemporaryDirectory() as directory:
            event = make_events()[0]
            event['payload'] = '{"title":"tampered"}'
            path = Path(directory) / 'v1.jsonl'
            path.write_text(json.dumps(event) + '\n')
            client = ClickHouse('http://127.0.0.1:12345')
            client.query = Mock()
            with self.assertRaisesRegex(ValueError, 'hash mismatch'):
                client.load('sea_test', path)
            client.query.assert_not_called()

    def test_wrong_batch_file_is_rejected_before_io(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'other.jsonl'
            path.write_text(json.dumps(make_events()[0]) + '\n')
            client = ClickHouse('http://127.0.0.1:12345')
            client.query = Mock()
            with self.assertRaisesRegex(ValueError, 'one fixed batch'):
                client.load('sea_test', path)
            client.query.assert_not_called()
