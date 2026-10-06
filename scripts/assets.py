#!/usr/bin/env python3
"""Generate the trusted loader, page, and asset manifest from internal/frontend.

loader.mjs embeds the approved GUI hash, index.html inlines the capture script
and pins its stylesheet and loader with SRI, and manifest.json covers every
asset. With --check, fail on stale output instead of rewriting it.
"""
import argparse
import base64
import hashlib
import json
from pathlib import Path

FRONTEND = Path(__file__).resolve().parents[1] / 'internal/frontend'
ASSETS = FRONTEND / 'assets'
GENERATED = ('loader.mjs', 'index.html', 'manifest.json')


def integrity(data: bytes) -> str:
    return 'sha256-' + base64.b64encode(hashlib.sha256(data).digest()).decode()


def fill(template: Path, values: dict[str, str]) -> str:
    text = template.read_text(encoding='utf-8')
    for placeholder, value in values.items():
        if text.count(placeholder) != 1:
            raise SystemExit(f'{template.name}: {placeholder} must appear exactly once')
        text = text.replace(placeholder, value)
    return text


def generate() -> dict[str, bytes]:
    """Return the expected bytes of every generated asset."""
    source = {path.name: path.read_bytes() for path in ASSETS.iterdir() if path.name not in GENERATED}
    approved = json.dumps([hashlib.sha256(source['gui.js']).hexdigest()])
    loader = fill(FRONTEND / 'loader.template.mjs', {'/* APPROVED_GUI */': approved}).encode()
    index = fill(FRONTEND / 'index.template.html', {
        '{{CAPTURE}}': source['capture.js'].decode().rstrip('\n'),
        '{{STYLE_INTEGRITY}}': integrity(source['style.css']),
        '{{LOADER_INTEGRITY}}': integrity(loader),
    }).encode()
    assets = {**source, 'loader.mjs': loader, 'index.html': index}
    manifest = {name: {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}
                for name, data in assets.items()}
    return {'loader.mjs': loader, 'index.html': index,
            'manifest.json': (json.dumps(manifest, indent=2, sort_keys=True) + '\n').encode()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='fail on stale generated assets')
    check = parser.parse_args().check
    for name, data in generate().items():
        path = ASSETS / name
        if not check:
            path.write_bytes(data)
        elif not path.is_file() or path.read_bytes() != data:
            raise SystemExit(f'Stale generated asset: {name}; run python3 scripts/assets.py')


if __name__ == '__main__':
    main()
