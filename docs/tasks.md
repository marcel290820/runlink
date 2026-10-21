# Sharing a task

`runlink share` turns a script on your machine into a link. Recipients open it in
a browser, fill in a form, run the script on your machine, and get its output.
Sharing lasts while the command runs; Ctrl-C stops it, including a run in progress.

```bash
scripts/dev.sh                                  # terminal 1: the local stack
build/runlink share examples/greet.json         # terminal 2: prints the link
build/runlink share --separate-secret examples/greet.json
build/runlink share examples/sort.json          # files in, a file out
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

Runlink takes a script as it is: whatever order, style, or mix of arguments,
options, flags, variables, and stdin it reads, the setup records how. The first
time you share a script from a terminal, `share` asks what recipients see and
how the script takes each value, and saves the answers beside the script as
`<script>.runlink.json`, named after all of the script's name: `backup.sh` gets
`backup.sh.runlink.json`, and `backup.py` its own `backup.py.runlink.json`.
Every question has a suggestion, so pressing Enter through them shares a task
without inputs that shows stdout as text. The questions follow the task file
below: title, description, owner, then inputs one at a time (label, name, type,
options for a choice, whether it takes several values, whether it is required,
and how the script takes it, as [Input](#input) lists: where a positional
argument goes among the others, an option's spelling, a counted flag and how
it is written, an argument you write around the value with an option before
it, what a boolean passes when on and when off, how several values are
joined), then variables fixed for every
run, the output's kind and label, the timeout, and progress. Lists take the
arrow keys or an option's first letter; a wrong answer is explained under the
question until you fix it. Before anything is saved, `share` shows the form as
recipients will see it and how the script will get the values: the command,
with in brackets what a run can leave out and `...` after what repeats per
value, and what goes to stdin. Enter saves and shares, `e` goes through the
answers again, `c` edits the command line itself, for anything the questions
do not cover, such as fixed arguments or a program to run the script with, and
Ctrl-C at any point stops without writing anything. A form or command that
would not load or run says why under the summary, and Enter saves nothing
until it is fixed.

A script without the executable bit runs with the program its `#!` line names,
or else the usual one for its extension, such as `python3` for `.py`, `node` for
`.js` and `sh` for `.sh`; the summary shows it, and `c` changes it.

Later shares load the saved file without asking, even after the script changes.
`--edit` asks again with the saved answers as suggestions and keeps the fields
it does not ask about, such as `help`, `placeholder` and bounds; editing the file
by hand works as well. The setup keeps the command's other arguments as they
are and moves only the placeholders its answers change. A saved file that does
not load stops `share` with the file's path and the problem, and `share` never
asks over it. The file is written whole or not at all, readable only by you when
new, and only `--edit` replaces one, keeping its permissions. Without a
terminal, a script without a saved file shares without input, and nothing is
asked or written. Name a task file directly, as in `runlink share backup.json`,
and it is shared as it is, without questions.

A script without a file of its own may still have one under another name or in
another folder. The setup records the script's SHA-256 in `sha256`, and each
share notes the script's file with the script's SHA-256 at that share in
`runlink/scripts.json` in your config directory (`~/.config` on Linux,
`~/Library/Application Support` on macOS). So `share` finds a file saved for a
script with the same contents anywhere, whether that script is gone, as after
a move or a rename, or still there, as for a copy, and, beside the script, a
file for one that is gone. It never uses one without asking: "Start from"
lists them, the likeliest first, with `new` for answers of its own, since a
copy may want a different form. Picking one goes through the questions with
its answers as suggestions, runs the new script in the other's place in its
command, with its interpreter when it is not executable, and saves a file of
the script's own; the other file stays as it was, and once a moved script
takes it, it is not offered again. A command that names no script there is
shown for you to check, and `c` changes it. A script edited after it moved and
before its next share no longer matches, and starts afresh. A notes file that
cannot be read is reported each time, and sharing goes on.

