"""Run built runlink-server binaries for the lifecycle and release checks."""
import contextlib
import json
import os
from pathlib import Path
import select
import subprocess
import time
import urllib.request

BUILD = Path(__file__).resolve().parents[1] / 'build'
STARTUP_SECONDS = 10


def read_line(pipe, timeout):
    """Read one line from pipe, returning what arrived if the deadline passes first."""
    deadline, line = time.monotonic() + timeout, b''
    while not line.endswith(b'\n'):
        remaining = deadline - time.monotonic()
        if remaining <= 0 or not select.select([pipe], [], [], remaining)[0]:
            break
        byte = os.read(pipe.fileno(), 1)  # unbuffered, so communicate() later sees the rest
        if not byte:
            break
        line += byte
    return line


@contextlib.contextmanager
def server(*args, binary=BUILD / 'runlink-server'):
    """Run runlink-server on an ephemeral loopback port and yield (origin, stop).

    The server logs server_started with its address once it is ready, and
    /readyz must then confirm it. stop() sends SIGTERM, requires a clean exit,
    and returns the remaining logs.
    """
    process = subprocess.Popen([binary, '--listen', '127.0.0.1:0', *args],
                               stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        line = read_line(process.stderr, STARTUP_SECONDS)
        event = json.loads(line) if line.startswith(b'{') and line.endswith(b'\n') else {}
        if event.get('msg') != 'server_started':
            raise AssertionError(f'runlink-server did not start: {line!r}')
        origin = 'http://' + event['address']
        with urllib.request.urlopen(origin + '/readyz', timeout=2) as response:
            assert response.status == 200 and json.load(response) == {'status': 'ready'}

        def stop():
            process.terminate()
            _, logs = process.communicate(timeout=5)
            assert process.returncode == 0, logs
            return logs

        yield origin, stop
    finally:
        if process.poll() is None:
            process.kill()
            process.communicate()
