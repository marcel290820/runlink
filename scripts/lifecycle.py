#!/usr/bin/env python3
"""Exercise actual binaries, argument validation and SIGTERM shutdown."""
import json
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request

root = Path(__file__).resolve().parents[1]
for binary in ['runlink', 'runlink-server']:
    path = root/'build'/binary
    for option in ['--help', '--version']:
        run = subprocess.run([path, option], capture_output=True, text=True, check=True)
        assert binary in run.stdout
    run = subprocess.run([path, 'invalid-command'], capture_output=True)
    assert run.returncode == 2
    run = subprocess.run([path, '--credential=fixture-do-not-log'], capture_output=True)
    assert run.returncode == 2 and b'fixture-do-not-log' not in run.stdout+run.stderr
with tempfile.TemporaryDirectory() as temp:
    process = subprocess.Popen([root/'build/runlink-server', '--state-dir', temp+'/state',
                                '--listen', '127.0.0.1:18081'], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        for _ in range(100):
            if process.poll() is not None:
                raise AssertionError('Server exited before readiness')
            try:
                with urllib.request.urlopen('http://127.0.0.1:18081/readyz', timeout=1) as response:
                    assert json.load(response) == {'status': 'ready'}
                break
            except OSError:
                time.sleep(.05)
        else:
            raise AssertionError('Server did not become ready')
        process.terminate()
        _, logs = process.communicate(timeout=5)
        assert process.returncode == 0
        assert b'server_stopped' in logs
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
print('CLI and server lifecycle passed')
