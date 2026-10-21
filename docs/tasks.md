# Sharing a task

`runlink share` turns a script on your machine into a link. Recipients open it in
a browser, fill in a form, run the script on your machine, and get its output.
Sharing lasts while the command runs; Ctrl-C stops it, including a run in progress.

```bash
scripts/dev.sh                                  # terminal 1: the local stack
build/runlink share examples/greet.json         # terminal 2: prints the link
build/runlink share --separate-secret examples/greet.json
build/runlink share examples/sort.json          # a file in, a file out
build/runlink share ./backup.sh                 # a script: set up its form once
build/runlink share --edit ./backup.sh          # change the script's answers
```

The link is `{service}/{uuid}#{secret}`. Anyone holding it can run the task, and
the secret can stay in their browser history. `--separate-secret` prints the link
and the secret apart, so you can send the secret another way; the recipient pastes
it into the page. Each `share` creates a new link; an old one stops working when
its command ends. In a terminal, `share` also copies the link (without the secret
under `--separate-secret`) with `pbcopy`, `wl-copy`, `xclip` or `xsel`, or else
through the terminal's OSC 52 clipboard, which also works over SSH; each Enter
opens the full link in your default browser.

The terminal shows the task, the link, and below them a status that redraws in
place: how many recipients are connected and the latest run, running, finished, or
failed with its duration. Earlier runs stay above as one line each. Every line a
run writes to stderr goes above the status as it arrives, indented under a line
marking the run's start, so the terminal's scrollback keeps all of it. Stderr is
shown with control characters escaped, because it can echo recipient input. Color marks state and nothing else;
`NO_COLOR` turns it off, a locale that is not UTF-8 gets ASCII symbols, and
`TERM=dumb` or redirected output gets plain lines: the link on stdout and one
timestamped line per connection, run, and stderr line on stderr, so a log keeps
every stderr line.

### Sharing a script

