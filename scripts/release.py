#!/usr/bin/env python3
"""Local release packaging and signed, atomic deployment. No network deployment."""
import argparse
import contextlib
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.request
import urllib.error
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]
VERSION = re.compile(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.]+)?\Z')
ARCHIVE = re.compile(r'runlink-(v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.]+)?)-(linux|darwin)-(amd64|arm64)\.tar\.gz\Z')

def run(command, **kwargs):
    return subprocess.run(command, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)

def package(args):
    if not VERSION.fullmatch(args.version):
        raise ValueError('Version must use vMAJOR.MINOR.PATCH[-suffix]')
    dest = args.output.resolve()
    dest.mkdir(parents=True, exist_ok=True)
    if list(dest.iterdir()):
        raise ValueError('Release output must be empty')
    run(['python3', 'scripts/assets.py', '--check'], cwd=ROOT)
    revision = run(['git', 'rev-parse', 'HEAD'], cwd=ROOT).stdout.decode().strip()
    dirty = bool(run(['git', 'status', '--porcelain'], cwd=ROOT).stdout)
    targets = args.platforms.split(',')
    if not targets or len(set(targets)) != len(targets):
        raise ValueError('Duplicate or missing target platform')
    for target in targets:
        if target not in ['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64']:
            raise ValueError('Unsupported platform')
        system, arch = target.split('-')
        with tempfile.TemporaryDirectory() as temp:
            stage = Path(temp)
            (stage/'bin').mkdir()
            env = os.environ.copy()
            env.update(GOOS=system, GOARCH=arch, CGO_ENABLED='0')
            for command in ['runlink', 'runlink-server']:
                run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags',
                     f'-s -w -X runlink/internal/buildinfo.Version={args.version}',
                     '-o', str(stage/'bin'/command), f'./cmd/{command}'], cwd=ROOT, env=env)
            (stage/'release.json').write_text(json.dumps({
                'version': args.version, 'platform': target, 'revision': revision,
                'dirty': dirty, 'assets': json.loads((ROOT/'internal/frontend/assets/manifest.json').read_text())
            }, sort_keys=True, indent=2)+'\n')
            archive = dest/f'runlink-{args.version}-{target}.tar.gz'
            with archive.open('wb') as output, gzip.GzipFile(fileobj=output, mode='wb', filename='', mtime=0) as gz:
                with tarfile.open(fileobj=gz, mode='w') as tar:
                    for file in sorted(stage.rglob('*')):
                        if file.is_file():
                            data = file.read_bytes()
                            info = tarfile.TarInfo(str(file.relative_to(stage)))
                            info.size, info.mtime = len(data), 0
                            info.mode = 0o755 if file.parent.name == 'bin' else 0o644
                            tar.addfile(info, io.BytesIO(data))
    sums = ''.join(f'{hashlib.sha256(file.read_bytes()).hexdigest()}  {file.name}\n'
                   for file in sorted(dest.glob('*.tar.gz')))
    (dest/'SHA256SUMS').write_text(sums)
    print(f'Packaged {len(targets)} targets in {dest}')

def sign(args):
    manifest = args.directory/'SHA256SUMS'
    signature = args.directory/'SHA256SUMS.sig'
    if signature.exists():
        raise ValueError('Refusing to replace an existing signature')
    run(['openssl', 'dgst', '-sha256', '-sign', str(args.private_key), '-out', str(signature), str(manifest)])
    print('Release manifest signed')

