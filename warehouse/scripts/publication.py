"""Single-host execution fence. DC lease/store integration remains a separate adapter."""
import contextlib
import fcntl
import hashlib
import json
import os
import re
from pathlib import Path


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

    def claim(self, target, attempt, input_digest):
        if not re.fullmatch(r'[a-f0-9]{64}', input_digest):
            raise ValueError('logical input digest must be SHA-256')
        with self.locked(target):
            state = self.state(target)
            if state['manifest']:
                raise PublicationError('immutable generation already published')
            if state.get('input_digest', input_digest) != input_digest:
                raise PublicationError('different logical inputs require a new generation')
            if state['cancelled']:
                raise PublicationError('cancelled generation requires explicit new generation')
            state.update(epoch=state['epoch'] + 1, attempt=checked_name(attempt), input_digest=input_digest)
            self.save(target, state)
            return state['epoch']

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
            path = Path(manifest).resolve(strict=True)
            if not path.is_file():
                raise PublicationError('manifest file missing')
            state['manifest'] = str(path)
            state['manifest_sha256'] = hashlib.sha256(path.read_bytes()).hexdigest()
            self.save(target, state)
