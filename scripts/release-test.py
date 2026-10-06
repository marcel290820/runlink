#!/usr/bin/env python3
"""Exercise scripts/release.sh with real packages, ephemeral RSA keys, and local roots."""
import hashlib
import http.server
import io
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import threading

from harness import server

ROOT = Path(__file__).resolve().parents[1]
RELEASE = ROOT / 'scripts/release.sh'
TARGET = ({'Linux': 'linux', 'Darwin': 'darwin'}[platform.system()] + '-'
          + {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}[platform.machine()])
V1, V2 = 'v0.0.1-fixture', 'v0.0.2-fixture'
UNREACHABLE = 'http://127.0.0.1:1'


def release(*args, ok=True, env=None):
    result = subprocess.run([RELEASE, *map(str, args)], capture_output=True, text=True, env=env)
    assert (result.returncode == 0) == ok, (args, result.stdout, result.stderr)
    return result


class Fixture:
    def __init__(self, directory):
        self.directory = directory
        self.key, self.public = directory / 'test.key', directory / 'test.pub'
        quiet = {'check': True, 'stdout': subprocess.DEVNULL, 'stderr': subprocess.DEVNULL}
        subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:3072',
                        '-out', self.key], **quiet)
        subprocess.run(['openssl', 'pkey', '-in', self.key, '-pubout', '-out', self.public], **quiet)
        self.bundles = {}
        for version in (V1, V2):
            bundle = directory / version
            release('package', '--version', version, '--platforms', TARGET, '--output', bundle)
            release('sign', bundle, '--private-key', self.key)
            release('verify', bundle, '--public-key', self.public)
            self.bundles[version] = bundle

    def deploy(self, root, version, *extra, bundle=None, ok=True, env=None):
        return release('deploy', bundle or self.bundles[version], '--root', root, '--version', version,
                       '--platform', TARGET, '--public-key', self.public, *extra, ok=ok, env=env)

    def rollback(self, root, version, ok=True):
        return release('rollback', '--root', root, '--version', version, '--platform', TARGET,
                       '--public-key', self.public, ok=ok)

    def copy(self, name):
        """Return a mutable copy of the first signed bundle and its archive."""
        bundle = self.directory / name
        shutil.copytree(self.bundles[V1], bundle)
        return bundle, next(bundle.glob('*.tar.gz'))

    def resign(self, bundle):
        (bundle / 'SHA256SUMS.sig').unlink()
        (bundle / 'SHA256SUMS').write_text(''.join(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n'
                                                   for path in sorted(bundle.glob('*.tar.gz'))))
        release('sign', bundle, '--private-key', self.key)


def current(root):
    return os.readlink(root / 'current')


def check_install_and_failed_update(fixture):
    root = fixture.directory / 'installation'
    fixture.deploy(root, V1)
    assert current(root) == f'releases/{V1}'
    result = subprocess.run([root / 'current/bin/runlink', '--version'], capture_output=True, text=True, check=True)
    assert V1 in result.stdout
    with server('--state-dir', fixture.directory / 'state', binary=root / 'current/bin/runlink-server') as (origin, stop):
        release('smoke', origin)
        stop()
    # A failed smoke check restores the prior link automatically.
    fixture.deploy(root, V2, '--smoke-url', UNREACHABLE, ok=False)
    assert current(root) == f'releases/{V1}'


def check_update_rollback_and_immutability(fixture):
    root = fixture.directory / 'update'
    fixture.deploy(root, V1)
    fixture.deploy(root, V2)
    assert current(root) == f'releases/{V2}'
    fixture.rollback(root, V1)
    assert current(root) == f'releases/{V1}'
    fixture.deploy(root, V1, ok=False)  # installed versions are immutable
    installed = root / 'releases' / V2 / 'bin/runlink'
    original = installed.read_bytes()
    installed.write_bytes(original + b'tamper')
    fixture.rollback(root, V2, ok=False)
    installed.write_bytes(original)
    fixture.rollback(root, V2)
    assert current(root) == f'releases/{V2}'


