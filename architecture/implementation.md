# Implementation plan

[Architecture overview and ADR index](README.md)

The [local foundation](../README.md) now supplies both executables, the loader/GUI asset boundary, shared checks, hosted CI configuration, three-server infrastructure, and signed release/deployment tooling. The local proof of concept adds signaling, TURN credentials, `runlink share`, WebRTC with peer authentication ([ADR 0009](adr/0009-poc-signaling-and-peer-authentication.md)), and one task run end to end. See [verification](../docs/verification.md) for executed checks and limits. The durable registry, recovery, and supervision remain planned. Complete each proof before broadening scope. ADRs record the design; this page tracks the work needed to deliver it.

[ADR 0008](adr/0008-local-first-proof-of-concept.md) puts a local proof of concept first: the whole product runs on one machine on the [local stack](../docs/development.md#local-stack), and the [external setup](../docs/operations.md#external-setup-in-order) waits until the idea is validated.

## Build sequence

| Step | Deliverable | Exit condition |
| --- | --- | --- |
| 1. Local proof of concept | On the local stack: bounded registration, signaling, TURN credential issuance, direct and relayed WebRTC, authentication, one existing script with typed input, verified owner-served GUI, progress and result | A browser on the owner's machine completes a shared task in both sharing modes, over direct and forced-relay paths. End-to-end tests drive the CLI, stack, and Chromium. UUID-only access, replay, and substituted peers fail. Established sessions survive B shutdown |
| 2. Transport across networks | The deferred external setup; direct/TURN paths between machines | Two machines connect across representative networks in both sharing modes; both peers can relay through C over TCP/TLS. Measure connection time, relay usage, and cost |
| 3. One useful task with recovery | Durable handles and acceptance, independent supervision | Coworker completes a task unaided. Authorization, timeout/size/concurrency limits, deduplication, reload recovery, and abrupt runner-death checks already pass on macOS and Linux |
| 4. Failure and exposure controls | File recovery, expiry/revocation, retention, cancellation, registry restore/cleanup | Complete the failure checks below; downloads retry without rerunning tasks; capacity and cleanup preserve existing ownership and recovery |
| 5. Delivery | Restricted releases, platform binaries, clear errors, polished GUI | Verify production deployment isolation; independently review authentication; critique rendered UI; pilot publication of real scripts |

## Local proof of concept

The local stack supplies the frontend, app, and a TURN stand-in on loopback, the
owner CLI's service origin, and in-process stacks for tests. The proof of concept
added product code in this order, each with its local check, all passing:

| Work | Where it plugs in | Local proof |
| --- | --- | --- |
| Registration and signaling | Two WebSocket routes on B; A terminates and relays them with the [ingress checks](adr/0003-trusted-browser-client.md#ingress-and-executable-content) | Go tests through A and B: registration, offer and answer, declines, owner loss, capacity, shutdown, and a hostile B confined to small JSON |
| TURN credential issuance | B reads the shared secret file and TURN URIs from its configuration; `devstack` passes `var/dev/turn-secret` and the stand-in's URIs | Issued credentials verify independently; Chromium allocates relays with them; expired or forged ones fail |
| Sharing from the CLI | `runlink share` resolves its origin as `runlink status` does and prints `{origin}/{uuid}#{secret}`; a script's first share at a terminal asks for its form and how the script takes each input, including each positional argument's place, several values, boolean texts, fixed variables, and unsafe inputs, and stores both as `<script>.runlink.json` with the script's SHA-256, which later shares load and copies or renamed scripts are offered by asking ([ADR 0011](adr/0011-adapt-to-the-script.md)); a status region redraws in place, with plain lines when there is no terminal | The printed link opens the task in Chromium; through a pseudo-terminal, the setup writes a file the loader accepts and the next share gives the same form without asking, runs pass each input as the setup's answer says, `--edit` keeps unasked fields and leaves an unchanged file byte for byte, Ctrl-C writes nothing, broken stored files are reported, scripts of the same name keep apart files, and `NO_COLOR` and `TERM=dumb` output stays complete |
| WebRTC and authentication | Pion in the runner, `RTCPeerConnection` in the loader, ICE servers from B | Chromium end to end over direct and `iceTransportPolicy: 'relay'` paths; Go tests refuse wrong secrets, other tasks, relayed peers, and replayed proofs |
| One task | Runner validates input, receives files, and executes the fixed command; the loader verifies the owner-served GUI | Chromium completes [examples/greet.json](../examples/greet.json) with progress and result and [examples/sort.json](../examples/sort.json) with a file in and out; Go tests cover validation, uploads, timeouts, oversized output, and stopping |

Step 1's exit condition is met locally except the [durable registry](adr/0004-bounded-task-registry.md):
both sharing modes, direct and forced-relay paths, UUID-only access, replay, and
substituted peers are tested, and an established session keeps running after A, B,
and the TURN stand-in stop.

## Required proofs

These are acceptance criteria for future implementation, not tests that have already passed. Under [ADR 0008](adr/0008-local-first-proof-of-concept.md), each can first be proven on the local stack except TURN fallback across real networks and production deployment isolation.

| Contract | Proof |
| --- | --- |
| [Ingress isolation](adr/0003-trusted-browser-client.md#ingress-and-executable-content) | Replace B with malicious responses: HTML/SVG/JS bodies, executable MIME types, redirects, weakened headers, and `Service-Worker-Allowed: /`, including errors and failed upgrades. Through A, direct navigation, script loading, and service-worker registration cannot execute B's code or control task pages; B cannot modify releases or routing |
| [Fragment handling](adr/0003-trusted-browser-client.md#recipient-secrets) | Fragment disappears before setup; no recipient secret reaches history state, storage, signaling, telemetry, or logs. Reload/session restoration requires reauthentication. One-click sharing explains the residual history/sync risk |
| [Browser recovery](adr/0005-run-recovery-and-supervision.md#recovery-handle-and-acceptance) | Reload, close, or discard before and after acceptance/acknowledgment; reopen the task URL and authenticate to query the same run. Test storage failure, multiple tabs, missing/expired records, same-ID input mismatch, and authentication failure. No automatic second execution |
| [Process supervision](adr/0005-run-recovery-and-supervision.md#process-supervision) | Kill the runner abruptly, including with `SIGKILL`, during handoff and execution while a script has a live child. Deadline and owner stop still work. Restart cannot admit a conflicting run until the group is reconciled; test supervisor loss, PID reuse, and host reboot with unknown outcomes |
| [TURN fallback](adr/0006-hetzner-deployment.md#turn-destination-policy) | Exercise direct, TURN UDP, and TURN TCP/TLS paths. Force both peers through the same C with client UDP/direct paths blocked; verify a relay-to-relay selected path. Private addresses and management/listener ports remain inaccessible through allocations on IPv4 and IPv6 |
| [Registry bounds](adr/0004-bounded-task-registry.md) | Exhaust per-owner/global count and byte caps, including concurrent reservations and restart. Reject with no partial entry; reconnect existing owners at capacity. Unregister only with the owning credential, reclaim storage, and restore a bounded backup |
| [Task and data access](adr/0001-owner-local-tasks.md) | Reject invalid/oversized input, malicious GUI bundles/output, cross-task access, replay, and signaling substitution. Test revocation during an existing session, group cancellation, bounded retention, and interrupted file transfer with digest verification |

## Shared check gate

`scripts/check.sh` and GitHub Actions now run the same gate: formatting/lint, Go vet, typechecking/compilation, race-enabled tests, and both builds. Focused browser, lifecycle, ingress, release, and infrastructure checks are present; add protocol checks as those paths land. Tools, dependencies, providers, and actions are pinned. Hosted runner execution remains external. Maintainers may review required checks manually; no branch protection is configured by this scaffold.

## Open items

| Resolve before | Implementation choice / proof still needed |
| --- | --- |
| Authentication implementation | Independent review of [ADR 0009](adr/0009-poc-signaling-and-peer-authentication.md) and both implementations; reconnect after a dropped session |
| Public registration | Owner enrollment/recovery; numeric per-owner/global registry caps, cleanup bounds, and reserved storage headroom |
| Public connectivity test | VM sizing, fixed ingress rules, coturn addressing/certificates/listener and relay ports, credential lifetimes, abuse limits, and firewall enforcement of the relay exception |
| Real script execution | Resumable file transfer; whether the minimal [task file](../docs/tasks.md) and the `runlink-auth-v1` messages need versioning; retention limits; run-ID acceptance lifetime and deduplication retention; durable record format; supervisor handoff, identity, and reconciliation on both platforms |
| Public release | Supported browser/platform matrix, GUI version withdrawal/update policy, and signed release verification details |
