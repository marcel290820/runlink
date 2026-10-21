# ADR 0010: Inputs reach a script the way it takes them

## Status

Accepted. Recorded 2026-10-21. Supersedes the "typed fields mapped to arguments
or JSON stdin" row of [ADR 0001](0001-owner-local-tasks.md). [ADR 0011](0011-adapt-to-the-script.md)
lets owners pass refused variables, adds several values, boolean texts and
fixed variables, and lifts the owner-side limits.

## Context

Runlink should share existing scripts without changing them. Scripts take input
in several common ways: positional arguments, options with separate or joined
values, flags, environment variables, lines or files on stdin, and JSON. Arguments
and JSON stdin alone forced a script to adopt one convention, and an absent
optional value became an empty argument, which most option parsers reject.

## Decision

The task file's `command` stays an explicit argument list with `{name}`
placeholders, run without a shell. Each input may add how it is passed:

| Field | Passes |
| --- | --- |
| `flag` | A lone `{name}` as the flag and its value, joined when the flag ends in `=`; a boolean as the flag alone when on |
| `env` | An environment variable for the run |
| `stdin` | The value as a line of stdin, or a file's contents |

An absent value leaves out its argument, flag, and variable; an empty positional
argument stays only to keep a later one's place. Without `stdin`, stdin carries
the JSON of all values, as before. Recipients never receive these fields. The
guided setup asks how each input is passed.

| Owner declares | Runner enforces |
| --- | --- |
| Typed fields passed as arguments, options, flags, variables, stdin, or JSON stdin | Values are validated first; the program stays fixed; no value can start an argument with `-` unless a joining flag or `--` makes that safe; variables that control shells, interpreters, or the loader, such as `PATH`, `BASH_ENV`, `LD_*`, and `DYLD_*`, are refused |

## Consequences

Most scripts can be shared as they are. Recipient text can now reach every
program a script starts through a variable, so the owner must name one only the
script reads; the refused names are a guard, not a complete list. Several values
for one input, negated flags, and fixed variables stay unsupported until a task
needs them.