## Task file

A task file is one JSON object, at most 64 KiB, with only these fields:

```json
{
  "title": "Greet someone",
  "description": "Writes a greeting on the owner's machine.",
  "owner": "Dana",
  "command": ["./greet.sh", "{tone}", "{loud}", "{name}"],
  "env": { "LANG": "C" },
  "inputs": [
    { "name": "name", "type": "text", "label": "Names", "required": true, "multiple": true, "maxLength": 60 },
    { "name": "tone", "type": "choice", "options": ["warm", "formal"], "default": "warm", "flag": "--tone" },
    { "name": "loud", "type": "boolean", "flag": "--loud", "off": "--quiet" },
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
| `env` | Variables set for every run, as `{"LANG": "C"}`, over the ones your shell gives; recipients never see them |
| `inputs` | Form fields, as [gui.js](../internal/frontend/assets/gui.js) documents: `text`, `multiline`, `number`, `date`, `boolean`, `choice`, `file`, and `multiple` for several values; `flag`, `env`, `stdin`, `on`, `off`, `join`, `count`, `stack` and `unsafe` say how a value reaches the script, as [Input](#input) explains, and recipients never see them |
| `output` | `kind` `text` or `json` shows stdout; `file` offers stdout as a download named `name`; without `output`, recipients see only that the run finished |
| `timeout` | Seconds from accepting a run, including receiving its files, until the run and every process it started are killed; default 60, as long as you need |
| `progress` | Show each stderr line to recipients while the task runs; off by default. A recipient whose connection cannot keep up misses lines instead of slowing the task |
| `sha256` | The script's SHA-256 when the setup last saved the file, so a copy or a renamed script can find it |

Sharing an executable loads its saved task file, `<script>.runlink.json`, which
the setup writes in this format; without one, it makes a task without inputs
that shows stdout as text.

## Input

The runner checks every value against its field before running anything: type,
`required`, `maxLength`, `min`, `max`, `step`, dates, choice options, and file
names and sizes. A problem shows on the recipient's field and nothing runs.

Each value reaches the script the way the script takes it:

| The script reads | The task file says | A run passes |
| --- | --- | --- |
| A positional argument, as `$1` | `"{name}"` in `command`, wherever it goes | `Ada` |
| An option and its value | `"{name}"` in `command`, and `"flag": "--name"` | `--name Ada` |
| An option joined to its value | `"flag": "--name="`, or `"--name={name}"` in `command` | `--name=Ada` |
| A flag | a boolean with `"flag": "--loud"` | `--loud` when on, nothing when off |
| A flag, and another for off | a boolean with `"flag": "--color", "off": "--no-color"` | `--color` or `--no-color` |
| Other texts for a boolean | `"on": "yes", "off": "no"`, as an argument, variable or stdin line | `yes` or `no`; an empty text passes nothing |
| Part of an argument | `"out-{day}.csv"` in `command` | `out-2026-10-21.csv` |
| An option before part of an argument | `"{name}.txt"` in `command`, and `"flag": "-o"` | `-o report.txt` |
| A flag counted, as `-vvv` | a number with `"flag": "-v", "count": true, "max": 3` | `-v -v -v` for 3; with `"stack": true`, `-vvv` |
| Several values, as `"$@"` | `"multiple": true` | `a b`, each its own argument; with a flag, `--tag a --tag b` |
| Several values as one | `"multiple": true, "join": ","` | `a,b` |
| Values in sets, as `-p 8080:80` | `"{host}:{port}"` in `command`, both `"multiple": true`, and `"flag": "-p"` on one | `-p a:80 -p b:443` |
| An environment variable | `"env": "NAME"` | `NAME=Ada` for the run |
| A fixed variable | `"env": {"MODE": "prod"}` beside `command` | `MODE=prod` for every run |
| A line of stdin, as a prompt reads it | `"stdin": true` | `Ada` and a newline |
| A file on stdin | a file with `"stdin": true` | the file's contents; several files one after another |
| JSON on stdin | nothing more | `{"name":"Ada"}`, when no input has `stdin` |

- `{name}` is replaced once in each argument, so a value containing braces stays
  literal: numbers in plain decimal, booleans as `true` or `false` unless `on`
  and `off` say otherwise, a file as the path below. Arguments run in the order
  `command` lists them; the setup asks where each positional argument goes and
  puts options before them, as `getopts` and Go's `flag` package need.
- An absent optional value leaves its argument out, with its flag. A lone
  `{name}` without a flag stays as empty text while a later positional argument
  is passed, so that one keeps its place: `./greet.sh "" Ada`. A variable is
  unset, even when your shell sets it, and a stdin line is empty.
- Several values arrive as a list in the form. An argument holding the input
  repeats once per value, flag included, unless `join` makes the values one
  text. An argument holding two or more such inputs repeats once per row,
  taking each one's value at that row: the page shows them as one field of
  rows, and a row filled in halfway is refused. Files cannot go in rows. A
  variable gets the values a line each, stdin a line each, and the JSON a list.
  `required` asks for at least one.
- A `flag` goes before every argument that holds its input, or joined to it
  when it ends in `=`, and is left out with the argument. One argument can take
  one input's flag. A boolean's flag and a count's are their own texts, so
  they take `{name}` as a whole argument.
- A count passes its flag once per one, from none, up to `max`, which it needs
  so that a recipient cannot build a huge command; its `min` is 0 and its
  `step` 1 unless set, and `stack` needs a one-letter flag.
- A text value that starts an argument, alone, after a separate flag, or before
  more text as in `{name}.txt`, may not start with `-`, so it cannot become an
  option. A flag ending in `=`, or a `--` argument before it in `command`,
  allows it. A run that would still start an argument with `-` from a value
  fails without running. When the script should take such values,
  `"unsafe": true` on the input lets them through; your terminal says when a
  value was refused, and the summary and every share say which inputs are unsafe.
- Inputs with `stdin` go one line each, in order; a value already ending in a
  newline gets no other. Files take stdin alone. Without `stdin`, stdin carries
  all values as one JSON object, absent optional values left out; with it, every
  other input must reach the script through `command` or `env`.
- A variable is recipient text in every program the script starts, so name one
  only the script reads. Names that change how programs start, such as `PATH`,
  `IFS`, `BASH_ENV`, `NODE_OPTIONS`, `PYTHONPATH` and those starting with `LD_`
  or `DYLD_`, let a recipient run other code, so an input sets one only with
  `"unsafe": true`; the setup asks before adding it. A fixed variable is your
  own text and can be any name.

Not covered yet: a script that needs a terminal for its input, rather than
stdin.

A file input's value is the path of the recipient's file, stored under its own
name in a new private directory and deleted with it when the run ends; several
files keep their own names even when they match. `maxBytes` limits each file's
size, by default 16 MiB, and can be as large as you allow. `accept` only filters
the recipient's file picker: the script gets whatever bytes the recipient sends.

## Limits

One run at a time per task, up to 16 recipients connected at once, input up to
64 KB plus files up to `maxBytes` each, stdout up to 100 MB per run, and at most
500 progress lines per run. A result past 1 MB arrives only when the recipient
agrees: the page asks with its size, shows how much has arrived, and offers it
as a download, and a declined one is noted in your terminal. Until the
recipient answers, the output waits in a deleted temporary file on your
machine. A
non-zero exit status shows as a failure without detail; your terminal has the
script's stderr. A run ends when its command exits: anything it started in the
background is killed then. A recipient who disconnects mid-run does not stop it; one
whose browser stops receiving for 15 seconds is disconnected. Run recovery, process supervision across
runner crashes, and expiry and revocation are later steps of the
[implementation plan](../architecture/implementation.md).