def verify(directory, public_key):
    run(['openssl', 'dgst', '-sha256', '-verify', str(public_key), '-signature',
         str(directory/'SHA256SUMS.sig'), str(directory/'SHA256SUMS')])
    manifest = (directory/'SHA256SUMS').read_text()
    if len(manifest) > 4096:
        raise ValueError('Oversized release manifest')
    files = {}
    versions = set()
    for line in manifest.splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  (.+)', line)
        if not match or not ARCHIVE.fullmatch(match[2]) or match[2] in files:
            raise ValueError('Invalid release manifest entry')
        name = match[2]
        file = directory/name
        if file.is_symlink() or not file.is_file() or file.stat().st_size > 128*1024*1024:
            raise ValueError('Invalid release archive')
        if hashlib.sha256(file.read_bytes()).hexdigest() != match[1]:
            raise ValueError('Release checksum mismatch')
        files[name] = file
        versions.add(ARCHIVE.fullmatch(name)[1])
    if not files or len(versions) != 1:
        raise ValueError('Release manifest must describe one nonempty release')
    actual = {file.name for file in directory.iterdir()}
    if actual != set(files) | {'SHA256SUMS', 'SHA256SUMS.sig'}:
        raise ValueError('Unsigned or unexpected release files')
    return files

def unpack(archive, target):
    total = 0
    expected = {'bin/runlink', 'bin/runlink-server', 'release.json'}
    with tarfile.open(archive, 'r:gz') as tar:
        seen = set()
        for item in tar:
            path = Path(item.name)
            if not item.isfile() or path.is_absolute() or '..' in path.parts or item.name in seen:
                raise ValueError('Unsafe archive member')
            if item.name not in expected:
                raise ValueError('Unexpected archive member')
            if not re.fullmatch(r'[a-zA-Z0-9_.\-/]+', item.name):
                raise ValueError('Invalid archive path')
            total += item.size
            if total > 128*1024*1024:
                raise ValueError('Archive extraction limit exceeded')
            seen.add(item.name)
            file = target/path
            file.parent.mkdir(parents=True, exist_ok=True)
            with tar.extractfile(item) as source, file.open('xb') as output:
                shutil.copyfileobj(source, output)
            file.chmod(0o755 if path.parts[0] == 'bin' else 0o644)
        if not expected <= seen:
            raise ValueError('Release is missing required files')
    metadata = json.loads((target/'release.json').read_text())
    match = ARCHIVE.fullmatch(archive.name)
    if metadata['version'] != match[1] or metadata['platform'] != f'{match[2]}-{match[3]}':
        raise ValueError('Release metadata mismatch')
    return metadata

def smoke_url(url):
    parsed = urlsplit(url)
    if parsed.scheme not in ['http', 'https'] or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ['', '/']:
        raise ValueError('Smoke URL must be a credential-free HTTP(S) origin')
    deadline = time.monotonic()+5
    while True:
        try:
            for path, status in [('/healthz','ok'),('/readyz','ready')]:
                with urllib.request.urlopen(url.rstrip('/')+path, timeout=1) as response:
                    data=response.read(4097)
                    if response.status != 200 or len(data)>4096 or json.loads(data) != {'status':status}:
                        raise ValueError('Smoke check failed')
            return
        except (OSError, ValueError):
            if time.monotonic() >= deadline: raise ValueError('Smoke check deadline exceeded')
            time.sleep(.1)

def switch(root, version):
    link = root/'current'
    if link.exists() and not link.is_symlink():
        raise ValueError('Current release must be a symlink')
    previous = os.readlink(link) if link.is_symlink() else None
    temp = root/'.current-next'
    if temp.exists() or temp.is_symlink():
        raise ValueError('Unexpected staging link')
    temp.symlink_to('releases/'+version)
    os.replace(temp, link)
    return previous

def restore(root, previous):
    if previous is None:
        (root/'current').unlink()
    else:
        temp = root/'.current-next'
        temp.symlink_to(previous)
        os.replace(temp, root/'current')

@contextlib.contextmanager
def deployment_lock(root):
    root.mkdir(parents=True, exist_ok=True)
    lock = root/'.deployment-lock'
    lock.mkdir()
    try:
        yield
    finally:
        lock.rmdir()

