#!/usr/bin/env python3
"""Exercise a separate actual serving process and stop it before returning."""
import argparse
import importlib.metadata
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import hashlib

import numpy as np
import psutil

from sea_bge_m3.contracts import MODEL_ID, PROFILES, REVISION


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--model-directory', type=Path, required=True)
    parser.add_argument('--output-directory', type=Path)
    args = parser.parse_args()
    here = Path(__file__).parent.resolve()
    out = args.output_directory or Path(tempfile.mkdtemp(prefix='sea-bge-http-'))
    out.mkdir(parents=True, exist_ok=True)
    command = [sys.executable, '-m', 'sea_bge_m3.server', '--model-directory', str(args.model_directory), '--model-lock', str(here / 'model.lock.json')]
    started = time.monotonic()
    peak = [0]
    stop = threading.Event()
    with (out / 'provider.log').open('w') as log:
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=log, text=True)
        child = psutil.Process(process.pid)
        def sample():
            while not stop.wait(.05):
                try:
                    peak[0] = max(peak[0], child.memory_info().rss)
                except psutil.NoSuchProcess:
                    return
        monitor = threading.Thread(target=sample, daemon=True)
        monitor.start()
        outputs = {}
        try:
            ready = process.stdout.readline()
            if not ready:
                raise RuntimeError('provider exited before ready; see provider.log')
            status = json.loads(ready)
            endpoint = status['endpoint']
            print('serving process ready', process.pid, endpoint, flush=True)
            with urllib.request.urlopen(endpoint + '/v1/models', timeout=5) as response:
                catalog = json.load(response)
            assert catalog['data'][0]['id'] == MODEL_ID
            texts = ['whale whale whale 是什么？', '鲸鱼是生活在海洋中的哺乳动物。']
            requests = {}
            receipts = []
            for kind, profile in PROFILES.items():
                role = 'document' if kind == 'sparse' else 'query'
                request = {
                    'model': MODEL_ID,
                    'configuration_id': '00000000-0000-4000-8000-000000000001',
                    'output_contract': profile['output_contract'],
                    'representation_contract_id': profile['representation_contract']['id'],
                    'representation_space': profile['representation_space'], 'role': role,
                    'input': [{'id': 'query', 'text': texts[0]}, {'id': 'document', 'text': texts[1]}],
                }
                requests[kind] = request
                req = urllib.request.Request(endpoint + '/v1/representations', data=json.dumps(request).encode(), headers={'Content-Type': 'application/json'})
                tick = time.monotonic()
                with urllib.request.urlopen(req, timeout=90) as response:
                    raw = response.read()
                    assert response.status == 200
                outputs[kind] = json.loads(raw)
                assert outputs[kind]['configuration_id'] == request['configuration_id']
                assert outputs[kind]['role'] == role
                assert outputs[kind]['representation_space'] == profile['representation_space']
                assert [item['id'] for item in outputs[kind]['data']] == ['query', 'document']
                (out / (kind + '.json')).write_bytes(raw)
                receipts.append({'kind': kind, 'role': role, 'response_sha256': hashlib.sha256(raw).hexdigest(), 'seconds': time.monotonic()-tick})
                print('actual typed HTTP', kind, 'passed', flush=True)
            (out / 'requests.json').write_text(json.dumps(requests, ensure_ascii=False, indent=2)+'\n')
            dense = [np.array(item['dense']['values']) for item in outputs['dense']['data']]
            assert all(v.shape == (1024,) and np.isfinite(v).all() and abs(np.linalg.norm(v)-1)<1e-5 for v in dense)
            sparse = [dict(zip(item['sparse']['indices'], item['sparse']['weights'])) for item in outputs['sparse']['data']]
            matrices = [np.array(item['token_matrix']['values']) for item in outputs['token_matrix']['data']]
            for item, matrix in zip(outputs['token_matrix']['data'], matrices):
                assert item['token_matrix']['shape'] == list(matrix.shape)
                assert item['token_matrix']['mask'] == [True] * len(matrix)
                assert matrix.shape[1] == 1024 and np.isfinite(matrix).all()
            mean = float(np.max(matrices[0] @ matrices[1].T, axis=1).mean())
            report = {
                'schema_version': 1, 'model': MODEL_ID, 'model_revision': REVISION,
                'data_kind': 'actual-official-model-inference', 'device': 'cpu', 'dtype': 'float32', 'cpu_threads': 2,
                'dense_shape': [len(dense),1024], 'sparse_nonzero': [len(x) for x in sparse], 'token_shapes': [list(x.shape) for x in matrices],
                'scores': {'dense_dot': float(dense[0] @ dense[1]), 'sparse_dot': sum(weight*sparse[1].get(token,0) for token,weight in sparse[0].items()), 'mean_maxsim': mean},
                'requests': receipts, 'versions': {name: importlib.metadata.version(name) for name in ['FlagEmbedding','torch','transformers','numpy']},
                'model_lock_sha256': hashlib.sha256((here/'model.lock.json').read_bytes()).hexdigest(),
                'uv_lock_sha256': hashlib.sha256((here/'uv.lock').read_bytes()).hexdigest(),
            }
        finally:
            process.send_signal(signal.SIGTERM) if process.poll() is None else None
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            stop.set()
            monitor.join()
            if process.stdout:
                process.stdout.close()
        if process.returncode != 0:
            raise RuntimeError(f'provider exit status {process.returncode}')
        report.update(wall_seconds=time.monotonic()-started, peak_rss_bytes=peak[0], provider_stopped=True)
        (out/'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
        print(json.dumps({'result':'passed','evidence':str(out),'report':report},ensure_ascii=False),flush=True)

if __name__ == '__main__':
    main()
