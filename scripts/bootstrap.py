#!/usr/bin/env python3
"""Install checksum-pinned tools into RUNLINK_TOOLS without changing the host.

Run through scripts/bootstrap.sh, which exports the workspace tool environment.
"""
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import threading
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
LOCK = json.loads((ROOT / 'scripts/tools.lock.json').read_text())
LINUX_LIBRARIES = ROOT / 'scripts/linux-libs.lock.json'


def platform_key():
    system = {'Linux': 'linux', 'Darwin': 'darwin'}.get(platform.system())
    machine = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
    key = f'{system}_{machine}'
    if key not in LOCK['platforms']:
        raise SystemExit(f'Unsupported tool platform: {key}')
    return key


def download(url, sha256, destination):
    """Download url to destination; fail unless its bytes match the pinned digest."""
    with urllib.request.urlopen(url, timeout=60) as response, destination.open('wb') as file:
        shutil.copyfileobj(response, file)
    with destination.open('rb') as file:
        if hashlib.file_digest(file, 'sha256').hexdigest() != sha256:
            raise SystemExit(f'Checksum mismatch for {url}')


def link(tools, name, target):
    """Point tools/bin/name at target, replacing only a previous symlink."""
    path = tools / 'bin' / name
    if path.is_symlink():
        path.unlink()
    elif path.exists():
        raise SystemExit(f'Refusing to replace non-symlink {path}')
    path.symlink_to(target)


def install_archives(tools, artifacts):
    """Unpack each pinned archive once into a versioned directory and link its binary."""
    for name, artifact in artifacts.items():
        home = tools / f'{name}-{artifact["version"]}'
        if not home.exists():
            with tempfile.TemporaryDirectory(dir=tools) as temp:
                archive = Path(temp) / 'download.tar.gz'
                download(artifact['url'], artifact['sha256'], archive)
                unpacked = Path(temp) / 'unpacked'
                unpacked.mkdir()
                with tarfile.open(archive) as tar:
                    tar.extractall(unpacked, filter='data')
                unpacked.rename(home)
        link(tools, name, home / artifact['binary'])
    # gofmt and npm ship inside the Go and Node archives.
    link(tools, 'gofmt', tools / f'go-{artifacts["go"]["version"]}/go/bin/gofmt')
    node = artifacts['node']
    link(tools, 'npm', (tools / f'node-{node["version"]}' / node['binary']).parent / 'npm')


def is_ubuntu_26_04_amd64(key):
    try:
        release = platform.freedesktop_os_release()
    except OSError:
        return False
    return key == 'linux_amd64' and release.get('ID') == 'ubuntu' and release.get('VERSION_ID') == '26.04'


def install_linux_libraries(tools):
    """Extract pinned Chromium libraries and the nft/coturn validators without sudo."""
    root = tools / 'linux-libs'
    # Extract afresh so damaged files or packages dropped from the lock cannot linger.
    if root.exists():
        shutil.rmtree(root)
    root.mkdir()
    for package in json.loads(LINUX_LIBRARIES.read_text())['ubuntu_26.04_amd64']:
        with tempfile.TemporaryDirectory(dir=tools) as temp:
            deb = Path(temp) / 'package.deb'
            download(package['url'], package['sha256'], deb)
            subprocess.run(['dpkg-deb', '-x', deb, root], check=True)
    for name, binary in {'nft': 'usr/sbin/nft', 'turnserver': 'usr/bin/turnserver',
                         'turnutils_uclient': 'usr/bin/turnutils_uclient'}.items():
        link(tools, name, root / binary)


def mirror_handler(tools, pins):
    class PinnedMirror(http.server.BaseHTTPRequestHandler):
        """Serve Playwright only downloads whose bytes match a pinned digest."""

        def do_GET(self):
            digest = pins.get(self.path.lstrip('/'))
            if digest is None:
                self.send_error(404, 'Unpinned Playwright download; update scripts/tools.lock.json')
                return
            with tempfile.TemporaryFile(dir=tools) as file:
                with urllib.request.urlopen('https://cdn.playwright.dev' + self.path, timeout=60) as response:
                    shutil.copyfileobj(response, file)
                size = file.tell()
                file.seek(0)
                if hashlib.file_digest(file, 'sha256').hexdigest() != digest:
                    self.send_error(502, 'Playwright download checksum mismatch')
                    return
                self.send_response(200)
                self.send_header('Content-Length', str(size))
                self.end_headers()
                file.seek(0)
                shutil.copyfileobj(file, self.wfile)

        def log_message(self, *args):
            pass

    return PinnedMirror


def install_chromium(tools, key):
    """Let Playwright install its headless shell through a loopback checksum mirror."""
    mirror = http.server.ThreadingHTTPServer(('127.0.0.1', 0), mirror_handler(tools, LOCK['playwright'][key]))
    threading.Thread(target=mirror.serve_forever, daemon=True).start()
    env = {**os.environ, 'PLAYWRIGHT_DOWNLOAD_HOST': f'http://127.0.0.1:{mirror.server_port}'}
    try:
        subprocess.run(['node', 'node_modules/playwright/cli.js', 'install', '--only-shell', 'chromium'],
                       cwd=ROOT, env=env, check=True)
    finally:
        mirror.shutdown()


def main():
    if 'RUNLINK_TOOLS' not in os.environ:
        raise SystemExit('Run scripts/bootstrap.sh, which sets up the tool environment')
    tools = Path(os.environ['RUNLINK_TOOLS']).resolve()
    (tools / 'bin').mkdir(parents=True, exist_ok=True)
    key = platform_key()
    install_archives(tools, LOCK['platforms'][key])
    if is_ubuntu_26_04_amd64(key):
        install_linux_libraries(tools)
    for module in LOCK['go_install'].values():
        subprocess.run(['go', 'install', module], check=True)
    subprocess.run(['npm', 'ci', '--ignore-scripts', '--no-fund', '--no-audit'], cwd=ROOT, check=True)
    install_chromium(tools, key)
    print(f'Tools installed in {tools}; run scripts/check.sh')


if __name__ == '__main__':
    main()
