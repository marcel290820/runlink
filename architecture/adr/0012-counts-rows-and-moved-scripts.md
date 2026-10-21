# ADR 0012: Counted flags, values in rows, and moved scripts

## Status

Accepted. Recorded 2026-10-21. Closes three of the cases
[ADR 0011](0011-adapt-to-the-script.md) left open; a script that needs a
terminal for its input stays open.

## Context

ADR 0011 still left scripts that Runlink could not share as they are: one that
counts a flag, as `-v -v` or `-vvv` for more output; one that takes values in
sets, as `-p 8080:80 -p 8443:443`, where two lists share an argument; and one
moved to another folder, which lost its answers because its task file stayed
behind. An option also went only before a whole `{name}`, so `-o {name}.txt`
kept `-o` when the value was absent, and the setup could not write an argument
around a value.

## Decision

| Need | Decision |
| --- | --- |
| A counted flag | A `number` input with `"count": true` passes its `flag` once per one, as `-v -v -v`; `"stack": true` writes a one-letter flag as one argument, `-vvv`. A count needs `max`, so a recipient cannot make the owner's machine build a huge command; `min` defaults to 0 and `step` to 1 |
| Values in sets | An argument holding two or more inputs of several values repeats once per row, taking each input's value at that row. The runner refuses rows that are not complete, and the definition's `pairs` tells the page to show those inputs as one field of rows. Files stay out of rows |
| An option before any argument | `flag` passes before, or joined by `=` to, every argument that holds the input, as `-p {host}:{port}` or `-o {name}.txt`, and goes with the argument when its value is absent |
| Writing such an argument | The setup's "inside an argument you write" asks for the argument, other inputs' `{names}` included, and an optional option before it; without an option it is positional and takes its place among the others |
| A script moved without its file | Each share notes the script's task file and the script's SHA-256 in `runlink/scripts.json` in the user's config directory. A script without a file is offered, by asking, a noted file whose script had the same contents: gone, as after a move, or still there, as for a copy. A file that a moved script takes is no longer offered to others |

## Consequences

Counting, sets of values, and moved scripts work without changing the script.
The notes are a local index of the owner's task files and script hashes,
private to the owner; deleting them only loses the matching. A script edited
after it moved and before its next share no longer matches by contents, so its
owner answers the questions again or copies the file. A script that needs a
terminal for its input, rather than stdin, stays open.
