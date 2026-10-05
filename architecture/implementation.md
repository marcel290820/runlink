# Implementation plan

[Architecture overview and ADR index](README.md)

The [local foundation](../README.md) now supplies both executables, the loader/GUI asset boundary, shared checks, hosted CI configuration, three-server infrastructure, and signed release/deployment tooling. See [verification](../docs/verification.md) for executed checks and limits. Product transport, authentication, registry, task execution, recovery, and supervision remain planned. Complete each proof before broadening scope. ADRs record the design; this page tracks the work needed to deliver it.

## Build sequence

| Step | Deliverable | Exit condition |
| --- | --- | --- |
| 1. Foundation and transport | Go executables, trusted loader/ingress, bounded registration, signaling, direct/TURN paths, authentication | Two machines connect across representative networks in both sharing modes; both peers can relay through C over TCP/TLS. UUID-only access, replay, and substituted peers fail. Established sessions survive B shutdown. Measure connection time, relay usage, and cost |
| 2. One useful task with recovery | Existing script, typed input, verified owner-served GUI, progress/result, durable handles and acceptance, independent supervision | Coworker completes a task unaided. Authorization, timeout/size/concurrency limits, deduplication, reload recovery, and abrupt runner-death checks already pass on macOS and Linux |
| 3. Failure and exposure controls | File recovery, expiry/revocation, retention, cancellation, registry restore/cleanup | Complete the failure checks below; downloads retry without rerunning tasks; capacity and cleanup preserve existing ownership and recovery |
| 4. Delivery | Restricted releases, platform binaries, clear errors, polished GUI | Verify production deployment isolation; independently review authentication; critique rendered UI; pilot publication of real scripts |

## Required proofs

These are acceptance criteria for future implementation, not tests that have already passed.

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
| Authentication implementation | Reviewed browser/Go protocol and libraries for secret possession, task/peer binding, freshness, and reconnect |
| Public registration | Owner enrollment/recovery; numeric per-owner/global registry caps, cleanup bounds, and reserved storage headroom |
| Public connectivity test | VM sizing, fixed ingress rules, coturn addressing/certificates/listener and relay ports, credential lifetimes, abuse limits, and firewall enforcement of the relay exception |
| Real script execution | Exact `task.json` and versioned message schema; numeric input/output/time/retention limits; run-ID acceptance lifetime and deduplication retention; durable record format; supervisor handoff, identity, and reconciliation on both platforms |
| Public release | Supported browser/platform matrix, GUI version withdrawal/update policy, and signed release verification details |
