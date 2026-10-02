# Implementation plan

[Architecture](README.md) | [Runtime](runtime.md) | [Security](security.md)

Planning only. Complete each proof before broadening scope.

## Build sequence

| Step | Deliverable | Exit condition |
| --- | --- | --- |
| 1. Foundation and transport | Go executables, loader, registration, signaling, direct/TURN paths, authentication | Two machines connect across representative networks; both sharing modes work. UUID-only access, replay, and substituted peers fail. Established sessions survive application-server shutdown. Measure connection time, relay usage, server cost |
| 2. One useful task | Existing script, typed input, verified owner-served GUI, progress/result | Coworker completes a task unaided. Authorization, timeout/size/concurrency limits, and run-ID deduplication already apply |
| 3. Failure and exposure controls | Reconnect, durable run state, files, expiry/revocation, cancellation | Fault checks cover the [runtime failure table](runtime.md#delivery-and-failure-semantics), including child processes and uncertain side effects |
| 4. Delivery | Separate servers, restricted releases, platform binaries, errors, polished GUI | Verify deployment isolation; independently review authentication; critique rendered UI; pilot publication of real scripts |

## Shared check gate

Keep shared scripts in vendor-neutral directories. Create `scripts/check.sh` before the first feature; GitHub Actions runs it too: formatting/lint, Go vet, typechecking/compilation, race-enabled tests, and both builds. Add focused browser/protocol checks as needed. Pin tools, dependencies, and actions; verify commands locally. Configure required-check branch protection separately.

## Open items

| Resolve before | Decision / proof still needed |
| --- | --- |
| Authentication implementation | Concrete browser/Go protocol and libraries for secret possession, peer binding, freshness, and reconnect. WebRTC DTLS alone is insufficient against malicious signaling |
| Public connectivity test | Hetzner VM sizing, fixed ingress routing, coturn endpoint addressing/certificates and listener/relay ports, credential lifetimes/abuse limits, and compatibility on restrictive networks; retain one public domain |
| Real script integration | Exact `task.json` and versioned message schema; numeric input/output/time limits and result retention; local durable record format and crash recovery |
| Public release | Owner enrollment/recovery, supported browser/platform matrix, GUI version withdrawal/update policy, and signed release verification details |

WebRTC supersedes TLS passthrough/per-owner certificates and all-payload WebSocket relaying. No custom cryptographic primitives, recipient accounts, or distributed scheduler.
