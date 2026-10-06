#!/usr/bin/env python3
"""Install checksum-pinned tools without changing the host."""
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

root = Path(__file__).resolve().parents[1]
tools = Path(os.environ.get('RUNLINK_TOOLS', root / '.tools')).resolve()
tools.mkdir(parents=True, exist_ok=True)
(tools / 'bin').mkdir(exist_ok=True)
os_name = {'Linux': 'linux', 'Darwin': 'darwin'}.get(platform.system())
arch = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
key = f'{os_name}_{arch}'
lock = json.loads((root / 'scripts/tools.lock.json').read_text())
if key not in lock['platforms']:
    raise SystemExit(f'Unsupported tool platform: {key}')
for name, artifact in lock['platforms'][key].items():
    dest = tools / f'{name}-{artifact["version"]}'
    if not dest.exists():
        with tempfile.TemporaryDirectory(dir=tools) as temp:
            archive = Path(temp) / 'download.tar.gz'
            urllib.request.urlretrieve(artifact['url'], archive)
            algorithm = 'sha256' if 'sha256' in artifact else 'sha512'
            if hashlib.new(algorithm, archive.read_bytes()).hexdigest() != artifact[algorithm]:
                raise SystemExit(f'{name}: download checksum mismatch')
            unpacked = Path(temp) / 'unpacked'
            unpacked.mkdir()
            with tarfile.open(archive) as tar:
                tar.extractall(unpacked, filter='data')
            unpacked.rename(dest)
    binary = dest / artifact['binary']
    target = tools / 'bin' / name
    if target.is_symlink():
        target.unlink()
    elif target.exists():
        raise SystemExit(f'Refusing to replace non-symlink {target}')
    target.symlink_to(binary)
go_binary = tools/f'go-{lock["platforms"][key]["go"]["version"]}'/'go/bin/gofmt'
target = tools/'bin/gofmt'
if target.is_symlink(): target.unlink()
elif target.exists(): raise SystemExit('Refusing to replace gofmt')
target.symlink_to(go_binary)
# Minimal Ubuntu hosts can extract pinned Chromium libraries without sudo.
if key == 'linux_amd64' and Path('/etc/os-release').exists():
    os_release = Path('/etc/os-release').read_text()
    if 'ID=ubuntu' in os_release and 'VERSION_ID="26.04"' in os_release:
        library_lock = json.loads((root/'scripts/linux-libs.lock.json').read_text())
        library_root = tools/'linux-libs'
        library_root.mkdir(exist_ok=True)
        for artifact in library_lock['ubuntu_26.04_amd64']:
            with tempfile.TemporaryDirectory(dir=tools) as temp:
                file = Path(temp)/'library.deb'
                urllib.request.urlretrieve(artifact['url'], file)
                if hashlib.sha256(file.read_bytes()).hexdigest() != artifact['sha256']:
                    raise SystemExit('Browser library checksum mismatch')
                subprocess.run(['dpkg-deb', '-x', file, library_root], check=True)
        for name, binary in {'nft':'usr/sbin/nft', 'turnserver':'usr/bin/turnserver',
                             'turnutils_uclient':'usr/bin/turnutils_uclient'}.items():
            target = tools/'bin'/name
            if target.is_symlink(): target.unlink()
            elif target.exists(): raise SystemExit('Refusing to replace validator')
            target.symlink_to(library_root/binary)
# npm is part of the pinned Node archive.
node = lock['platforms'][key]['node']
npm = tools / f'node-{node["version"]}' / node['binary']
target = tools/'bin/npm'
if target.is_symlink():
    target.unlink()
elif target.exists():
    raise SystemExit('Refusing to replace npm')
target.symlink_to(npm.parent/'npm')
env = os.environ.copy()
env.update(PATH=str(tools/'bin')+os.pathsep+env['PATH'], GOTOOLCHAIN='local',
           GOENV='off', GOPATH=str(tools/'gopath'), GOCACHE=str(tools/'gocache'), GOBIN=str(tools/'bin'),
           npm_config_cache=str(tools/'npm-cache'), npm_config_userconfig=str(tools/'npm-user.conf'),
           npm_config_globalconfig=str(tools/'npm-global.conf'), PLAYWRIGHT_BROWSERS_PATH=str(tools/'browsers'))
for module in lock['go_install'].values():
    subprocess.run(['go', 'install', module], env=env, check=True)
subprocess.run(['npm', 'ci', '--ignore-scripts', '--no-fund', '--no-audit'], cwd=root, env=env, check=True)


class PinnedPlaywrightMirror(http.server.BaseHTTPRequestHandler):
    """Loopback mirror that hands Playwright only checksum-pinned downloads."""

    def do_GET(self):
        digest = lock['playwright'][key].get(self.path.lstrip('/'))
        if digest is None:
            self.send_error(404, 'Unpinned Playwright download; update scripts/tools.lock.json')
            return
        with tempfile.TemporaryFile(dir=tools) as file:
            with urllib.request.urlopen('https://cdn.playwright.dev' + self.path, timeout=60) as response:
                shutil.copyfileobj(response, file)
            file.seek(0)
            if hashlib.file_digest(file, 'sha256').hexdigest() != digest:
                self.send_error(502, 'Playwright download checksum mismatch')
                return
            self.send_response(200)
            self.send_header('Content-Length', str(file.tell()))
            self.end_headers()
            file.seek(0)
            shutil.copyfileobj(file, self.wfile)

    def log_message(self, *args):
        pass


mirror = http.server.ThreadingHTTPServer(('127.0.0.1', 0), PinnedPlaywrightMirror)
threading.Thread(target=mirror.serve_forever, daemon=True).start()
env['PLAYWRIGHT_DOWNLOAD_HOST'] = f'http://127.0.0.1:{mirror.server_port}'
try:
    subprocess.run(['node', 'node_modules/playwright/cli.js', 'install', '--only-shell', 'chromium'],
                   cwd=root, env=env, check=True)
finally:
    mirror.shutdown()
print(f'Tools installed in {tools}; run scripts/check.sh')
