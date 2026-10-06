#!/usr/bin/env python3
"""Relay through pinned coturn on loopback over UDP, TCP, and TLS.

The fixture keeps the rendered production options, quotas, and TLS bounds, and
only swaps in loopback listeners, test ports, temporary credentials and a
loopback-peer exception. It never touches a host firewall, so it proves local
relay behavior, not the production firewall/NAT criterion.
"""
import base64
import hashlib
import hmac
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
RENDERED = ROOT / 'build/infra/c/etc/runlink/turnserver.base.conf'  # written by infra-check.py
HELPER = ROOT / 'infra/templates/configure-turn.py'
PORT, TLS_PORT = 13478, 15349


def fixture_config(base, directory, certificate, key):
    """Return the production base config rebound to loopback and fixture paths."""
    config = re.sub(r'^(listening-ip|relay-ip|allowed-peer-ip)=.*\n', '', base, flags=re.MULTILINE)
    for production, fixture in {
        'listening-port=3478': f'listening-port={PORT}',
        'tls-listening-port=5349': f'tls-listening-port={TLS_PORT}',
        'cert=/etc/runlink/tls/fullchain.pem': f'cert={certificate}',
        'pkey=/etc/runlink/tls/privkey.pem': f'pkey={key}',
        '/var/lib/runlink-turn/turn.sqlite': str(directory / 'turn.sqlite'),
        '/run/runlink-turn/turn.pid': str(directory / 'turn.pid'),
    }.items():
        assert config.count(production) == 1, production
        config = config.replace(production, fixture)
    return config + '\nlistening-ip=127.0.0.1\nrelay-ip=127.0.0.1\nallow-loopback-peers\nallowed-peer-ip=127.0.0.1\nrelay-threads=1\n'


def check_startup_helper(directory, base, secret):
    """Run the exact startup helper against a temporary /etc and /run layout."""
    etc, runtime = directory / 'etc/runlink', directory / 'run/runlink-turn'
    etc.mkdir(parents=True)
    runtime.mkdir(parents=True)
    (etc / 'turnserver.base.conf').write_text(base)
    helper = HELPER.read_text().replace('/etc/runlink', str(etc)).replace('/run/runlink-turn', str(runtime))
    merged = runtime / 'turnserver.conf'

    (etc / 'turn-secret').write_text(secret + '\n')
    result = subprocess.run([sys.executable, '-c', helper], capture_output=True, text=True)
    assert result.returncode == 0 and not result.stdout + result.stderr, result
    assert merged.stat().st_mode & 0o777 == 0o600
    assert merged.read_text() == f'{base}\nstatic-auth-secret={secret}\n'

    # An invalid secret is rejected without echoing it or replacing the old config.
    (etc / 'turn-secret').write_text('invalid-secret\nstatic-auth-secret=injection')
    result = subprocess.run([sys.executable, '-c', helper], capture_output=True, text=True)
    assert result.returncode != 0 and 'injection' not in result.stdout + result.stderr
    assert merged.read_text().endswith(f'static-auth-secret={secret}\n')


def wait_for_listener(process):
    for _ in range(100):
        if process.poll() is not None:
            raise AssertionError('coturn exited during fixture startup')
        try:
            with socket.create_connection(('127.0.0.1', PORT), timeout=0.2):
                return
        except OSError:
            time.sleep(0.05)
    raise AssertionError('coturn did not become ready')


def check_relay(client, secret, certificate):
    for index, (flags, port) in enumerate([([], PORT), (['-t'], PORT), (['-t', '-S', '-E', str(certificate)], TLS_PORT)]):
        # coturn REST credentials: username "expiry:name", password base64(HMAC-SHA1(secret, username)).
        username = f'{int(time.time()) + 60}:fixture-{index}'
        password = base64.b64encode(hmac.new(secret.encode(), username.encode(), hashlib.sha1).digest()).decode()
        result = subprocess.run([client, '-y', '-c', '-s', '-z', '100', '-n', '5', '-m', '2', '-p', str(port),
                                 '-u', username, '-w', password, *flags, '127.0.0.1'],
                                capture_output=True, text=True, timeout=20)
        # uclient can exit zero despite failed allocations, so count the relayed packets too.
        output = result.stdout + result.stderr
        assert result.returncode == 0 and re.search(r'tot_send_msgs=10.*tot_recv_msgs=10', output), \
            'coturn fixture path failed: ' + repr(re.findall(r'(?:tot_[a-z_]+|[a-z_]*msgs)=[0-9]+', output))


def main():
    server, client = shutil.which('turnserver'), shutil.which('turnutils_uclient')
    if not server or not client:
        print('coturn runtime check unavailable on this platform; run on bootstrapped Ubuntu 26.04 amd64')
        return
    with tempfile.TemporaryDirectory(prefix='runlink-turn-') as temp:
        directory = Path(temp)
        certificate, key = directory / 'certificate.pem', directory / 'private.key'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:3072', '-nodes', '-keyout', key, '-out', certificate,
                        '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1'],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        base = fixture_config(RENDERED.read_text(), directory, certificate, key)
        secret = os.urandom(32).hex()
        check_startup_helper(directory, base, secret)
        config = directory / 'turn.conf'
        config.write_text(f'{base}static-auth-secret={secret}\n')
        config.chmod(0o600)
        process = subprocess.Popen([server, '-c', config], cwd=directory, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            wait_for_listener(process)
            check_relay(client, secret, certificate)
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
    print('coturn: bounded config parsed, ephemeral REST credentials, UDP/TCP/TLS same-server loopback relay passed '
          '(production firewall/NAT proof remains external)')


if __name__ == '__main__':
    main()
