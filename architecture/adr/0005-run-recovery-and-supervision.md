# ADR 0005: Durable run recovery and crash-surviving supervision

## Status

Accepted. Recorded 2026-10-02.

## Context

A connection can fail after a task changes external resources but before the browser receives acknowledgment. Reloading loses in-memory run IDs, and killing a runner can leave its child processes alive. Retrying with a new ID or releasing a concurrency slot can then repeat or overlap side effects.

## Decision

### Recovery handle and acceptance

Before sending a run request, the browser generates a run ID and commits a nonsecret handle containing the task UUID and run ID to persistent browser storage. Store no secret, input, output, or credential there. A handle grants no access; querying it requires authentication and current authorization for that task. Keep unresolved handles across reloads and tab closure, including separate submissions from multiple tabs. Bound storage and refuse submission if a handle cannot be saved; never silently overwrite an unresolved handle. Remove a handle after the recipient acknowledges its outcome or explicitly discards recovery; removal never starts another run.

On reconnect, reload, or reopening a shared task URL, authenticate and query saved runs before offering fresh execution. A missing response, unknown/expired record, or lost browser storage is not evidence that a run never executed. Show the uncertainty; never automatically resubmit or mint a replacement ID. A recipient can explicitly start a new run after seeing that it may repeat effects. Reauthentication follows [ADR 0003](0003-trusted-browser-client.md#recipient-secrets).

The runner authorizes and validates the complete request, then durably records acceptance and its input identity before acknowledgment or execution. Scope IDs to the task. Concurrent or repeated submissions with the same ID return existing state; different input for that ID is rejected. Retain deduplication metadata for the full period in which an ID can be submitted, and reject expired IDs even after result files are removed. Bound retained records and reject new runs at capacity rather than discard required recovery state.

### Process supervision

Use a separate local supervisor process, launched from the same owner binary, to own each run's process group, deadline, bounded output, and stop operation. Its lifetime must not depend on the runner's connection process. This preserves one distributed binary and requires no cloud scheduler.

Before starting the task, persist its supervision identity and deadline and establish a recoverable handoff. A crash between acceptance and handoff leaves an unstarted/unknown record, never an automatic retry. The supervisor continues enforcing limits after abrupt runner death and exposes an owner-only local stop/reconnect path. A raw PID or process-group number is insufficient identity because it can be reused.

At startup, reconcile every unfinished run with its supervisor and process group before admitting new work for the affected task. Reattach to live supervision or terminate verified remaining processes. Keep concurrency slots occupied until all prior processes are accounted for and exit is confirmed, including descendants after the command leader exits. If supervision or process identity is uncertain, block the task for owner reconciliation; marking a record interrupted does not release its slot. Prove the handoff and process identity mechanism on both macOS and Linux before running real tasks.

### Failure behavior

| Event | Required behavior |
| --- | --- |
| Browser or network disconnect | Show unconfirmed/disconnected; supervision continues within limits; reconnect queries the saved run ID |
| Runner crashes | Supervisor retains deadline and stop control; restart reconciles processes and records; never automatically repeat possible side effects |
| Supervisor unavailable or host reboot | Reconcile remaining processes before releasing slots; report interrupted/unknown unless durable evidence establishes completion |
| Owner stops a run or deadline expires | Terminate the supervised group, escalate to forced termination if needed, and confirm exit before releasing its slot |
| Result/file transfer fails | Keep buffers, storage, and retention bounded; verify complete length and digest; retry transfer without rerunning the task |
| Task expires or is revoked | Deny new runs and data access, including recovery through existing sessions; owner separately controls stopping active runs |
| Owner offline | Report unavailable; do not queue execution |
| Application server restarts | Established peer sessions continue; new connections and recovery wait for registry restoration and owner reauthentication |

## Consequences

Transport receipt, durable acceptance, process exit, and result receipt are distinct. Runlink does not promise exactly-once external side effects or rollback. Recovery requires the original browser's saved handle and valid task authorization; lost storage or expired records limit what can be confirmed. Unsafe uncertainty blocks execution rather than guessing.

This adds a small local supervision and recovery contract. It does not make arbitrary detached or hostile scripts safe; [ADR 0001](0001-owner-local-tasks.md) restricts supported tasks. Numeric retention limits and platform mechanisms remain in the [implementation plan](../implementation.md#open-items).
