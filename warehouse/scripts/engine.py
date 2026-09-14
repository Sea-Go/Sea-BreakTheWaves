"""ClickHouse/dbt IO orchestration; all relational business logic remains SQL."""
import hashlib
import fcntl
import json
import os
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from publication import checked_name

PROJECT = Path(__file__).resolve().parents[1]


def digest(path):
    value = hashlib.sha256()
    with Path(path).open('rb') as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


class ClickHouse:
    def __init__(self, endpoint):
        parsed = urllib.parse.urlsplit(endpoint)
        if parsed.hostname != '127.0.0.1':
            raise ValueError('fixture runner only connects to its isolated localhost server')
        self.endpoint = endpoint

    def query(self, query, data=None):
        suffix = '?' + urllib.parse.urlencode({'query': query}) if data is not None else ''
        req = urllib.request.Request(self.endpoint + suffix, data=data if data is not None else query.encode())
        try:
            with urllib.request.urlopen(req, timeout=90) as response:
                return response.read()
        except urllib.error.HTTPError as error:
            raise RuntimeError(error.read().decode()) from error

    def rows(self, query):
        return [json.loads(line) for line in self.query(query + ' FORMAT JSONEachRow').splitlines()]

    def load(self, database, file, s3_prefix=None):
        checked_name(database)
        payload = Path(file).read_bytes()
        for line in payload.splitlines():
            event = json.loads(line)
            canonical = {key: value for key, value in event.items() if key not in ('batch_id', 'payload_hash')}
            actual = hashlib.sha256(json.dumps(canonical, sort_keys=True).encode()).hexdigest()
            if actual != event.get('payload_hash'):
                raise ValueError('source event hash mismatch')
            if event.get('batch_id') != Path(file).stem:
                raise ValueError('source file does not identify one fixed batch')
        self.query(f'CREATE DATABASE IF NOT EXISTS {database}')
        self.query((PROJECT / 'environment/landing.sql').read_text().format(database=database))
        if s3_prefix:
            url = f'{s3_prefix}/archive/{Path(file).stem}/{digest(file)}.jsonl'
            with urllib.request.urlopen(urllib.request.Request(url, data=payload, method='PUT'), timeout=30) as response:
                response.read()
            with urllib.request.urlopen(url, timeout=30) as response:
                if response.read() != payload:
                    raise ValueError('archive content differs from fixed source batch')
            structure = (PROJECT / 'environment/landing.sql').read_text().split('(\n', 1)[1].split(') ENGINE', 1)[0]
            structure = structure.replace("'", "''")
            self.query(f"INSERT INTO {database}.event_history SELECT * FROM s3('{url}', NOSIGN, 'JSONEachRow', '{structure}') SETTINGS date_time_input_format='best_effort'")
        else:
            self.query(f"INSERT INTO {database}.event_history SETTINGS date_time_input_format='best_effort' FORMAT JSONEachRow", payload)


def build(dbt, endpoint, namespace, parameters, output):
    checked_name(namespace)
    output = Path(output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    env = os.environ.copy()
    env.update(WAREHOUSE_CH_PORT=str(urllib.parse.urlsplit(endpoint).port),
               WAREHOUSE_GENERATION_SCHEMA=namespace,
               DBT_SEND_ANONYMOUS_USAGE_STATS='false', DBT_USE_COLORS='false')
    command = [str(dbt), 'build', '--project-dir', str(PROJECT), '--profiles-dir', str(PROJECT / 'environment'),
               '--target-path', str(output / 'target'), '--log-path', str(output / 'logs'),
               '--vars', json.dumps(parameters), '--no-partial-parse']
    # The isolated runtime owns this lock directory. Production supplies a DC lease/store adapter.
    locks = output.parent / 'sql_target_locks'
    locks.mkdir(exist_ok=True)
    key = hashlib.sha256((endpoint + '/' + namespace).encode()).hexdigest()
    with (locks / key).open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise RuntimeError('same ClickHouse target is already being built') from error
        with (output / 'dbt.log').open('wb') as log:
            result = subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=180)
    if result.returncode:
        raise RuntimeError(f'dbt failed ({result.returncode}); inspect {output}/dbt.log')
    return json.loads((output / 'target/run_results.json').read_text())
