"""Bind actual warehouse inputs and verified artifacts to one execution attempt."""
import hashlib
import json
from pathlib import Path

import jsonschema
import pyarrow as pa
import pyarrow.parquet as pq

PROJECT = Path(__file__).resolve().parents[1]


def sha256(path):
    value = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def canonical_digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def project_revision():
    # Include relative names as well as file hashes; identical concatenated text is not enough.
    files = {str(path.relative_to(PROJECT)): sha256(path) for path in sorted(PROJECT.rglob('*'))
             if path.is_file() and path.suffix in ('.sql', '.yml', '.py', '.jsonl')
             and '__pycache__' not in path.parts}
    return 'sha256:' + canonical_digest(files)


def make_binding(recipe, namespace, contracts, source_files):
    contracts = Path(contracts).resolve()
    return dict(recipe=recipe, recipe_sha256=canonical_digest(recipe), project_revision=project_revision(),
                output_namespace=namespace,
                batches=[dict(batch_id=Path(path).stem, path=str(Path(path).resolve()), sha256=sha256(path)) for path in source_files],
                schema_path=str(contracts / 'training-dataset-manifest.v2.schema.json'),
                schema_sha256=sha256(contracts / 'training-dataset-manifest.v2.schema.json'),
                columns_path=str(contracts / 'recommend-engagement.columns.v1.json'),
                columns_sha256=sha256(contracts / 'recommend-engagement.columns.v1.json'))


def input_digest(binding):
    return canonical_digest({key: value for key, value in binding.items() if key != 'output_namespace'})


def validate_binding(binding):
    if binding['recipe_sha256'] != canonical_digest(binding['recipe']):
        raise ValueError('recipe digest mismatch')
    if binding['project_revision'] != project_revision():
        raise ValueError('warehouse code changed after binding')
    if [b['batch_id'] for b in binding['batches']] != binding['recipe']['source_batches']:
        raise ValueError('recipe and fixed source batches differ')
    for batch in binding['batches']:
        if Path(batch['path']).stem != batch['batch_id'] or sha256(batch['path']) != batch['sha256']:
            raise ValueError('fixed source batch changed')
    for kind in ('schema', 'columns'):
        if sha256(binding[f'{kind}_path']) != binding[f'{kind}_sha256']:
            raise ValueError('bound contract changed')


def verify_dbt(build_output, execution):
    root = Path(build_output).resolve()
    binding = execution['binding']
    validate_binding(binding)
    manifest_path, results_path = root / 'target/manifest.json', root / 'target/run_results.json'
    manifest, results = json.loads(manifest_path.read_text()), json.loads(results_path.read_text())
    if manifest['metadata']['invocation_id'] != results['metadata']['invocation_id']:
        raise ValueError('dbt artifacts belong to different invocations')
    if results['args']['vars'] != binding['recipe']:
        raise ValueError('dbt executed a different recipe')
    nodes = {key: node for key, node in manifest['nodes'].items()
             if node['resource_type'] in ('model', 'test') and node['config'].get('enabled', True)}
    actual = {result['unique_id']: result for result in results['results']}
    required = {f'model.sea_warehouse.{p.stem}': p for p in (PROJECT / 'models').rglob('*.sql')}
    required.update({f'test.sea_warehouse.{p.stem}': p for p in (PROJECT / 'tests').glob('*.sql')})
    if set(nodes) != set(required) or set(actual) != set(nodes) or len(actual) != len(results['results']):
        raise ValueError('dbt did not finish the complete declared DAG')
    for key, node in nodes.items():
        if node['raw_code'].strip() != required[key].read_text().strip():
            raise ValueError('dbt model text differs from bound project')
        expected = 'success' if node['resource_type'] == 'model' else 'pass'
        if actual[key]['status'] != expected:
            raise ValueError('dbt model or quality check did not pass')
        if node['resource_type'] == 'model' and node['schema'] != binding['output_namespace']:
            raise ValueError('dbt output namespace mismatch')
    return dict(dbt_manifest_sha256=sha256(manifest_path), dbt_run_results_sha256=sha256(results_path))


def write_build_receipt(build_output, execution):
    hashes = verify_dbt(build_output, execution)
    receipt = dict(execution=execution, **hashes)
    (Path(build_output) / 'build_receipt.json').write_text(json.dumps(receipt, sort_keys=True, indent=2) + '\n')
    return receipt


