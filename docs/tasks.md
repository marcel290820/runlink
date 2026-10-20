# Sharing a task

`runlink share` turns a script on your machine into a link. Recipients open it in
a browser, fill in a form, run the script on your machine, and get its output.
Sharing lasts while the command runs; Ctrl-C stops it, including a run in progress.

```bash
scripts/dev.sh                                  # terminal 1: the local stack
build/runlink share examples/greet.json         # terminal 2: prints the link
build/runlink share --separate-secret examples/greet.json
build/runlink share examples/sort.json          # a file in, a file out
build/runlink share ./backup.sh                 # an executable without inputs
```

The link is `{service}/{uuid}#{secret}`. Anyone holding it can run the task, and
the secret can stay in their browser history. `--separate-secret` prints the link
and the secret apart, so you can send the secret another way; the recipient pastes
it into the page. Each `share` creates a new link; an old one stops working when
its command ends. In a terminal, `share` also copies the link (without the secret
under `--separate-secret`) with `pbcopy`, `wl-copy`, `xclip` or `xsel`, or else
through the terminal's OSC 52 clipboard, which also works over SSH; each Enter
opens the full link in your default browser. The terminal shows connections, runs, and the script's stderr,
with control characters escaped because stderr can echo recipient input.

## Task file

A task file is one JSON object, at most 64 KiB, with only these fields:

```json
{
  "title": "Greet someone",
  "description": "Writes a greeting on the owner's machine.",
  "owner": "Dana",
  "command": ["./greet.sh", "{name}", "--tone={tone}"],
  "inputs": [
    { "name": "name", "type": "text", "label": "Name", "required": true, "maxLength": 60 },
    { "name": "tone", "type": "choice", "options": ["warm", "formal"], "default": "warm" }
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
| `inputs` | Form fields, as [gui.js](../internal/frontend/assets/gui.js) documents: `text`, `multiline`, `number`, `date`, `boolean`, `choice`, `file` |
| `output` | `kind` `text` or `json` shows stdout; `file` offers stdout as a download named `name`; without `output`, recipients see only that the run finished |
| `timeout` | Seconds from accepting a run, including receiving its files, until the run and every process it started are killed; default 60, at most 3600 |
| `progress` | Show each stderr line to recipients while the task runs; off by default. A recipient whose connection cannot keep up misses lines instead of slowing the task |

Sharing an executable directly makes a task without inputs that shows stdout as text.

## Input

The runner checks every value against its field before running anything: type,
`required`, `maxLength`, `min`, `max`, `step`, dates, choice options, and file
names and sizes. A problem shows on the recipient's field and nothing runs.

Each value reaches the script two ways:

- `{name}` anywhere in a `command` argument is replaced by the value: numbers in
  plain decimal, booleans as `true` or `false`, an absent optional value as empty
  text. Each argument stays one argument, and replacement happens once, so a value
  containing braces stays literal. A text value filling a whole argument may not
  start with `-`, so it cannot become an option; write `--name={name}` to allow it.
- stdin carries all values as one JSON object, absent optional values left out.

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
