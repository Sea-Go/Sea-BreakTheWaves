import multiprocessing
import tempfile
import unittest
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
from publication import LocalPublication, PublicationError


def claim_worker(root, attempt, output):
    output.put((attempt, LocalPublication(root).claim('g1', attempt, 'a' * 64)))


class PublicationTest(unittest.TestCase):
    def test_fencing_and_immutable_publication(self):
        with tempfile.TemporaryDirectory() as root:
            store = LocalPublication(root)
            old = store.claim('g1', 'a1', 'a' * 64)
            new = store.claim('g1', 'a2', 'a' * 64)
            with self.assertRaises(PublicationError):
                store.publish('g1', 'a1', old, Path(root) / 'old.json')
            (Path(root) / 'new.json').write_text('{}')
            store.publish('g1', 'a2', new, Path(root) / 'new.json')
            with self.assertRaises(PublicationError):
                store.claim('g1', 'a3', 'a' * 64)
            self.assertTrue(store.state('g1')['manifest'].endswith('new.json'))

    def test_cancel_wins_even_after_sql_success(self):
        with tempfile.TemporaryDirectory() as root:
            store = LocalPublication(root)
            epoch = store.claim('g1', 'a1', 'a' * 64)
            store.cancel('g1')
            with self.assertRaises(PublicationError):
                store.publish('g1', 'a1', epoch, Path(root) / 'valid.json')
            with self.assertRaises(PublicationError):
                store.claim('g1', 'a2', 'a' * 64)
            self.assertIsNone(store.state('g1')['manifest'])

    def test_concurrent_claims_have_one_current_epoch(self):
        with tempfile.TemporaryDirectory() as root:
            ctx = multiprocessing.get_context('spawn')
            output = ctx.Queue()
            workers = [ctx.Process(target=claim_worker, args=(root, f'a{index}', output)) for index in range(4)]
            for worker in workers:
                worker.start()
            for worker in workers:
                worker.join(10)
                self.assertEqual(worker.exitcode, 0)
            claims = [output.get(timeout=2) for _ in workers]
            self.assertEqual(sorted(epoch for _, epoch in claims), [1, 2, 3, 4])
            store = LocalPublication(root)
            winner = store.state('g1')['attempt']
            for attempt, epoch in claims:
                if attempt != winner:
                    with self.assertRaises(PublicationError):
                        store.publish('g1', attempt, epoch, Path(root) / 'stale.json')
            (Path(root) / 'winner.json').write_text('{}')
            store.publish('g1', winner, 4, Path(root) / 'winner.json')

    def test_changed_inputs_require_new_generation(self):
        with tempfile.TemporaryDirectory() as root:
            store = LocalPublication(root)
            store.claim('g1', 'a1', 'a' * 64)
            with self.assertRaises(PublicationError):
                store.claim('g1', 'a2', 'b' * 64)
            self.assertEqual(store.state('g1')['epoch'], 1)
