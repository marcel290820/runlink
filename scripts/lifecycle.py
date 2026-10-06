#!/usr/bin/env python3
"""Exercise the built binaries: help, version, argument errors, both roles, SIGTERM."""
import json
from pathlib import Path
import subprocess
import tempfile
import urllib.request

from harness import BUILD, server


def check_cli(name):
    binary = BUILD / name
    for option in ('--help', '--version'):
        result = subprocess.run([binary, option], capture_output=True, text=True, check=True)
        assert name in result.stdout, (option, result.stdout)
    assert subprocess.run([binary, 'invalid-command'], capture_output=True).returncode == 2
    # Rejected arguments may be secrets, so they must never be echoed.
    result = subprocess.run([binary, '--credential=fixture-do-not-log'], capture_output=True)
    assert result.returncode == 2 and b'fixture-do-not-log' not in result.stdout + result.stderr


def get_json(url):
    with urllib.request.urlopen(url, timeout=2) as response:
        return json.load(response)


def check_roles():
    """Run the dev topology: the frontend relays the app's status over loopback."""
    with tempfile.TemporaryDirectory() as temp:
        state, config = Path(temp) / 'state', Path(temp) / 'server.json'
        # server() passes --listen, which must override the file's privileged port.
        config.write_text(json.dumps({'role': 'app', 'listen': '127.0.0.1:1', 'state_dir': str(state)}))
        with server('--config', config) as (app, stop_app):
            assert state.stat().st_mode & 0o777 == 0o700
            with server('--role', 'frontend', '--upstream', app) as (frontend, stop_frontend):
                assert get_json(frontend + '/api/v1/status') == {'status': 'scaffold'}
                assert b'server_stopped' in stop_frontend()
            assert b'server_stopped' in stop_app()


check_cli('runlink')
check_cli('runlink-server')
check_roles()
print('CLI, config override, frontend-to-app relay and SIGTERM lifecycle passed')
