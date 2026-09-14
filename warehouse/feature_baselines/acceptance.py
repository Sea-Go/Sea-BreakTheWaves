"""Start task-owned PostgreSQL, ClickHouse and SeaweedFS for H10.c E2E."""
import argparse
import getpass
import os
import subprocess
import sys
import tempfile
from pathlib import Path

SCRIPT_ROOT = Path(__file__).resolve().parents[1] / 'scripts'
sys.path.insert(0, str(SCRIPT_ROOT))
from local_server import free_port, start as start_clickhouse
from local_s3 import start as start_s3


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--runtime', required=True)
    parser.add_argument('--postgres-bin', default='/opt/homebrew/opt/postgresql@16/bin')
    args = parser.parse_args()
    runtime = Path(args.runtime).resolve()
    for name in ('clickhouse', 'weed', '.venv/bin/dbt'):
        if not (runtime / name).exists():
            raise SystemExit(f'missing locked warehouse runtime dependency: {runtime / name}')
    evidence = Path(tempfile.mkdtemp(prefix='feature_handoff_', dir=runtime))
    ch = s3 = None
    started_pg = False
    pg_bin = Path(args.postgres_bin)
    pg_dir = evidence / 'postgres'
    port = free_port()
    try:
        subprocess.run([str(pg_bin / 'initdb'), '-D', str(pg_dir), '--no-locale', '--encoding=UTF8', '--auth=trust'],
                       check=True, stdout=(evidence / 'initdb.log').open('wb'))
        subprocess.run([str(pg_bin / 'pg_ctl'), '-D', str(pg_dir), '-l', str(evidence / 'postgres.log'),
                        '-o', f'-h 127.0.0.1 -p {port} -k {evidence}', '-w', 'start'], check=True)
        started_pg = True
        ch, ch_url = start_clickhouse(runtime / 'clickhouse', evidence / 'clickhouse')
        s3, s3_prefix = start_s3(runtime / 'weed', evidence / 'seaweed')
        env = os.environ.copy()
        env.update(USERMODEL_TEST_POSTGRES_DSN=f'postgres://{getpass.getuser()}@127.0.0.1:{port}/postgres?sslmode=disable',
                   USERMODEL_TEST_CH_URL=ch_url, USERMODEL_TEST_S3_PREFIX=s3_prefix,
                   USERMODEL_TEST_DBT=str(runtime / '.venv/bin/dbt'))
        command = ['go', 'test', '-mod=readonly', '-race', '-count=1', '-v', './internal/warehouse/featurebaseline']
        with (evidence / 'go-test.log').open('wb') as log:
            result = subprocess.run(command, cwd=Path(__file__).resolve().parents[2], env=env, stdout=log, stderr=subprocess.STDOUT)
        print(f'Go integration exit={result.returncode}; evidence={evidence}', flush=True)
        if result.returncode:
            print((evidence / 'go-test.log').read_text()[-10000:], flush=True)
        return result.returncode
    finally:
        try:
            for process in (s3, ch):
                if process is not None:
                    process.terminate()
                    try:
                        process.wait(timeout=60)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=10)
        finally:
            if started_pg:
                subprocess.run([str(pg_bin / 'pg_ctl'), '-D', str(pg_dir), '-m', 'immediate', '-w', 'stop'], check=True)


if __name__ == '__main__':
    raise SystemExit(main())
