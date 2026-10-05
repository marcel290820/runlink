#!/usr/bin/env python3
"""Generate exact GUI approvals, SRI metadata and the trusted static HTML."""
import argparse
import base64
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1] / 'internal/frontend/assets'
parser = argparse.ArgumentParser()
parser.add_argument('--check', action='store_true')
args = parser.parse_args()

def write(name, content):
    if args.check:
        if not (root / name).exists() or (root / name).read_text() != content:
            raise SystemExit(f'Stale generated asset: {name}; run python3 scripts/assets.py')
    else:
        (root / name).write_text(content)

def sri(name):
    return 'sha256-' + base64.b64encode(hashlib.sha256((root/name).read_bytes()).digest()).decode()

approved = json.dumps([hashlib.sha256((root/'gui.js').read_bytes()).hexdigest()])
write('loader.mjs', (root.parent/'loader.template.mjs').read_text().replace('/* APPROVED_GUI */', approved))
capture = (root/'capture.js').read_text().rstrip('\n')
write('index.html', f'''<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<script>{capture}</script>
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Runlink</title>
<link rel="stylesheet" href="/assets/style.css" integrity="{sri('style.css')}">
<script type="module" src="/assets/loader.mjs" integrity="{sri('loader.mjs')}"></script>
</head>
<body>
<main>
<h1>Runlink</h1>
<p>Run a shared task on its owner's machine.</p>
<p id="status" role="status">Preparing your link…</p>
<form id="secret-form">
<label for="secret">Link secret</label>
<input id="secret" type="password" autocomplete="off" maxlength="256" required>
<button type="submit">Continue</button>
</form>
<p class="note">A secret-bearing link may remain in browser history or synchronization.
Receive the secret separately to keep it out of the URL. Reloading requires the original link or secret.</p>
<section id="task" aria-live="polite"></section>
</main>
</body>
</html>
''')
manifest = {}
for path in sorted(root.iterdir()):
    if path.name != 'manifest.json':
        data = path.read_bytes()
        manifest[path.name] = {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}
write('manifest.json', json.dumps(manifest, indent=2, sort_keys=True) + '\n')
