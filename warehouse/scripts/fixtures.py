"""Synthetic producer events with explicit expected labels, never real user data."""
import hashlib
import json
from pathlib import Path

DAY = '2026-09-01T'
FEATURE = 'recommendation-engagement-fixture-v1'


def iso(time):
    return DAY + time + ':00Z'


def make_events():
    events = []

    def add(kind, key, time, *, batch='v1', arrived=None, payload=None, **values):
        event = dict(batch_id=batch, event_id=key, domain='recommendation', event_type=kind,
                     authority_id='rtw', tenant_id='tenant-fixture', subject_id='user-fixture',
                     request_id='', candidate_id='', impression_id='', item_id='article-1',
                     content_revision='v1', stage='', stage_invocation_id='', attempt=1,
                     feature_snapshot_ref='', feature_contract_id=FEATURE,
                     event_time=iso(time), available_at=iso(arrived or time),
                     source_partition='fixture-0', source_sequence=len(events) + 1,
                     payload=json.dumps(payload or {}, sort_keys=True, separators=(',', ':')))
        event.update(values)
        canonical = {k: v for k, v in event.items() if k != 'batch_id'}
        event['payload_hash'] = hashlib.sha256(json.dumps(canonical, sort_keys=True).encode()).hexdigest()
        events.append(event)
        return event

    add('content_revision', 'content-v1', '08:00', payload={'title': 'Original article'})
    # Same immutable revision may be redelivered; exact duplicate does not multiply joins.
    events.append(dict(events[0]))
    for num, time, interest, positive in [(1, '09:00', .8, True), (2, '09:15', .2, False),
                                         (3, '10:00', .7, True), (4, '11:00', .1, False),
                                         (5, '13:00', .9, False), (6, '13:05', .5, False)]:
        r, c, i, f, invocation = f'r{num}', f'c{num}', f'i{num}', f'f{num}', f'stage{num}'
        add('feature_snapshot', f'feature-{num}', '08:30', arrived='13:06' if num == 6 else '08:30',
            feature_snapshot_ref=f, payload={'user_interest': interest, 'item_quality': .6})
        add('request', f'request-{num}', time, request_id=r, feature_snapshot_ref=f,
            payload={'feature_cutoff': iso(time)})
        add('candidate', f'candidate-{num}', time, request_id=r, candidate_id=c,
            stage='served', stage_invocation_id=invocation)
        add('impression', f'impression-{num}', time, request_id=r, candidate_id=c,
            impression_id=i, stage_invocation_id=invocation, payload={'visible': True})
        if positive:
            hour, minute = map(int, time.split(':'))
            add('behavior', f'read-{num}', f'{hour:02}:{minute+2:02}', request_id=r,
                impression_id=i, payload={'action': 'effective_read'})
    # Features can become available during request execution, before actual display.
    add('feature_snapshot', 'feature-7', '13:10', arrived='13:11', feature_snapshot_ref='f7',
        payload={'user_interest': .4, 'item_quality': .6})
    add('request', 'request-7', '13:10', arrived='13:12', request_id='r7', feature_snapshot_ref='f7',
        payload={'feature_cutoff': iso('13:12')})
    add('candidate', 'candidate-7', '13:12', request_id='r7', candidate_id='c8', stage='served', stage_invocation_id='stage7')
    add('impression', 'impression-7', '13:13', request_id='r7', candidate_id='c8', impression_id='i7', stage_invocation_id='stage7', payload={'visible': True})
    add('candidate', 'served-retry', '13:00', request_id='r5', candidate_id='c5',
        stage='served', stage_invocation_id='stage5', attempt=2)
    add('candidate', 'unexposed', '13:10', request_id='r5', candidate_id='c7',
        stage='recall', stage_invocation_id='recall5')
    add('behavior', 'orphan-behavior', '12:00', request_id='missing', impression_id='orphan',
        payload={'action': 'effective_read'})
    add('behavior', 'late-read', '13:25', arrived='13:40', batch='v2', request_id='r5',
        impression_id='i5', payload={'action': 'effective_read'})
    add('content_revision', 'content-v2', '08:00', arrived='13:40', batch='v2',
        content_revision='v2', payload={'title': 'Later correction, old evidence preserved'})
    return events


def write(root):
    root = Path(root)
    root.mkdir(parents=True, exist_ok=True)
    events = make_events()
    for batch in ('v1', 'v2'):
        (root / f'{batch}.jsonl').write_text(''.join(json.dumps(e, sort_keys=True) + '\n' for e in events if e['batch_id'] == batch))
    return events


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument('directory')
    write(parser.parse_args().directory)


def write_scope_cases(root):
    """Colliding local IDs across subjects and invalid candidate identity."""
    base = make_events()
    chain = [e for e in base if e['event_id'] in ('feature-1', 'request-1', 'candidate-1', 'impression-1', 'read-1')]
    cases = []
    # Same local request/impression/feature/event IDs in another tenant are legitimate.
    for original in chain:
        if original['event_type'] != 'behavior':
            cases.append(dict(original, batch_id='scope_cases', tenant_id='other-tenant'))
    for label, request, impression in [('a', 'r:part', 'one'), ('b', 'r', 'part:one')]:
        for original in chain:
            value = dict(original, batch_id='scope_cases', event_id=f'collision-{label}-{original["event_id"]}',
                         subject_id='collision-user')
            if value['request_id']:
                value['request_id'] = request
            if value['impression_id']:
                value['impression_id'] = impression
            if value['feature_snapshot_ref']:
                value['feature_snapshot_ref'] = 'collision-feature-' + label
            cases.append(value)
    for original in chain:
        value = dict(original, batch_id='scope_cases', event_id='wrong-candidate-' + original['event_id'])
        if value['request_id']:
            value['request_id'] = 'wrong-candidate-request'
        if value['impression_id']:
            value['impression_id'] = 'wrong-candidate-impression'
        if value['feature_snapshot_ref']:
            value['feature_snapshot_ref'] = 'wrong-candidate-feature'
        if value['event_type'] == 'candidate':
            value['tenant_id'] = 'wrong-tenant'
        cases.append(value)
    for number, event in enumerate(cases, 100):
        event['source_sequence'] = number
        canonical = {k:v for k,v in event.items() if k not in ('batch_id','payload_hash')}
        event['payload_hash'] = hashlib.sha256(json.dumps(canonical, sort_keys=True).encode()).hexdigest()
    path = Path(root) / 'scope_cases.jsonl'
    path.write_text(''.join(json.dumps(event, sort_keys=True) + '\n' for event in cases))
