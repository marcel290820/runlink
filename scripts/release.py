#!/usr/bin/env python3
"""Package, sign, verify, and locally deploy or roll back Runlink releases.

Nothing here deploys over the network: deploy and rollback change only the
installation root on the machine that runs them.
"""
import argparse
import contextlib
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tarfile
import tempfile
import time
import urllib.request
from urllib.parse import urlsplit
import zlib

ROOT = Path(__file__).resolve().parents[1]
PLATFORMS = ('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64')
SERVICES = ('runlink-app', 'runlink-frontend')
VERSION = re.compile(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.]+)?')
ARCHIVE = re.compile(rf'runlink-({VERSION.pattern})-((?:linux|darwin)-(?:amd64|arm64))\.tar\.gz')
MANIFEST_LINE = re.compile(r'([0-9a-f]{64})  (.+)')
MEMBERS = frozenset({'bin/runlink', 'bin/runlink-server', 'release.json'})
MAX_ARCHIVE = 128 << 20  # bytes, compressed and expanded
MAX_MANIFEST = 4096
MAX_SIGNATURE = 4096
MAX_SMOKE_BODY = 4096


class ReleaseError(Exception):
    """A rejected release or deployment. Messages are fixed text, safe to print."""


def run(*command, **kwargs):
    # Capture output: subprocess errors can echo key material or server responses.
    return subprocess.run(command, check=True, capture_output=True, **kwargs)


def archive_name(version, target):
    return f'runlink-{version}-{target}.tar.gz'


def build(target, version):
    """Cross-compile both executables for target and return their archive entries."""
    system, arch = target.split('-')
    env = {**os.environ, 'GOOS': system, 'GOARCH': arch, 'CGO_ENABLED': '0'}
    entries = {}
    with tempfile.TemporaryDirectory() as temp:
        for command in ('runlink', 'runlink-server'):
            binary = Path(temp) / command
            run('go', 'build', '-trimpath', '-buildvcs=false',
                '-ldflags', f'-s -w -X runlink/internal/buildinfo.Version={version}',
                '-o', str(binary), f'./cmd/{command}', cwd=ROOT, env=env)
            entries[f'bin/{command}'] = binary.read_bytes()
    return entries


def write_archive(path, entries):
    """Write a reproducible tar.gz: sorted names, zero times and owners, fixed modes."""
    with path.open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', filename='', mtime=0) as gz, \
            tarfile.open(fileobj=gz, mode='w') as tar:
        for name in sorted(entries):
            info = tarfile.TarInfo(name)
            info.size, info.mtime = len(entries[name]), 0
            info.mode = 0o755 if name.startswith('bin/') else 0o644
            tar.addfile(info, io.BytesIO(entries[name]))


def package(args):
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        raise ReleaseError('Release output must be empty')
    targets = args.platforms.split(',')
    if len(set(targets)) != len(targets) or not set(targets) <= set(PLATFORMS):
        raise ReleaseError('Platforms must be distinct supported targets')
    if subprocess.run(['python3', 'scripts/assets.py', '--check'], cwd=ROOT).returncode:
        raise ReleaseError('Generated browser assets are stale')
    metadata = {
        'version': args.version,
        'revision': run('git', 'rev-parse', 'HEAD', cwd=ROOT).stdout.decode().strip(),
        'dirty': bool(run('git', 'status', '--porcelain', cwd=ROOT).stdout),
        'assets': json.loads((ROOT / 'internal/frontend/assets/manifest.json').read_text()),
    }
    for target in targets:
        entries = build(target, args.version)
        entries['release.json'] = (json.dumps({**metadata, 'platform': target}, sort_keys=True, indent=2) + '\n').encode()
        write_archive(output / archive_name(args.version, target), entries)
    sums = ''.join(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n'
                   for path in sorted(output.glob('*.tar.gz')))
    (output / 'SHA256SUMS').write_text(sums)
    print(f'Packaged {len(targets)} targets in {output}')


def sign(args):
    signature = args.directory / 'SHA256SUMS.sig'
    if signature.exists():
        raise ReleaseError('Refusing to replace an existing signature')
    run('openssl', 'dgst', '-sha256', '-sign', str(args.private_key), '-out', str(signature),
        str(args.directory / 'SHA256SUMS'))
    print('Release manifest signed')


def read_regular(path, limit):
    """Read a regular file, never through a symlink, of at most limit bytes."""
    # O_NONBLOCK keeps a FIFO from blocking the open; fstat then rejects it.
    with open(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), 'rb') as file:
        if not stat.S_ISREG(os.fstat(file.fileno()).st_mode):
            raise ReleaseError('Release files must be regular files')
        data = file.read(limit + 1)
    if len(data) > limit:
        raise ReleaseError('Release file exceeds its size limit')
    return data


