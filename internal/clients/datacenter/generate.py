#!/usr/bin/env python3
"""Refresh the public DC wire SDK from one explicitly supplied checkout."""
import hashlib, json, pathlib, subprocess, sys
root = pathlib.Path(sys.argv[1]).resolve()
out = pathlib.Path(__file__).parent
revision = subprocess.check_output(['git','-C',str(root),'rev-parse',sys.argv[2] if len(sys.argv)>2 else 'HEAD'],text=True).strip()
def read(rel):return subprocess.check_output(['git','-C',str(root),'show',revision+':'+str(rel)])
sources = {}
for name, filename in [('representation','representation.go'), ('eventing','events.go'), ('jobs','jobs.go')]:
    rel = pathlib.Path('contracts') / name / filename
    raw = read(rel)
    target = out / 'wire' / name / 'contract.gen.go'
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(b'// Code generated from the DataCenter public wire contract. DO NOT EDIT.\n' + raw)
    sources[str(rel)] = hashlib.sha256(raw).hexdigest()
for source in [pathlib.Path(p) for p in subprocess.check_output(['git','-C',str(root),'ls-tree','-r','--name-only',revision,'contracts/representation/testdata'],text=True).splitlines() if p.endswith('.json')]:
    target = out / 'testdata' / source.name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(read(source))
(out / 'source.lock.json').write_text(json.dumps({'repository':'Sea-DataCenter','revision':revision,'sources':sources},indent=2)+'\n')
