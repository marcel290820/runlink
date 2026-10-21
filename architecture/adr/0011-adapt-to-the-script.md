# ADR 0011: Runlink adapts to the script

## Status

Accepted. Recorded 2026-10-21. Supersedes the refused variable names and the
owner-side limits of [ADR 0010](0010-input-passing.md), and the guided setup's
file naming.

## Context

Runlink is a generic tool: an owner shares a script as it is, and never changes
it, renames it, or makes it executable to suit Runlink. ADR 0010 still made some
scripts impossible to share: it refused variables such as `PATH` outright,
ordered positional arguments by form order, took one value per input, passed
booleans only as `true`, `false` or a lone flag, and capped timeouts at an hour
and uploads at 16 MiB. The setup also named a script's file after its name
without the extension, so `greet.sh` and `greet.py` shared one file, and the
second script could not be set up at all.

## Decision

| Need | Decision |
| --- | --- |
| A guard that blocks a real script | Guards stay the default; `"unsafe": true` on an input lets it set any variable and start an argument with `-`. The setup asks before marking an input unsafe, and the summary and every share show which inputs are, in one line |
| Positional arguments in any order | The setup asks where each one goes among the others and edits the command in place, keeping arguments it did not make; `c` edits the command line itself |
| Several values | `multiple` gives a list field; an argument holding the input repeats per value, unless `join` makes one text; variables and stdin get a line each, the JSON a list |
| Booleans the script spells its own way | `on` and `off` set the text for each state; `off` gives a flag another for off, as `--no-color` |
| Fixed variables | A task-level `env` sets variables for every run |
| A script without the executable bit | It runs with its `#!` program, or the usual one for its extension |
| One file per script | The file is `<script>.runlink.json`, after the whole name; `sha256` records the script's hash so a copy or a renamed script finds the file, which `share` offers only by asking |
| Owner-side limits | Timeouts and upload sizes take whatever the owner sets; the defaults stay 60 seconds and 16 MiB |

## Consequences

More scripts share unchanged, and nothing an owner needs is refused outright.
An unsafe input can let recipients run other code on the owner's machine; the
owner chooses that per input and sees it whenever the task is shared. Limits
that protect recipients' browsers, such as the 1 MB result, stay as they are.
Counted flags such as `-vvv`, two lists in one argument, scripts that need a
terminal, and a script moved to another folder without its file remain open.
