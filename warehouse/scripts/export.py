"""Export immutable Parquet with the training-owned H10.b schema."""
import datetime
import json
import urllib.request
from pathlib import Path

import jsonschema
import pyarrow as pa
import pyarrow.parquet as pq

from engine import PROJECT, digest
from publication import checked_name


def export(ch, namespace, parameters, build_output, destination, contract_dir, project_revision, s3_prefix=None):
    checked_name(namespace)
    destination = Path(destination).resolve()
    destination.mkdir(parents=True, exist_ok=False)
    contracts = Path(contract_dir)
    columns = json.loads((contracts / 'recommend-engagement.columns.v1.json').read_text())
    # Select schema-owned names in schema order. ClickHouse writes the Parquet bytes.
    selected = ', '.join(checked_name(column['name']) for column in columns)
    files = []
    for split in parameters['splits']:
        path = destination / f'{checked_name(split)}.parquet'
        sql = (f'SELECT {selected} FROM {namespace}.ds_recommendation_interaction '
               f"WHERE split='{split}' ORDER BY sample_id "
               'SETTINGS output_format_parquet_string_as_string=1 FORMAT Parquet')
        if s3_prefix:
            url = f'{s3_prefix}/{namespace}/{path.name}'
            if not url.startswith('http://127.0.0.1:'):
                raise ValueError('isolated fixture S3 prefix required')
            # Native ClickHouse S3 table function performs the actual export.
            query = (f"INSERT INTO FUNCTION s3('{url}', NOSIGN, 'Parquet') "
                     f'SELECT {selected} FROM {namespace}.ds_recommendation_interaction '
                     f"WHERE split='{split}' ORDER BY sample_id "
                     'SETTINGS output_format_parquet_string_as_string=1, s3_truncate_on_insert=0, s3_create_new_file_on_insert=0')
            ch.query(query)
            with urllib.request.urlopen(url, timeout=30) as response:
                path.write_bytes(response.read())
            remote_count = ch.rows(f"SELECT count() AS n FROM s3('{url}', NOSIGN, 'Parquet')")[0]['n']
        else:
            path.write_bytes(ch.query(sql))
            remote_count = None
        parquet = pq.ParquetFile(path)
        if remote_count is not None and parquet.metadata.num_rows != remote_count:
            raise ValueError('S3 export row count mismatch')
        actual = [{'name': field.name, 'arrow_type': 'float64' if pa.types.is_float64(field.type) else str(field.type), 'nullable': field.nullable}
                  for field in parquet.schema_arrow]
        if actual != columns:
            raise ValueError(f'Parquet schema differs from H10.b: {actual!r}')
        files.append(dict(path=path.name, split=split, rows=parquet.metadata.num_rows,
                          size_bytes=path.stat().st_size, sha256=digest(path)))
    revision = parameters['label_revision']
    source_events = []
    for batch in parameters['source_batches']:
        source_events.extend(json.loads(line) for line in (PROJECT / f'fixtures/{batch}.jsonl').read_text().splitlines())
    manifest = dict(
        schema_version='sea.training-dataset.v1', row_contract='sea.recommend-engagement.v1', dataset_id='recommendation-interaction-fixture',
        revision=revision, parent_revision=None if revision == 1 else revision-1,
        domain='recommend', data_kind='synthetic',
        created_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
        feature_contract_id='recommendation-engagement-fixture-v1', columns=columns,
        source=dict(warehouse_run_id=namespace, project_revision=project_revision, generation=namespace,
                    ingest_cutoff=parameters['observed_until'],
                    batches=[dict(batch_id=batch, sha256=digest(PROJECT / f'fixtures/{batch}.jsonl'))
                             for batch in parameters['source_batches']],
                    watermarks=[dict(source='synthetic-fixture', partition='fixture-0',
                                     position=max(event['source_sequence'] for event in source_events),
                                     event_time=parameters['event_watermark'])],
                    dim_revisions=[row['revision'] for row in ch.rows(
                        f"SELECT concat(item_id, ':', content_revision) AS revision FROM {namespace}.dim_content_history ORDER BY revision")],
                    dbt_manifest_sha256=digest(Path(build_output) / 'target/manifest.json'),
                    dbt_run_results_sha256=digest(Path(build_output) / 'target/run_results.json')),
        label=dict(target='effective_read', window_anchor='impression_time', rule_version='effective-read-fixture-v1', window_seconds=1800,
                   maturity_watermark=parameters['event_watermark'], source='synthetic'),
        splits=[dict(name=name, **bounds) for name, bounds in parameters['splits'].items()],
        files=files, row_count=sum(file['rows'] for file in files), transform_refs=[])
    schema = json.loads((contracts / 'training-dataset-manifest.v1.schema.json').read_text())
    jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker()).validate(manifest)
    # Structural schema alone is not consumer acceptance. Reader verifies per-row semantics.
    path = destination / 'manifest.json'
    path.write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
    if s3_prefix:
        with urllib.request.urlopen(urllib.request.Request(f'{s3_prefix}/{namespace}/manifest.json', data=path.read_bytes(), method='PUT'), timeout=30) as response:
            response.read()
    return path
