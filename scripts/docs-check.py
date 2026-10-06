#!/usr/bin/env python3
"""Reject broken local Markdown links and heading anchors."""
from pathlib import Path
import re
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
DOCUMENTS = [ROOT / name for name in ('README.md', 'AGENTS.md', 'VISION.md', 'RATIONALE.md')] + \
    sorted((ROOT / 'architecture').rglob('*.md')) + sorted((ROOT / 'docs').rglob('*.md'))
LINK = re.compile(r'\[[^\]]*\]\(([^\s)]+)\)')
HEADING = re.compile(r'^#+\s+(.+)$', re.MULTILINE)


def anchors(document):
    """GitHub's heading slugs: lowercase, punctuation dropped, spaces to hyphens."""
    return {re.sub(r'[^\w -]', '', heading.lower()).replace(' ', '-')
            for heading in HEADING.findall(document.read_text())}


checked = 0
for document in DOCUMENTS:
    for link in LINK.findall(document.read_text()):
        parts = urlsplit(link)
        if parts.scheme or parts.netloc:
            continue
        target = (document.parent / unquote(parts.path)).resolve() if parts.path else document
        if not target.is_file():
            raise SystemExit(f'Broken link in {document.relative_to(ROOT)}: {link}')
        if parts.fragment and target.suffix == '.md' and parts.fragment not in anchors(target):
            raise SystemExit(f'Broken anchor in {document.relative_to(ROOT)}: {link}')
        checked += 1
print(f'Documentation: {checked} local links and anchors passed')
