#!/usr/bin/env python3
"""Reject broken local Markdown links and anchors."""
from pathlib import Path
import re
from urllib.parse import unquote, urlsplit

root=Path(__file__).resolve().parents[1]
files=[root/'README.md',root/'AGENTS.md',root/'VISION.md',root/'RATIONALE.md',
       *sorted((root/'architecture').rglob('*.md')),*sorted((root/'docs').rglob('*.md'))]
checked=0
for file in files:
    for link in re.findall(r'\[[^\]]*\]\(([^\s)]+)\)',file.read_text()):
        parsed=urlsplit(link)
        if parsed.scheme or parsed.netloc:continue
        target=(file.parent/unquote(parsed.path)).resolve() if parsed.path else file
        if not target.is_file():raise SystemExit(f'Broken link in {file.relative_to(root)}: {link}')
        if parsed.fragment and target.suffix=='.md':
            headings=re.findall(r'^#+\s+(.+)$',target.read_text(),flags=re.M)
            anchors={re.sub(r'[^\w -]','',heading.lower()).replace(' ','-') for heading in headings}
            if parsed.fragment not in anchors:raise SystemExit(f'Broken anchor in {file.relative_to(root)}: {link}')
        checked+=1
print(f'Documentation: {checked} local links and anchors passed')