def check_tampered_bundles(fixture):
    bundle, archive = fixture.copy('corrupt')
    archive.write_bytes(archive.read_bytes() + b'tamper')
    release('verify', bundle, '--public-key', fixture.public, ok=False)

    bundle, _ = fixture.copy('bad-signature')
    (bundle / 'SHA256SUMS.sig').write_bytes(b'invalid-signature')
    release('verify', bundle, '--public-key', fixture.public, ok=False)

    # A FIFO in place of a signed archive is rejected without blocking.
    bundle, archive = fixture.copy('fifo')
    archive.unlink()
    os.mkfifo(archive)
    assert subprocess.run([RELEASE, 'verify', bundle, '--public-key', fixture.public],
                          capture_output=True, timeout=10).returncode != 0

    bundle, _ = fixture.copy('unsigned-extra')
    (bundle / 'extra.js').write_text('evil')
    release('verify', bundle, '--public-key', fixture.public, ok=False)

    # Even a correctly signed extra traversal member cannot escape the installation root.
    bundle, archive = fixture.copy('traversal')
    with tarfile.open(archive, 'r:gz') as source:
        members = [(member, source.extractfile(member).read()) for member in source]
    with tarfile.open(archive, 'w:gz') as tar:
        escape = tarfile.TarInfo('../../escaped')
        escape.size = 4
        for member, data in [*members, (escape, b'evil')]:
            tar.addfile(member, io.BytesIO(data))
    fixture.resign(bundle)
    fixture.deploy(fixture.directory / 'unsafe', V1, bundle=bundle, ok=False)
    assert not (fixture.directory / 'escaped').exists()


def check_smoke_requires_200():
    class Accepted(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            body = b'{"status":"ok"}' if self.path == '/healthz' else b'{"status":"ready"}'
            self.send_response(202)
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    with http.server.ThreadingHTTPServer(('127.0.0.1', 0), Accepted) as accepted:
        threading.Thread(target=accepted.serve_forever, daemon=True).start()
        release('smoke', f'http://127.0.0.1:{accepted.server_port}', ok=False)
        accepted.shutdown()


def check_requested_restarts(fixture):
    """Record systemctl calls through a stub; the host's service manager is never used."""
    commands = fixture.directory / 'commands'
    commands.mkdir()
    log = fixture.directory / 'systemctl.log'
    stub = commands / 'systemctl'
    stub.write_text('#!/bin/sh\nprintf "%s %s\\n" "$1" "$2" >> "$RUNLINK_TEST_SYSTEMCTL_LOG"\n')
    stub.chmod(0o755)
    env = {**os.environ, 'PATH': f'{commands}{os.pathsep}{os.environ["PATH"]}', 'RUNLINK_TEST_SYSTEMCTL_LOG': str(log)}

    # A failed first installation leaves no current link and stops the new service.
    root = fixture.directory / 'first-failure'
    fixture.deploy(root, V1, '--restart', 'runlink-frontend', '--smoke-url', UNREACHABLE, ok=False, env=env)
    assert not (root / 'current').is_symlink()
    assert log.read_text().splitlines() == ['restart runlink-frontend', 'stop runlink-frontend']

    # A failed update restores the link and restarts the previous release.
    log.write_text('')
    root = fixture.directory / 'restart-restore'
    fixture.deploy(root, V1, env=env)
    fixture.deploy(root, V2, '--restart', 'runlink-app', '--smoke-url', UNREACHABLE, ok=False, env=env)
    assert current(root) == f'releases/{V1}'
    assert log.read_text().splitlines() == ['restart runlink-app', 'restart runlink-app']


def check_reproducible_packaging(fixture):
    repeat = fixture.directory / 'repeat'
    release('package', '--version', V1, '--platforms', TARGET, '--output', repeat)
    assert next(repeat.glob('*.tar.gz')).read_bytes() == next(fixture.bundles[V1].glob('*.tar.gz')).read_bytes()


with tempfile.TemporaryDirectory(prefix='runlink-release-') as temp:
    fixture = Fixture(Path(temp))
    check_install_and_failed_update(fixture)
    check_update_rollback_and_immutability(fixture)
    check_tampered_bundles(fixture)
    check_smoke_requires_200()
    check_requested_restarts(fixture)
    check_reproducible_packaging(fixture)
print('Release: reproducible packaging, signatures, checksums, packaged binary smoke, update, rollback, '
      'restart recovery and tamper/traversal rejection passed')