The first time you share a script from a terminal, `share` asks what recipients
see and saves the answers beside the script as `<name>.runlink.json`: `backup.sh`
gets `backup.runlink.json`, leaving `backup.json` free. Every question has a
suggestion, so pressing Enter through them shares a task without inputs that
shows stdout as text. The questions follow the task file below: title,
description, owner, then inputs one at a time (label, name, type, options for a
choice, whether it is required, and how the script takes it, as
[Input](#input) lists), the output's kind and label, the timeout, and progress.
Lists take the arrow keys or an option's first letter; a wrong answer is
explained under the question until you fix it. Before anything is saved, `share`
shows the form as recipients will see it and how the script will get the values:
the command, with options before positional arguments and in brackets what a run
can leave out, and what goes to stdin. Enter saves and shares, `e` goes through
the answers again, and Ctrl-C at any point stops without writing anything.

Later shares load the saved file without asking. `--edit` asks again with the
saved answers as suggestions and keeps the fields it does not ask about, such as
`help`, `placeholder` and bounds; editing the file by hand works as well. A
command written by hand stays as written; one the setup made follows the inputs.
A saved file that does not load, or whose command does not run the script, stops
`share` with the file's path and the problem, and `share` never asks over it.
The file is written whole or not at all, readable only by you when new, and
only `--edit` replaces one, keeping its permissions. Without
a terminal, a script without a saved file shares without input, and nothing is
asked or written. Name a task file directly, as in `runlink share backup.json`,
and it is shared as it is, without questions.

## Task file

A task file is one JSON object, at most 64 KiB, with only these fields:

```json
{
  "title": "Greet someone",
  "description": "Writes a greeting on the owner's machine.",
  "owner": "Dana",
  "command": ["./greet.sh", "{tone}", "{loud}", "{name}"],
  "inputs": [
    { "name": "name", "type": "text", "label": "Name", "required": true, "maxLength": 60 },
    { "name": "tone", "type": "choice", "options": ["warm", "formal"], "default": "warm", "flag": "--tone" },
    { "name": "loud", "type": "boolean", "flag": "--loud" },
    { "name": "sign", "type": "text", "label": "Signed by", "env": "SIGNATURE" }
  ],
  "output": { "kind": "text", "label": "a greeting" },
  "timeout": 10,
  "progress": true
}
```

| Field | Meaning |
| --- | --- |
| `title`, `description`, `owner` | What recipients see; `owner` makes the page say "Dana's machine" |
| `command` | The program and its arguments, run without a shell from the task file's directory; a relative program path starts there too. Only arguments take input; the program is fixed |
| `inputs` | Form fields, as [gui.js](../internal/frontend/assets/gui.js) documents: `text`, `multiline`, `number`, `date`, `boolean`, `choice`, `file`; `flag`, `env` and `stdin` say how a value reaches the script, as [Input](#input) explains, and recipients never see them |
| `output` | `kind` `text` or `json` shows stdout; `file` offers stdout as a download named `name`; without `output`, recipients see only that the run finished |
| `timeout` | Seconds from accepting a run, including receiving its files, until the run and every process it started are killed; default 60, at most 3600 |
| `progress` | Show each stderr line to recipients while the task runs; off by default. A recipient whose connection cannot keep up misses lines instead of slowing the task |

Sharing an executable loads its saved task file, `<name>.runlink.json`, which
the setup writes in this format and a command running the executable; without
one, it makes a task without inputs that shows stdout as text.

## Input

The runner checks every value against its field before running anything: type,
`required`, `maxLength`, `min`, `max`, `step`, dates, choice options, and file
names and sizes. A problem shows on the recipient's field and nothing runs.

Each value reaches the script the way the script takes it:

| The script reads | The task file says | A run passes |
| --- | --- | --- |
| A positional argument, as `$1` | `"{name}"` in `command` | `Ada` |
| An option and its value | `"{name}"` in `command`, and `"flag": "--name"` | `--name Ada` |
| An option joined to its value | `"flag": "--name="`, or `"--name={name}"` in `command` | `--name=Ada` |
| A flag | a boolean with `"flag": "--loud"` | `--loud` when on, nothing when off |
| Part of an argument | `"out-{day}.csv"` in `command` | `out-2026-10-21.csv` |
| An environment variable | `"env": "NAME"` | `NAME=Ada` for the run |
| A line of stdin, as a prompt reads it | `"stdin": true` | `Ada` and a newline |
| A file on stdin | a file with `"stdin": true` | the file's contents |
| JSON on stdin | nothing more | `{"name":"Ada"}`, when no input has `stdin` |

- `{name}` is replaced once in each argument, so a value containing braces stays
  literal: numbers in plain decimal, booleans as `true` or `false`, a file as the
  path below. Arguments run in the order `command` lists them; the setup puts
  options first, as `getopts` and Go's `flag` package need.
- An absent optional value leaves its argument out, with its flag. A lone
  `{name}` without a flag stays as empty text while a later positional argument
  is passed, so that one keeps its place: `./greet.sh "" Ada`. A variable is unset, even when
  your shell sets it, and a stdin line is empty.
- A text value that starts an argument, alone, after a separate flag, or before
  more text as in `{name}.txt`, may not start with `-`, so it cannot become an
  option. A flag ending in `=`, or a `--` argument before it in `command`,
  allows it. A run that would still start an argument with `-` from a value
  fails without running.
- Inputs with `stdin` go one line each, in order; a value already ending in a
  newline gets no other. A file takes stdin alone. Without `stdin`, stdin carries
  all values as one JSON object, absent optional values left out; with it, every
  other input must reach the script through `command` or `env`.
- A variable is recipient text in every program the script starts, so name one
  only the script reads. Names that change how programs start, such as `PATH`,
  `IFS`, `BASH_ENV`, `NODE_OPTIONS`, `PYTHONPATH` and those starting with `LD_`
  or `DYLD_`, are refused.

Not covered yet: several values for one input, such as a list of files or a
repeated option, which needs a list field on the page; a flag that becomes
`--no-name` when off; and fixed variables for every run, which the script can set.

A file input's value is the path of the recipient's file, stored under its own
name in a new private directory and deleted with it when the run ends. `maxBytes`
limits its size, at most and by default 16 MiB. `accept` only filters the
recipient's file picker: the script gets whatever bytes the recipient sends.

## Limits

One run at a time per task, up to 16 recipients connected at once, input up to
64 KB plus files up to 16 MiB each, stdout up to 1 MB per run, and at most 500
progress lines per run. A
non-zero exit status shows as a failure without detail; your terminal has the
script's stderr. A run ends when its command exits: anything it started in the
background is killed then. A recipient who disconnects mid-run does not stop it; one
whose browser stops receiving for 15 seconds is disconnected. Run recovery, process supervision across
runner crashes, and expiry and revocation are later steps of the
[implementation plan](../architecture/implementation.md).