def verify(directory, public_key):
    """Return every file of a signed release directory, by name, as verified bytes.

    Later steps use only these bytes, so files changed after verification are
    never installed.
    """
    manifest = read_regular(directory / 'SHA256SUMS', MAX_MANIFEST)
    signature = read_regular(directory / 'SHA256SUMS.sig', MAX_SIGNATURE)
    with tempfile.NamedTemporaryFile() as signature_file:
        signature_file.write(signature)
        signature_file.flush()
        try:
            run('openssl', 'dgst', '-sha256', '-verify', str(public_key), '-signature', signature_file.name,
                input=manifest)
        except subprocess.CalledProcessError:
            raise ReleaseError('Release signature verification failed') from None
    files = {'SHA256SUMS': manifest, 'SHA256SUMS.sig': signature}
    versions = set()
    for line in manifest.decode().splitlines():
        entry = MANIFEST_LINE.fullmatch(line)
        archive = entry and ARCHIVE.fullmatch(entry[2])
        if not archive or entry[2] in files:
            raise ReleaseError('Invalid release manifest entry')
        data = read_regular(directory / entry[2], MAX_ARCHIVE)
        if hashlib.sha256(data).hexdigest() != entry[1]:
            raise ReleaseError('Release checksum mismatch')
        files[entry[2]] = data
        versions.add(archive[1])
    if len(versions) != 1:
        raise ReleaseError('Release manifest must describe one nonempty release')
    if {path.name for path in directory.iterdir()} != files.keys():
        raise ReleaseError('Unsigned or unexpected release files')
    return files


def read_archive(name, data):
    """Return the files of a verified archive after enforcing its exact layout."""
    entries, total = {}, 0
    with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as tar:
        for member in tar:
            # The exact allowlist also rules out links, devices, absolute paths and traversal.
            if not member.isfile() or member.name not in MEMBERS or member.name in entries:
                raise ReleaseError('Unexpected archive member')
            total += member.size
            if total > MAX_ARCHIVE:
                raise ReleaseError('Archive extraction limit exceeded')
            with tar.extractfile(member) as file:
                entries[member.name] = file.read()
    if entries.keys() != MEMBERS:
        raise ReleaseError('Release is missing required files')
    metadata = json.loads(entries['release.json'])
    version, target = ARCHIVE.fullmatch(name).groups()
    if not isinstance(metadata, dict) or (metadata.get('version'), metadata.get('platform')) != (version, target):
        raise ReleaseError('Release metadata mismatch')
    return entries


def signed_archive(files, version, target):
    name = archive_name(version, target)
    if name not in files:
        raise ReleaseError('Platform/version missing from signed release')
    return read_archive(name, files[name])


def smoke(url):
    """Poll /healthz and /readyz for up to five seconds until both report success."""
    parts = urlsplit(url)
    if (parts.scheme not in ('http', 'https') or not parts.hostname or parts.username or parts.password
            or parts.query or parts.fragment or parts.path not in ('', '/')):
        raise ReleaseError('Smoke URL must be a credential-free HTTP(S) origin')
    origin = url.rstrip('/')
    deadline = time.monotonic() + 5
    while True:
        try:
            for path, status in (('/healthz', 'ok'), ('/readyz', 'ready')):
                with urllib.request.urlopen(origin + path, timeout=1) as response:
                    code, body = response.status, response.read(MAX_SMOKE_BODY + 1)
                if code != 200 or len(body) > MAX_SMOKE_BODY or json.loads(body) != {'status': status}:
                    raise ValueError('unexpected smoke response')
            return
        except (OSError, ValueError):
            if time.monotonic() >= deadline:
                raise ReleaseError('Smoke check deadline exceeded') from None
            time.sleep(0.1)


@contextlib.contextmanager
def deployment_lock(root):
    """Hold root/.deployment-lock. A crash leaves it behind for an operator to inspect."""
    root.mkdir(parents=True, exist_ok=True)
    lock = root / '.deployment-lock'
    try:
        lock.mkdir()
    except FileExistsError:
        raise ReleaseError('Another deployment holds the lock, or a failed one left it') from None
    try:
        yield
    finally:
        lock.rmdir()


def point_current(root, target):
    """Atomically point root/current at target; return the previous target, if any."""
    current, staging = root / 'current', root / '.current-next'
    if current.exists() and not current.is_symlink():
        raise ReleaseError('current must be a symlink')
    if os.path.lexists(staging):
        raise ReleaseError('A failed switch left .current-next; inspect and remove it')
    previous = os.readlink(current) if current.is_symlink() else None
    staging.symlink_to(target)
    os.replace(staging, current)
    return previous