def verify_build_receipt(build_output, execution):
    recorded = json.loads((Path(build_output) / 'build_receipt.json').read_text())
    if recorded['execution'] != execution:
        raise ValueError('build result belongs to another attempt or binding')
    hashes = verify_dbt(build_output, execution)
    if any(recorded.get(key) != value for key, value in hashes.items()):
        raise ValueError('dbt build artifact changed after completion')
    return hashes


def validate_export(path, execution):
    path = Path(path).resolve(strict=True)
    binding, recipe = execution['binding'], execution['binding']['recipe']
    validate_binding(binding)
    manifest = json.loads(path.read_text())
    schema = json.loads(Path(binding['schema_path']).read_text())
    try:
        jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker()).validate(manifest)
    except jsonschema.ValidationError as error:
        raise ValueError('invalid TrainingDatasetManifest v2') from error
    receipt = json.loads((path.parent / 'export_receipt.json').read_text())
    if receipt['execution'] != execution or receipt['manifest_sha256'] != sha256(path):
        raise ValueError('export receipt belongs to another attempt or manifest')
    hashes = verify_build_receipt(receipt['build_output'], execution)
    source = manifest['source']
    expected = dict(project_revision=binding['project_revision'], recipe_sha256=binding['recipe_sha256'],
                    warehouse_run_id=binding['output_namespace'], generation=binding['output_namespace'],
                    ingest_cutoff=recipe['observed_until'],
                    batches=[{k: b[k] for k in ('batch_id', 'sha256')} for b in binding['batches']], **hashes)
    events = [json.loads(line) for batch in binding['batches'] for line in Path(batch['path']).read_text().splitlines()]
    expected['watermarks'] = [dict(source='synthetic-fixture', partition='fixture-0',
                                   position=max(event['source_sequence'] for event in events),
                                   event_time=recipe['event_watermark'])]
    if any(source.get(key) != value for key, value in expected.items()):
        raise ValueError('manifest source differs from claimed inputs or namespace')
    actual_splits = {split['name']: {k: split[k] for k in ('start', 'end')} for split in manifest['splits']}
    if actual_splits != recipe['splits'] or len(actual_splits) != len(manifest['splits']) or manifest['revision'] != recipe['label_revision']:
        raise ValueError('manifest split or label revision differs from recipe')
    if manifest['feature_contract_id'] != recipe['feature_contract_id']:
        raise ValueError('feature contract differs from recipe')
    label = manifest['label']
    if manifest['domain'] != 'recommend' or manifest['data_kind'] != 'synthetic' or label['source'] != 'synthetic' or label['target'] != 'effective_read':
        raise ValueError('dataset domain or label target differs from this bound producer')
    if (label['window_seconds'] != recipe['label_window_seconds'] or label['rule_version'] != recipe['label_rule_version']
            or label['maturity_watermark'] != recipe['event_watermark']):
        raise ValueError('label recipe differs from bound input')
    expected_columns = json.loads(Path(binding['columns_path']).read_text())
    if manifest['columns'] != expected_columns or manifest['files'] != receipt['files']:
        raise ValueError('columns or file receipt mismatch')
    filenames, splits, rows = set(), set(), 0
    referenced_dims = set()
    for file in manifest['files']:
        relative = Path(file['path'])
        resolved = (path.parent / relative).resolve(strict=True)
        if relative.is_absolute() or not resolved.is_relative_to(path.parent) or str(relative) in filenames:
            raise ValueError('invalid or duplicate fragment path')
        filenames.add(str(relative))
        splits.add(file['split'])
        if resolved.stat().st_size != file['size_bytes'] or sha256(resolved) != file['sha256']:
            raise ValueError('fragment size or hash mismatch')
        parquet = pq.ParquetFile(resolved)
        actual_columns = [dict(name=f.name, arrow_type='float64' if pa.types.is_float64(f.type) else str(f.type), nullable=f.nullable)
                          for f in parquet.schema_arrow]
        if actual_columns != expected_columns or parquet.metadata.num_rows != file['rows']:
            raise ValueError('fragment physical schema or row count mismatch')
        for batch in parquet.iter_batches(columns=['item_id', 'content_revision', 'split']):
            for row in batch.to_pylist():
                if row['split'] != file['split']:
                    raise ValueError('fragment split differs from its rows')
                referenced_dims.add((row['item_id'], row['content_revision']))
        rows += file['rows']
    if rows != manifest['row_count'] or splits != set(recipe['splits']):
        raise ValueError('fragment set is incomplete')
    declared_dims = {(d['item_id'], d['content_revision']) for d in source['dim_revisions']}
    if not referenced_dims <= declared_dims:
        raise ValueError('manifest does not cover referenced content revisions')
    return sha256(path)
