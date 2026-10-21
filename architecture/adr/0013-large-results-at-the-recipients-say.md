# ADR 0013: Results past 1 MiB at the recipient's say

## Status

Accepted. Recorded 2026-10-21. Raises the output bound of
[ADR 0009](0009-poc-signaling-and-peer-authentication.md#data-channel).

## Context

ADR 0009 bounds a run's output at 1 MiB on both sides: the runner stops the
script past it, and the browser ends a connection that sends more. The bound
protects the recipient's browser, which holds the whole result in memory, from
an owner's machine that sends without end. It also refuses results that are
reasonable today, such as a report, an archive, or a log of tens of megabytes.

## Decision

- The runner keeps a run's stdout, up to 100 MiB, in a temporary file it has
  already unlinked, so the output goes when its delivery ends, also after a
  crash. Past 100 MiB the run fails, as runs past 1 MiB did.
- After a successful run, the runner sends `{size}`, the output's length,
  before its bytes; without `output`, the size is 0 and no bytes follow.
- Up to 1 MiB, the bytes follow at once. Past it, the runner waits for
  `{receive: true}` or `{receive: false}`. The loader asks the GUI, which asks
  the recipient with the size; a GUI without the question declines. Declined,
  the runner sends `{done: true}` without the bytes and tells the owner.
- The loader takes at most 1 MiB of output unless the recipient agreed to a
  size, counting messages as ADR 0009 does, and an announced result must
  arrive after its size and exactly, neither a byte more nor less. It refuses a size past
  100 MiB, and a connection lost while the recipient decides ends the run. A result past 1 MiB downloads as a file instead
  of showing as text or data, and the page shows how much has arrived.

## Consequences

Large results work, and no recipient receives more than 1 MiB without saying
so; an owner's machine still cannot make a page hold more than the recipient
agreed to. An unanswered question holds the output on the owner's disk, at most
100 MiB for each of the 16 sessions, until the recipient answers or leaves. A
100 MiB result over a TURN relay limited to 1 MiB/s takes about two minutes.
Results past 100 MiB, and a result streamed while the script writes it, stay
open.
