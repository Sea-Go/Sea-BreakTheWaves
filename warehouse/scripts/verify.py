"""Real ClickHouse/dbt acceptance runner for immutable recommendation datasets."""
import argparse
import hashlib
import sys
import json
import subprocess
import time
from pathlib import Path

from engine import ClickHouse, PROJECT, build, digest
from export import export
from local_server import start
from local_s3 import start as start_s3
from publication import LocalPublication, PublicationError
from receipt import make_binding, project_revision

SPLITS = {'train': {'start': '2026-09-01T08:00:00Z', 'end': '2026-09-01T10:00:00Z'},
          'validation': {'start': '2026-09-01T10:00:00Z', 'end': '2026-09-01T11:00:00Z'},
          'test': {'start': '2026-09-01T11:00:00Z', 'end': '2026-09-01T14:00:00Z'}}


def parameters(revision):
    return dict(landing_schema='sea_fixture_landing', source_batches=['v1'] if revision == 1 else ['v1', 'v2'],
                observed_until='2026-09-01T13:35:00Z' if revision == 1 else '2026-09-01T14:00:00Z',
                event_watermark='2026-09-01T13:20:00Z' if revision == 1 else '2026-09-01T14:00:00Z',
                label_revision=revision, splits=SPLITS, label_window_seconds=1800,
                label_rule_version='effective-read-fixture-v1', feature_contract_id='recommendation-engagement-fixture-v1')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--clickhouse', required=True)
    parser.add_argument('--dbt', required=True)
    parser.add_argument('--seaweed', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--contracts', required=True)
    parser.add_argument('--keep-services', action='store_true')
    args = parser.parse_args()
    root = Path(args.output).resolve()
    root.mkdir(parents=True, exist_ok=False)
    server, endpoint = start(args.clickhouse, root / 'server')
    s3_process = None
    try:
        s3_process, s3_prefix = start_s3(args.seaweed, root / 's3')
        ch = ClickHouse(endpoint)
        publication = LocalPublication(root / 'publication')
        source_revision = project_revision()

        def execution_for(target, attempt, namespace, recipe):
            binding = make_binding(recipe, namespace, args.contracts,
                                   [PROJECT / f'fixtures/{batch}.jsonl' for batch in recipe['source_batches']])
            epoch = publication.claim(target, attempt, binding)
            return epoch, publication.execution(target, attempt, epoch)

        ch.load('sea_fixture_landing', PROJECT / 'fixtures/v1.jsonl', s3_prefix)
        epoch, execution_v1 = execution_for('generation_v1', 'attempt1', 'sea_gen_v1_a1', parameters(1))
        build(args.dbt, endpoint, 'sea_gen_v1_a1', parameters(1), root / 'v1', execution_v1)
        states_v1 = ch.rows('SELECT impression_id, qualification, label_state FROM sea_gen_v1_a1.dws_labels ORDER BY impression_id')
        assert [row['label_state'] for row in states_v1] == ['POSITIVE', 'OBSERVED_NEGATIVE', 'POSITIVE', 'OBSERVED_NEGATIVE', 'PENDING', 'EXCLUDED', 'PENDING']
        assert states_v1[-2]['qualification'] == 'future_feature'
        assert ch.rows('SELECT count() AS n FROM sea_gen_v1_a1.dwd_impressions')[0]['n'] == 7
        manifest_v1 = export(ch, 'sea_gen_v1_a1', parameters(1), root / 'v1', root / 'dataset_v1', args.contracts, source_revision, s3_prefix)
        publication.publish('generation_v1', 'attempt1', epoch, manifest_v1)
        v1_hash = digest(manifest_v1)
        try:
            export(ch, 'sea_gen_v1_a1', parameters(1), root / 'v1', root / 'rejected_overwrite', args.contracts, source_revision, s3_prefix)
            raise AssertionError('existing S3 generation was overwritten')
        except RuntimeError as error:
            assert 'already exists' in str(error) or 'already exist' in str(error)
        assert digest(manifest_v1) == v1_hash
        old_rows = ch.rows('SELECT * FROM sea_gen_v1_a1.ds_recommendation_interaction ORDER BY sample_id')
        assert len(old_rows) == 4
        assert all(row['impression_id'] not in ('i5', 'i6', 'orphan') for row in old_rows)

        # Simulate a mutable current projection after v2. History is a separate append table.
        ch.load('sea_fixture_landing', PROJECT / 'fixtures/v2.jsonl', s3_prefix)
        ch.query("CREATE TABLE sea_fixture_landing.content_current (item_id String, revision String, seq UInt64) ENGINE=ReplacingMergeTree(seq) ORDER BY item_id")
        ch.query("INSERT INTO sea_fixture_landing.content_current SELECT item_id,content_revision,source_sequence FROM sea_fixture_landing.event_history WHERE event_type='content_revision'")
        ch.query('OPTIMIZE TABLE sea_fixture_landing.content_current FINAL')
        assert ch.rows('SELECT revision FROM sea_fixture_landing.content_current FINAL') == [{'revision': 'v2'}]
        _, execution_replay = execution_for('generation_replay', 'attempt1', 'sea_gen_v1_replay', parameters(1))
        build(args.dbt, endpoint, 'sea_gen_v1_replay', parameters(1), root / 'v1_replay', execution_replay)
        assert ch.rows('SELECT * FROM sea_gen_v1_replay.ds_recommendation_interaction ORDER BY sample_id') == old_rows
        assert ch.rows('SELECT content_revision FROM sea_gen_v1_replay.dim_content_history') == [{'content_revision': 'v1'}]
        replay_manifest = export(ch, 'sea_gen_v1_replay', parameters(1), root / 'v1_replay', root / 'dataset_v1_replay', args.contracts, source_revision, s3_prefix)
        original_files = json.loads(manifest_v1.read_text())['files']
        assert json.loads(replay_manifest.read_text())['files'] == original_files

        epoch_v2, execution_v2 = execution_for('generation_v2', 'attempt1', 'sea_gen_v2_a1', parameters(2))
        build(args.dbt, endpoint, 'sea_gen_v2_a1', parameters(2), root / 'v2', execution_v2)
        manifest_v2 = export(ch, 'sea_gen_v2_a1', parameters(2), root / 'v2', root / 'dataset_v2', args.contracts, source_revision, s3_prefix)
        publication.publish('generation_v2', 'attempt1', epoch_v2, manifest_v2)
        rows_v2 = ch.rows('SELECT * FROM sea_gen_v2_a1.ds_recommendation_interaction ORDER BY sample_id')
        assert len(rows_v2) == 6
        late = [row for row in rows_v2 if row['impression_id'] == 'i5'][0]
        assert late['label'] == 1 and late['feature_snapshot_ref'] == 'f5' and late['user_interest'] == .9
        assert all(row['content_revision'] == 'v1' for row in rows_v2)
        during_request = next(row for row in rows_v2 if row['impression_id'] == 'i7')
        assert during_request['request_time'] < during_request['feature_available_at'] <= during_request['feature_cutoff'] < during_request['impression_time']
        assert during_request['label_observation_end'].startswith('2026-09-01 13:43:00')
        assert digest(manifest_v1) == v1_hash
        assert all(digest(manifest_v1.parent / file['path']) == file['sha256'] for file in original_files)

        # A valid SQL result is insufficient after cancellation or fencing.
        cancelled_epoch, _ = execution_for('cancelled_gen', 'attempt1', 'sea_cancelled_a1', parameters(2))
        publication.cancel('cancelled_gen')
        try:
            publication.publish('cancelled_gen', 'attempt1', cancelled_epoch, manifest_v2)
            raise AssertionError('cancelled generation was published')
        except PublicationError:
            assert publication.state('cancelled_gen')['manifest'] is None

        ch.load('sea_fixture_landing', PROJECT / 'fixtures/scope_cases.jsonl', s3_prefix)
        scoped_recipe = parameters(1)
        scoped_recipe['source_batches'] = ['v1', 'scope_cases']
        scope_epoch, scope_execution = execution_for('generation_scopes', 'attempt1', 'sea_scopes_a1', scoped_recipe)
        build(args.dbt, endpoint, 'sea_scopes_a1', scoped_recipe, root / 'scopes', scope_execution)
        scope_rows = ch.rows('SELECT * FROM sea_scopes_a1.ds_recommendation_interaction ORDER BY sample_id')
        assert len(scope_rows) == 7
        repeated = [row for row in scope_rows if row['request_id'] == 'r1' and row['impression_id'] == 'i1']
        assert len(repeated) == 2 and len({row['sample_id'] for row in repeated}) == 2
        assert {row['tenant_id']:row['label'] for row in repeated} == {'tenant-fixture':1, 'other-tenant':0}
        colon_cases = [row for row in scope_rows if row['subject_id'] == 'collision-user']
        assert len(colon_cases) == 2 and len({row['sample_id'] for row in colon_cases}) == 2
        wrong_candidate = ch.rows("SELECT qualification FROM sea_scopes_a1.dws_labels WHERE impression_id='wrong-candidate-impression'")
        assert wrong_candidate == [{'qualification': 'missing_served_candidate'}]
        scope_manifest = export(ch, 'sea_scopes_a1', scoped_recipe, root / 'scopes', root / 'dataset_scopes', args.contracts, source_revision, s3_prefix)
        publication.publish('generation_scopes', 'attempt1', scope_epoch, scope_manifest)

        negative_cases = []
        event = json.loads((PROJECT / 'fixtures/v1.jsonl').read_text().splitlines()[0])
        event.update(batch_id='conflict', payload='{"title":"same immutable event, conflicting body"}')
        event['payload_hash'] = hashlib.sha256(json.dumps({k:v for k,v in event.items() if k not in ('batch_id','payload_hash')}, sort_keys=True).encode()).hexdigest()
        for name, value in [('conflict', event)]:
            path = root / f'{name}.jsonl'
            path.write_text(json.dumps(value) + '\n')
            ch.load('sea_fixture_landing', path, s3_prefix)
            bad = parameters(1)
            bad['source_batches'] = ['v1', name]
            try:
                build(args.dbt, endpoint, f'sea_bad_{name}', bad, root / name)
                raise AssertionError('conflicting source passed dbt')
            except RuntimeError:
                results = json.loads((root / name / 'target/run_results.json').read_text())
                failures = [r['unique_id'] for r in results['results'] if r['status'] == 'fail']
                assert 'test.sea_warehouse.no_event_conflicts' in failures
                negative_cases.append(name)
        # A second event ID with the same logical impression must fail before a fanout can publish.
        impression = next(json.loads(line) for line in (PROJECT / 'fixtures/v1.jsonl').read_text().splitlines()
                          if json.loads(line)['event_type'] == 'impression')
        impression.update(batch_id='duplicate_grain', event_id='duplicate-logical-impression')
        impression['payload_hash'] = hashlib.sha256(json.dumps({k:v for k,v in impression.items() if k not in ('batch_id','payload_hash')}, sort_keys=True).encode()).hexdigest()
        duplicate = root / 'duplicate_grain.jsonl'
        duplicate.write_text(json.dumps(impression) + '\n')
        ch.load('sea_fixture_landing', duplicate, s3_prefix)
        bad = parameters(1)
        bad['source_batches'] = ['v1', 'duplicate_grain']
        try:
            build(args.dbt, endpoint, 'sea_bad_duplicate_grain', bad, root / 'duplicate_grain')
            raise AssertionError('duplicate logical grain passed dbt')
        except RuntimeError:
            results = json.loads((root / 'duplicate_grain/target/run_results.json').read_text())
            assert any(r['status'] == 'fail' and 'unique_fact_grains' in r['unique_id'] for r in results['results'])
            negative_cases.append('duplicate_grain')
        report = dict(status='passed', scope='isolated_clickhouse_dbt_seaweedfs_synthetic_export',
                      source_revision=source_revision, clickhouse=ch.rows('SELECT version() AS version')[0]['version'],
                      datasets={'v1': str(manifest_v1), 'v2': str(manifest_v2), 'scopes': str(scope_manifest)},
                      s3_prefix=s3_prefix, s3_manifests={'v1': 'sea-fixture/sea_gen_v1_a1/manifest.json', 'v2': 'sea-fixture/sea_gen_v2_a1/manifest.json', 'scopes': 'sea-fixture/sea_scopes_a1/manifest.json'},
                      v1_rows=len(old_rows), v2_rows=len(rows_v2), negative_cases=negative_cases,
                      assertions=['exact_redelivery_deduplicated', 'cross_scope_ids_preserved', 'wrong_scope_candidate_excluded', 'tuple_sample_hash_no_delimiter_collision', 'unexposed_not_negative', 'orphan_preserved_not_sample',
                                  'future_feature_excluded', 'in_request_available_feature_accepted', 'pending_not_negative', 'late_label_new_generation',
                                  'current_merge_keeps_rebuildable_history', 'v1_rebuild_parquet_hash_equal',
                                  'old_manifest_files_unchanged', 's3_existing_generation_write_rejected', 'cancelled_sql_result_not_published'])
        (root / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report, indent=2), flush=True)
        if args.keep_services:
            print('Services remain live until this process receives SIGINT.', flush=True)
            while server.poll() is None and s3_process.poll() is None:
                time.sleep(1)
    finally:
        if s3_process is not None:
            s3_process.terminate()
            s3_process.wait(timeout=20)
        server.terminate()
        server.wait(timeout=20)


if __name__ == '__main__':
    try:
        main()
    except KeyboardInterrupt:
        pass
