#!/usr/bin/env python3
"""Exercise the built binaries: help, version, argument errors, both roles, the local stack, SIGTERM."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.request

from harness import BUILD, STARTUP_SECONDS, read_line, server


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


def check_dev_stack():
    """Run runlink-dev on free ports: A relays B, the owner CLI reaches it, SIGTERM stops everything."""
    with tempfile.TemporaryDirectory() as temp:
        state = Path(temp) / 'dev'
        process = subprocess.Popen([BUILD / 'runlink-dev', '--frontend', '127.0.0.1:0', '--app', '127.0.0.1:0',
                                    '--turn', '127.0.0.1:0', '--state-dir', state],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            event = {}
            while event.get('msg') != 'stack_ready':
                line = read_line(process.stderr, STARTUP_SECONDS)
                assert line.startswith(b'{') and line.endswith(b'\n'), f'runlink-dev did not start: {line!r}'
                event = json.loads(line)
            assert get_json(event['frontend'] + '/api/v1/status') == {'status': 'scaffold'}
            result = subprocess.run([BUILD / 'runlink', 'status'], capture_output=True, text=True,
                                    env={**os.environ, 'RUNLINK_SERVICE': event['frontend']})
            assert result.returncode == 0 and result.stdout == f"{event['frontend']}: scaffold\n", result
            assert (state / 'turn-secret').stat().st_mode & 0o777 == 0o600
            process.terminate()
            stdout, logs = process.communicate(timeout=5)
            assert process.returncode == 0, logs
            assert f"RUNLINK_SERVICE={event['frontend']} build/runlink status".encode() in stdout, stdout
            assert logs.count(b'"msg":"server_stopped"') == 2, logs
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()
    # Stand-ins have none of production's exposure controls, so they stay on loopback.
    result = subprocess.run([BUILD / 'runlink-dev', '--turn', '0.0.0.0:3478'], capture_output=True, text=True)
    assert result.returncode == 2 and 'turn must be an IPv4 loopback IP:port' in result.stderr, result


check_cli('runlink')
check_cli('runlink-server')
check_cli('runlink-dev')
check_roles()
check_dev_stack()
print('CLI, config override, frontend-to-app relay, local stack and SIGTERM lifecycle passed')