def activate(root, version, restart, smoke_url):
    """Switch current to version, then restart and smoke-check it.

    On failure, restore the previous link and restart the previous release, or
    stop the service when there was none.
    """
    previous = point_current(root, f'releases/{version}')
    try:
        if restart:
            run('systemctl', 'restart', restart)
        if smoke_url:
            smoke(smoke_url)
    except Exception:
        if previous is None:
            (root / 'current').unlink()
        else:
            point_current(root, previous)
        if restart:
            run('systemctl', 'restart' if previous else 'stop', restart)
        raise


def deploy(args):
    root = args.root.resolve()
    with deployment_lock(root):
        files = verify(args.directory, args.public_key)
        entries = signed_archive(files, args.version, args.platform)
        releases = root / 'releases'
        releases.mkdir(exist_ok=True)
        target = releases / args.version
        if os.path.lexists(target):
            raise ReleaseError('Installed releases are immutable; use a new version')
        with tempfile.TemporaryDirectory(prefix='.release-', dir=releases) as temp:
            stage = Path(temp) / 'content'
            for name, data in {**entries, **{f'signed/{name}': data for name, data in files.items()}}.items():
                path = stage / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
                path.chmod(0o755 if name.startswith('bin/') else 0o644)
            stage.rename(target)
        activate(root, args.version, args.restart, args.smoke_url)
    print(f'Activated {args.version}; runtime restart {"requested" if args.restart else "deferred"}')


def rollback(args):
    root = args.root.resolve()
    with deployment_lock(root):
        target = root / 'releases' / args.version
        if target.is_symlink() or not target.is_dir():
            raise ReleaseError('Rollback release not installed')
        # Compare installed bytes with the retained signed archive instead of trusting them.
        entries = signed_archive(verify(target / 'signed', args.public_key), args.version, args.platform)
        for name, data in entries.items():
            installed = target / name
            if installed.is_symlink() or not installed.is_file() or installed.read_bytes() != data:
                raise ReleaseError('Installed rollback release was modified')
        activate(root, args.version, args.restart, args.smoke_url)
    print(f'Rolled back to {args.version}')


def version_argument(value):
    if not VERSION.fullmatch(value):
        raise argparse.ArgumentTypeError('use vMAJOR.MINOR.PATCH[-suffix]')
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)

    command = commands.add_parser('package', help='build deterministic archives and SHA256SUMS')
    command.add_argument('--version', type=version_argument, required=True)
    command.add_argument('--output', type=Path, required=True)
    command.add_argument('--platforms', default=','.join(PLATFORMS))
    command.set_defaults(action=package)

    command = commands.add_parser('sign', help='sign SHA256SUMS with an RSA private key')
    command.add_argument('directory', type=Path)
    command.add_argument('--private-key', type=Path, required=True)
    command.set_defaults(action=sign)

    command = commands.add_parser('verify', help='check the signature, checksums and file set')
    command.add_argument('directory', type=Path)
    command.add_argument('--public-key', type=Path, required=True)
    command.set_defaults(action=lambda args: (verify(args.directory, args.public_key),
                                              print('Signature and checksums verified')))

    for name, action in (('deploy', deploy), ('rollback', rollback)):
        command = commands.add_parser(name, help=f'{name} a signed release under --root')
        if name == 'deploy':
            command.add_argument('directory', type=Path)
        command.add_argument('--root', type=Path, required=True)
        command.add_argument('--version', type=version_argument, required=True)
        command.add_argument('--platform', choices=PLATFORMS, required=True)
        command.add_argument('--public-key', type=Path, required=True)
        command.add_argument('--restart', choices=SERVICES)
        command.add_argument('--smoke-url')
        command.set_defaults(action=action)

    command = commands.add_parser('smoke', help='check /healthz and /readyz of an origin')
    command.add_argument('url')
    command.set_defaults(action=lambda args: (smoke(args.url), print('Health and readiness smoke passed')))

    args = parser.parse_args()
    try:
        args.action(args)
    except ReleaseError as error:
        parser.exit(1, f'{args.command} failed: {error}\n')
    except (OSError, ValueError, EOFError, zlib.error, tarfile.TarError, subprocess.CalledProcessError):
        # Never print key material, server responses, or subprocess output.
        parser.exit(1, f'{args.command} failed; verify arguments, signature, checksums and local permissions\n')


if __name__ == '__main__':
    main()
