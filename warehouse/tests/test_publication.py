import copy
import datetime
import json
import multiprocessing
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pyarrow as pa
import pyarrow.parquet as pq

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
from publication import LocalPublication, PublicationError
from receipt import PROJECT, canonical_digest, make_binding, sha256, write_build_receipt
from verify import parameters

CONTRACTS = Path(os.environ.get('WAREHOUSE_CONTRACTS', PROJECT.parent / 'contracts/jsonschema'))


def binding(namespace='ns_a1'):
    return make_binding(parameters(1), namespace, CONTRACTS, [PROJECT / 'fixtures/v1.jsonl'])


def claim_worker(root, attempt, output):
    output.put((attempt, LocalPublication(root).claim('g1', attempt, binding('ns_' + attempt))))


def fixture_export(root, execution):
    """Minimal unit artifact fixtures; real SQL is separately exercised by verify.py."""
    root = Path(root)
    root.mkdir(parents=True)
    build = root / 'build'
    (build / 'target').mkdir(parents=True)
    spec = execution['binding']
    nodes = {}
    for kind, paths in [('model', (PROJECT / 'models').rglob('*.sql')), ('test', (PROJECT / 'tests').glob('*.sql'))]:
        for path in paths:
            nodes[f'{kind}.sea_warehouse.{path.stem}'] = dict(resource_type=kind, config={'enabled': True},
                                                          schema=spec['output_namespace'], raw_code=path.read_text())
    dbt_manifest = {'metadata': {'invocation_id': 'unit-fixture'}, 'nodes': nodes}
    dbt_results = {'metadata': {'invocation_id': 'unit-fixture'}, 'args': {'vars': spec['recipe']},
                   'results': [{'unique_id': key, 'status': 'success' if node['resource_type'] == 'model' else 'pass'}
                               for key,node in nodes.items()]}
    (build / 'target/manifest.json').write_text(json.dumps(dbt_manifest))
    (build / 'target/run_results.json').write_text(json.dumps(dbt_results))
    receipt = write_build_receipt(build, execution)
    columns = json.loads(Path(spec['columns_path']).read_text())
    types = {'string': pa.string(), 'uint8': pa.uint8(), 'uint32': pa.uint32(), 'float64': pa.float64(),
             'timestamp[us, tz=UTC]': pa.timestamp('us', tz='UTC')}
    schema = pa.schema([pa.field(c['name'], types[c['arrow_type']], nullable=c['nullable']) for c in columns])
    now = datetime.datetime(2026, 9, 1, 9, tzinfo=datetime.timezone.utc)
    row = dict(sample_id='unit-fixture', request_id='r1', impression_id='i1', authority_id='rtw', tenant_id='tenant',
               subject_id='user', item_id='article-1', content_revision='v1', request_time=now, impression_time=now,
               feature_cutoff=now, feature_snapshot_ref='f1', feature_contract_id='recommendation-engagement-fixture-v1',
               feature_available_at=now, user_interest=.5, item_quality=.5, label=1, label_state='POSITIVE', label_revision=1,
               label_observation_end=now + datetime.timedelta(minutes=30), sampling_probability=1., split='train')
    files=[]
    for split in spec['recipe']['splits']:
        path = root / (split + '.parquet')
        rows = [row] if split == 'train' else []
        pq.write_table(pa.Table.from_pylist(rows, schema=schema), path)
        files.append(dict(path=path.name, split=split, rows=len(rows), size_bytes=path.stat().st_size, sha256=sha256(path)))
    events=[json.loads(line) for line in Path(spec['batches'][0]['path']).read_text().splitlines()]
    manifest=dict(schema_version='sea.training-dataset.v2', row_contract='sea.recommend-engagement.v1',
                  dataset_id='unit-fixture', revision=1, parent_revision=None, domain='recommend', data_kind='synthetic',
                  created_at=now.isoformat(), feature_contract_id=spec['recipe']['feature_contract_id'], columns=columns,
                  source=dict(warehouse_run_id=spec['output_namespace'], generation=spec['output_namespace'],
                              project_revision=spec['project_revision'], recipe_sha256=spec['recipe_sha256'],
                              ingest_cutoff=spec['recipe']['observed_until'],
                              batches=[{k:b[k] for k in ('batch_id','sha256')} for b in spec['batches']],
                              watermarks=[dict(source='synthetic-fixture', partition='fixture-0', position=max(e['source_sequence'] for e in events), event_time=spec['recipe']['event_watermark'])],
                              dim_revisions=[dict(item_id='article-1',content_revision='v1')],
                              dbt_manifest_sha256=receipt['dbt_manifest_sha256'], dbt_run_results_sha256=receipt['dbt_run_results_sha256']),
                  label=dict(target='effective_read', window_anchor='impression_time', rule_version=spec['recipe']['label_rule_version'],
                             window_seconds=1800, maturity_watermark=spec['recipe']['event_watermark'], source='synthetic'),
                  splits=[dict(name=name, **bounds) for name,bounds in spec['recipe']['splits'].items()],
                  files=files,row_count=1,transform_refs=[])
    path = root / 'manifest.json'
    path.write_text(json.dumps(manifest))
    (root / 'export_receipt.json').write_text(json.dumps(dict(execution=execution, build_output=str(build.resolve()),
                                                            manifest_sha256=sha256(path), files=files)))
    return path


