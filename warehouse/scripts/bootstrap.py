"""Fetch locked official binaries and an isolated Python environment on macOS ARM64."""
import argparse
import hashlib
import platform
import shutil
import subprocess
import tarfile
import urllib.request
from pathlib import Path

from engine import PROJECT

ARTIFACTS = [
    ('clickhouse', 'https://github.com/ClickHouse/ClickHouse/releases/download/v25.8.28.1-lts/clickhouse-macos-aarch64',
     '1a13ff892a6ba964fc973d6227aa7ed2ea0ae453bdc1b9b51df2b4986a08cfea'),
    ('seaweedfs.tar.gz', 'https://github.com/seaweedfs/seaweedfs/releases/download/3.97/darwin_arm64.tar.gz',
     'ba91178e77fafa1aebad8a842c5b3d7d43821efac4099b048d9e958527fb66bf'),
]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--runtime', required=True)
    args = parser.parse_args()
    if (platform.system(), platform.machine()) != ('Darwin', 'arm64'):
        raise SystemExit('This verified binary lock is macOS ARM64 only. Supply independently verified Linux binaries to verify.py on Linux.')
    uv = shutil.which('uv')
    if not uv:
        raise SystemExit('uv is required to create the isolated Python environment')
    root = Path(args.runtime).resolve()
    root.mkdir(parents=True, exist_ok=True)
    if list(root.iterdir()):
        raise SystemExit('runtime must be an empty task-owned directory')
    for name, url, expected in ARTIFACTS:
        path = root / name
        with urllib.request.urlopen(url, timeout=90) as response, path.open('xb') as out:
            shutil.copyfileobj(response, out)
        if hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise SystemExit(f'Official download hash differs from verified lock: {name}')
    (root / 'clickhouse').chmod(0o755)
    with tarfile.open(root / 'seaweedfs.tar.gz') as archive:
        member = archive.getmember('weed')
        with archive.extractfile(member) as source, (root / 'weed').open('xb') as out:
            shutil.copyfileobj(source, out)
    (root / 'weed').chmod(0o755)
    subprocess.run([uv, 'venv', '--python', '3.12.13', str(root / '.venv')], check=True)
    subprocess.run([uv, 'pip', 'sync', '--python', str(root / '.venv/bin/python'),
                    '--index-url', 'https://pypi.org/simple', str(PROJECT / 'environment/requirements.lock')], check=True)
    print(root)


if __name__ == '__main__':
    main()
