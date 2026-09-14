"""Task-owned SeaweedFS S3 for integration checks, never the shared home cloud."""
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

from local_server import free_port


def start(binary, root):
    root = Path(root).resolve()
    root.mkdir(parents=True, exist_ok=False)
    data = root / 'data'
    data.mkdir()
    (root / 'filer.toml').write_text('[leveldb2]\nenabled = true\ndir = "' + str(root / 'filer') + '"\n')
    ports = {name: free_port() for name in ('master', 'volume', 'filer', 's3', 'master_grpc', 'volume_grpc', 'filer_grpc', 's3_grpc')}
    command = [str(Path(binary).resolve()), 'server', '-ip=127.0.0.1', '-ip.bind=127.0.0.1',
               f'-dir={data}', '-filer', '-s3', '-master.volumeSizeLimitMB=100', '-volume.max=3',
               '-master.telemetry=false', '-volume.preStopSeconds=0']
    command.extend(f'-{name}.port={ports[name]}' for name in ('master', 'volume', 'filer', 's3'))
    command.extend(f'-{name}.port.grpc={ports[name + "_grpc"]}' for name in ('master', 'volume', 'filer', 's3'))
    with (root / 'process.log').open('wb') as log:
        proc = subprocess.Popen(command, cwd=root, stdout=log, stderr=subprocess.STDOUT)
    endpoint = f'http://127.0.0.1:{ports["s3"]}'
    try:
        for _ in range(150):
            if proc.poll() is not None:
                raise RuntimeError(f'SeaweedFS exited; inspect {root}/process.log')
            try:
                req = urllib.request.Request(endpoint + '/sea-fixture', data=b'', method='PUT')
                urllib.request.urlopen(req, timeout=2).close()
                return proc, endpoint + '/sea-fixture'
            except (OSError, TimeoutError):
                time.sleep(.2)
        raise RuntimeError('SeaweedFS startup timed out')
    except BaseException:
        proc.terminate()
        proc.wait(timeout=20)
        raise
