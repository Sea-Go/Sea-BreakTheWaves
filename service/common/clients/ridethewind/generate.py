#!/usr/bin/env python3
"""Generate worker-only wire DTOs from goctl output of the authority API DSL."""
import hashlib, json, pathlib, re, subprocess, sys
root = pathlib.Path(sys.argv[1]).resolve()
out = pathlib.Path(__file__).parent
revision = subprocess.check_output(['git','-C',str(root),'rev-parse',sys.argv[2] if len(sys.argv)>2 else 'HEAD'],text=True).strip()
def read(rel):return subprocess.check_output(['git','-C',str(root),'show',revision+':'+str(rel)])
source = pathlib.Path('service/knowledge/api/internal/types/types.go')
raw = read(source).decode()
wanted = {'Revision','SourceRef','Release','RetrievalProfile','Build','Compile','ClaimBuildReq','AcceptBuildReq','ClaimCompileReq','AcceptCompileReq',
          'CitationLocation','CitationObject','CitationChunk','SearchSnapshot','ReadSearchSourceReq','AcceptSearchCitationsReq',
          'SearchCitationReceipt','SearchCitationReference','SearchCitationRecord',
          'AcceptedSubjectRef','CommitAcceptedAnswerReq','AcceptedAnswer','AcceptedAnswersPage',
          'GetAcceptedAnswerReq','ListAcceptedAnswersReq'}
blocks = dict(re.findall(r'type (\w+) struct \{(.*?)\n\}', raw, re.S))
result = ['// Code generated from RideTheWind api/knowledge.api via goctl types. DO NOT EDIT.\npackage ridethewind\n']
for name in sorted(wanted):
    body = re.sub(r'`path:"[^"]+"`', '`json:"-"`', blocks[name])
    body = body.replace(',optional"', ',omitempty"')
    result.append('type '+name+' struct {'+body+'\n}\n')
(out / 'wire.gen.go').write_text('\n'.join(result))
sources = {str(p):hashlib.sha256(read(p)).hexdigest() for p in [pathlib.Path('api/knowledge.api'),source]}
(out / 'source.lock.json').write_text(json.dumps({'repository':'Sea-RideTheWind','revision':revision,'sources':sources},indent=2)+'\n')
subprocess.run(["gofmt", "-w", str(out / "wire.gen.go")], check=True)
