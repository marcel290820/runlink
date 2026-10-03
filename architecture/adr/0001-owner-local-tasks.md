# ADR 0001: Owner-local tasks and a single owner binary

## Status

Accepted. Recorded 2026-10-02.

## Context

An owner already has a useful script and its working environment. Sharing it should require little more than defining the inputs, actions, and outputs a recipient may access. Recipients need only a browser.

## Decision

Execute bounded tasks on the owner's machine. Ship one `runlink` binary for macOS and Linux; defer Windows. Users may configure `rl` as a shell alias, but documentation and scripts use `runlink`. Support trusted scripts in any installed language, without an SDK or per-language discovery.

| Area | Choice | Reason |
| --- | --- | --- |
| CLI and application server | Go 1.27.1; one module, two executables; standard library first | Network I/O and binary distribution |
| Browser | HTML, CSS, vanilla JavaScript; CLI embeds GUI with `go:embed` | Fixed task interaction without a frontend framework/runtime |
| Peer transport | Browser WebRTC and Pion WebRTC v4 | Native data channels and NAT traversal; [ADR 0002](0002-webrtc-transport.md) |
| Signaling | HTTPS/WSS; `net/http` and `coder/websocket` | Small control messages and outbound owner connections |
| Execution | `os/exec`, explicit arguments, independent process supervision | Reuse existing scripts while enforcing [ADR 0005](0005-run-recovery-and-supervision.md) |
| Initial storage | Local files and one application-server instance | Bounded state without an external database or queue |

Pin dependencies at bootstrap. Keep `cmd/runlink` and `cmd/runlink-server` in one Go module, with shared implementation in `internal/`.

Share no-input commands directly. Parameterized tasks use `task.json`; its exact syntax remains an [implementation choice](../implementation.md#open-items).

| Owner declares | Runner enforces |
| --- | --- |
| Fixed executable, arguments, working directory | Launch only that command; no shell interpolation of recipient input |
| Typed fields mapped to arguments or JSON stdin | Validate values, sizes, and allowed argument/path semantics |
| Public outputs and metadata | Return only declared text/JSON, files, progress, and resource usage; diagnostics private by default |
| Execution policy | Deadlines, bounded input/output, rate limits, default one concurrent run per task |
| Access policy | Expiry and revocation checked locally on every invocation and result request, including existing sessions |

Owners can stop active tasks locally. Cancellation covers the supervised process group and does not roll back external side effects. Scripts must remain within supervision; detached daemons and hostile-code sandboxing are outside v1.

## Consequences

Tasks reuse the owner's tools and credentials, so owners must restrict exposed actions and credential scope. Owner availability is required; requests are not queued for later execution. Cloud execution, bespoke task interfaces, multi-step recipient workflows, and a distributed scheduler remain outside the product scope.