def update_manifest(path, mutate):
    manifest = json.loads(path.read_text())
    mutate(manifest)
    path.write_text(json.dumps(manifest))
    receipt_path=path.parent / 'export_receipt.json'
    receipt=json.loads(receipt_path.read_text())
    receipt['manifest_sha256']=sha256(path)
    receipt_path.write_text(json.dumps(receipt))


class PublicationTest(unittest.TestCase):
    def setup_export(self, root):
        store=LocalPublication(Path(root) / 'control')
        epoch=store.claim('g1','a1',binding())
        execution=store.execution('g1','a1',epoch)
        path=fixture_export(Path(root) / 'artifact',execution)
        return store,epoch,path

    def test_valid_export_is_immutable(self):
        with tempfile.TemporaryDirectory() as root:
            store,epoch,path=self.setup_export(root)
            store.publish('g1','a1',epoch,path)
            self.assertEqual(store.state('g1')['manifest_sha256'],sha256(path))
            with self.assertRaises(PublicationError):
                store.claim('g1','a2',binding('ns_a2'))

    def test_cancel_and_new_lease_cannot_publish_old_results(self):
        for cancel in (True,False):
            with self.subTest(cancel=cancel),tempfile.TemporaryDirectory() as root:
                store,epoch,path=self.setup_export(root)
                if cancel:
                    store.cancel('g1')
                    with self.assertRaises(PublicationError): store.publish('g1','a1',epoch,path)
                else:
                    next_epoch=store.claim('g1','a2',binding('ns_a2'))
                    with self.assertRaises(PublicationError): store.publish('g1','a1',epoch,path)
                    with self.assertRaises(PublicationError): store.publish('g1','a2',next_epoch,path)
                self.assertIsNone(store.state('g1')['manifest'])

    def test_tampered_or_incomplete_export_never_publishes(self):
        cases=['empty_manifest','wrong_namespace','wrong_recipe','missing_fragment','changed_fragment','wrong_file_receipt','missing_dbt_node','changed_dbt_hash']
        for case in cases:
            with self.subTest(case=case),tempfile.TemporaryDirectory() as root:
                store,epoch,path=self.setup_export(root)
                if case=='empty_manifest': path.write_text('{}')
                elif case=='wrong_namespace': update_manifest(path,lambda m:m['source'].update(generation='other'))
                elif case=='wrong_recipe': update_manifest(path,lambda m:m['source'].update(recipe_sha256='f'*64))
                elif case=='missing_fragment': (path.parent / 'train.parquet').unlink()
                elif case=='changed_fragment': (path.parent / 'train.parquet').write_bytes(b'not parquet')
                elif case=='wrong_file_receipt': update_manifest(path,lambda m:m['files'][0].update(rows=2))
                elif case=='missing_dbt_node':
                    results=path.parent/'build/target/run_results.json'
                    value=json.loads(results.read_text());value['results'].pop();results.write_text(json.dumps(value))
                elif case=='changed_dbt_hash':
                    results=path.parent/'build/target/run_results.json'
                    results.write_text(results.read_text()+' ')
                with self.assertRaises(PublicationError): store.publish('g1','a1',epoch,path)
                self.assertIsNone(store.state('g1')['manifest'])

    def test_claim_binds_code_recipe_and_source(self):
        with tempfile.TemporaryDirectory() as root:
            store=LocalPublication(root)
            original=binding()
            store.claim('g1','a1',original)
            changed=copy.deepcopy(original)
            changed['output_namespace']='ns_a2'
            changed['recipe']['label_window_seconds']=3600
            changed['recipe_sha256']=canonical_digest(changed['recipe'])
            with self.assertRaises(PublicationError): store.claim('g1','a2',changed)
            with patch('receipt.project_revision',return_value='sha256:'+'f'*64):
                changed=binding('ns_a3')
                with self.assertRaises(PublicationError): store.claim('g1','a3',changed)
            tampered=copy.deepcopy(original);tampered['batches'][0]['sha256']='f'*64
            with self.assertRaises(ValueError): store.claim('other','a1',tampered)

    def test_code_change_after_build_rejects_publish(self):
        with tempfile.TemporaryDirectory() as root:
            store,epoch,path=self.setup_export(root)
            with patch('receipt.project_revision',return_value='sha256:'+'f'*64):
                with self.assertRaises(PublicationError): store.publish('g1','a1',epoch,path)
            self.assertIsNone(store.state('g1')['manifest'])

    def test_concurrent_claims_have_one_current_epoch(self):
        with tempfile.TemporaryDirectory() as root:
            ctx=multiprocessing.get_context('spawn');output=ctx.Queue()
            workers=[ctx.Process(target=claim_worker,args=(root,f'a{i}',output)) for i in range(4)]
            for worker in workers: worker.start()
            for worker in workers: worker.join(15);self.assertEqual(worker.exitcode,0)
            claims=[output.get(timeout=2) for _ in workers]
            self.assertEqual(sorted(epoch for _,epoch in claims),[1,2,3,4])
            store=LocalPublication(root)
            winner=store.state('g1')['attempt']
            for attempt,epoch in claims:
                if attempt!=winner:
                    with self.assertRaises(PublicationError): store.execution('g1',attempt,epoch)

    def test_attempt_cannot_reuse_any_prior_output_namespace(self):
        with tempfile.TemporaryDirectory() as root:
            store=LocalPublication(root)
            store.claim('g1','a1',binding('ns_a1'))
            store.claim('g1','a2',binding('ns_a2'))
            with self.assertRaises(PublicationError): store.claim('g1','a3',binding('ns_a1'))
            self.assertEqual(store.state('g1')['epoch'],2)
