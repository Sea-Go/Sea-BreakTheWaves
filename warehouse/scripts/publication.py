"""Single-host execution fence. DC lease/store integration remains a separate adapter."""
import contextlib
import fcntl
import json
import os
import re
from pathlib import Path

from receipt import input_digest, validate_binding, validate_export


class PublicationError(RuntimeError):
    pass


def checked_name(value):
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,62}', value):
        raise ValueError('expected a lowercase task-owned SQL or output identifier')
    return value


class LocalPublication:
    def __init__(self, directory):
        self.root = Path(directory).resolve()
        self.root.mkdir(parents=True, exist_ok=True)

    @contextlib.contextmanager
    def locked(self, target):
        checked_name(target)
        with (self.root / f'{target}.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            yield

    def state(self, target):
        path = self.root / f'{checked_name(target)}.json'
        return json.loads(path.read_text()) if path.exists() else {'epoch': 0, 'cancelled': False, 'manifest': None}

    def save(self, target, state):
        temporary = self.root / f'{target}.tmp'
        with temporary.open('w') as handle:
            json.dump(state, handle, sort_keys=True, indent=2)
            handle.flush()
            os.fsync(handle.fileno())
        temporary.replace(self.root / f'{target}.json')
        descriptor = os.open(self.root, os.O_RDONLY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def claim(self, target, attempt, binding):
        validate_binding(binding)
        checked_name(binding['output_namespace'])
        digest = input_digest(binding)
        with self.locked(target):
            state = self.state(target)
            if state['manifest']:
                raise PublicationError('immutable generation already published')
            if state.get('input_digest', digest) != digest:
                raise PublicationError('different inputs, code, or recipe require a new generation')
            if state['cancelled']:
                raise PublicationError('cancelled generation requires explicit new generation')
            used_namespaces = state.get('used_namespaces', [])
            if binding['output_namespace'] in used_namespaces:
                raise PublicationError('each attempt requires a fresh output namespace')
            state.update(epoch=state['epoch'] + 1, attempt=checked_name(attempt),
                         input_digest=digest, binding=json.loads(json.dumps(binding)),
                         used_namespaces=used_namespaces + [binding['output_namespace']])
            self.save(target, state)
            return state['epoch']

    def execution(self, target, attempt, epoch):
        state = self.state(target)
        if state['cancelled'] or state['epoch'] != epoch or state.get('attempt') != attempt:
            raise PublicationError('stale or cancelled execution')
        return dict(target=target, attempt=attempt, epoch=epoch,
                    input_digest=state['input_digest'], binding=state['binding'])

    def cancel(self, target):
        with self.locked(target):
            state = self.state(target)
            if state['manifest']:
                raise PublicationError('published generation cannot be cancelled or mutated')
            state.update(epoch=state['epoch'] + 1, cancelled=True)
            self.save(target, state)

    def publish(self, target, attempt, epoch, manifest):
        with self.locked(target):
            state = self.state(target)
            if state['cancelled'] or state['epoch'] != epoch or state.get('attempt') != attempt:
                raise PublicationError('stale or cancelled execution cannot publish')
            if state['manifest']:
                raise PublicationError('immutable generation already published')
            execution = self.execution(target, attempt, epoch)
            try:
                manifest_hash = validate_export(manifest, execution)
            except Exception as error:
                raise PublicationError(f'invalid export: {error}') from error
            state['manifest'] = str(Path(manifest).resolve(strict=True))
            state['manifest_sha256'] = manifest_hash
            self.save(target, state)
