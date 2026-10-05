#!/usr/bin/env python3
"""Generate a runtime coturn configuration without exposing the shared secret."""
from pathlib import Path
import os
import re

try:
    secret = Path('/etc/runlink/turn-secret').read_text().strip()
    if not re.fullmatch(r'[0-9a-f]{64}', secret):
        raise ValueError('Invalid shared secret')
    os.umask(0o077)
    Path('/run/runlink-turn/turnserver.conf').write_text(
        Path('/etc/runlink/turnserver.base.conf').read_text()+'\nstatic-auth-secret='+secret+'\n')
except (OSError, ValueError):
    raise SystemExit('TURN configuration unavailable; check secret permissions and format')