def activate(args):
    root = args.root.resolve()
    with deployment_lock(root):
        files = verify(args.directory, args.public_key)
        name = f'runlink-{args.version}-{args.platform}.tar.gz'
        if name not in files:
            raise ValueError('Platform/version missing from signed release')
        releases = root/'releases'
        releases.mkdir(exist_ok=True)
        target = releases/args.version
        if target.exists():
            raise ValueError('Immutable release already exists; use a new root or version')
        with tempfile.TemporaryDirectory(prefix='.release-', dir=releases) as temp:
            stage = Path(temp)/'content'
            stage.mkdir()
            unpack(files[name], stage)
            # Retain signed evidence for offline rollback verification.
            shutil.copytree(args.directory, stage/'signed')
            stage.rename(target)
        previous = switch(root, args.version)
        try:
            if args.restart:
                run(['systemctl', 'restart', args.restart])
            if args.smoke_url:
                smoke_url(args.smoke_url)
        except Exception:
            restore(root, previous)
            if args.restart:
                run(['systemctl', 'restart' if previous else 'stop', args.restart])
            raise
        (root/'previous').write_text((previous or '')+'\n')
    print(f'Activated {args.version}; runtime restart {"requested" if args.restart else "deferred"}')

def rollback(args):
    root = args.root.resolve()
    with deployment_lock(root):
        target = root/'releases'/args.version
        if not target.is_dir() or target.is_symlink():
            raise ValueError('Rollback release not installed')
        files = verify(target/'signed', args.public_key)
        name = f'runlink-{args.version}-{args.platform}.tar.gz'
        if name not in files:
            raise ValueError('Rollback platform/version missing')
        # Rebuild from the signed archive, rather than trusting mutable installed bytes.
        with tempfile.TemporaryDirectory(prefix='.rollback-', dir=root) as temp:
            stage = Path(temp)
            unpack(files[name], stage)
            for file in stage.rglob('*'):
                if file.is_file():
                    installed = target/file.relative_to(stage)
                    if installed.is_symlink() or not installed.is_file() or installed.read_bytes() != file.read_bytes():
                        raise ValueError('Installed rollback release was modified')
        previous = switch(root, args.version)
        try:
            if args.restart:
                run(['systemctl','restart',args.restart])
            if args.smoke_url:
                smoke_url(args.smoke_url)
        except Exception:
            restore(root, previous)
            if args.restart and previous:
                run(['systemctl','restart',args.restart])
            raise
    print(f'Rolled back to {args.version}')

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    p = commands.add_parser('package')
    p.add_argument('--version', required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--platforms', default='linux-amd64,linux-arm64,darwin-amd64,darwin-arm64')
    p.set_defaults(function=package)
    p = commands.add_parser('sign')
    p.add_argument('directory', type=Path)
    p.add_argument('--private-key', type=Path, required=True)
    p.set_defaults(function=sign)
    p = commands.add_parser('verify')
    p.add_argument('directory', type=Path)
    p.add_argument('--public-key', type=Path, required=True)
    p.set_defaults(function=lambda args: (verify(args.directory,args.public_key),print('Signature and checksums verified')))
    for command, function in [('deploy', activate),('rollback',rollback)]:
        p = commands.add_parser(command)
        if command == 'deploy':
            p.add_argument('directory', type=Path)
        p.add_argument('--root', type=Path, required=True)
        p.add_argument('--version', required=True)
        p.add_argument('--platform', choices=['linux-amd64','linux-arm64','darwin-amd64','darwin-arm64'], required=True)
        p.add_argument('--public-key', type=Path, required=True)
        p.add_argument('--restart', choices=['runlink-app','runlink-frontend'])
        p.add_argument('--smoke-url')
        p.set_defaults(function=function)
    p = commands.add_parser('smoke')
    p.add_argument('url')
    p.set_defaults(function=lambda args: (smoke_url(args.url),print('Health and readiness smoke passed')))
    args = parser.parse_args()
    if hasattr(args, 'version') and not VERSION.fullmatch(args.version):
        parser.error('Invalid version')
    try:
        args.function(args)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError, tarfile.TarError):
        # Never print key material, server responses or subprocess stderr.
        parser.exit(1, f'{args.command} failed; verify arguments, signature, checksums and local permissions\n')

if __name__ == '__main__':
    main()
