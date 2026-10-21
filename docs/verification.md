# Local verification and remaining proofs

The foundation was developed and locally verified in an isolated worktree.
Verification did not provision resources, change DNS/accounts, deploy remotely,
change host services, or use retained service credentials.

## Executed locally

The shared gate is `scripts/check.sh`. It has been executed on Ubuntu 24.04 amd64
with workspace-installed tools. A clean copy of all proposed sources also bootstrapped
from empty caches and passed the same gate. The documented `scripts/dev.sh` command
was exercised there: both readiness endpoints, frontend-to-app ingress, private state,
and cleanup of both children after SIGTERM passed. After the move to the Ubuntu 26.04
baseline, a clean copy bootstrapped from empty caches and passed the gate in an
`ubuntu:26.04` container, including cloud-init schema and systemd unit validation;
Chrome's base libraries were installed there as hosted runners provide them.

The foundation was later rewritten for readability in a separate worktree, keeping
its behavior. On Ubuntu 24.04 amd64 the rewrite passed the gate with the existing
workspace tools, and again after bootstrapping into an empty tools directory, where
nft and coturn report unavailable because the Ubuntu 26.04 libraries are not
extracted on that host. Against the previous implementation, the generated browser
assets and the rendered `build/infra` files are byte-identical, and 114 HTTP requests
across both roles, every fixed route, unsupported methods, and worker/upgrade headers
returned identical status, headers, and bodies. `scripts/dev.sh` served both roles
and stopped both children on SIGTERM and when one child exited. A counterpart review
found regressions that were fixed with checks that fail without the fix: IPv6 zones
smuggling URL syntax into the upstream, waiting for the body of a rejected upstream
status, smoke accepting non-200 success, and blocking on a FIFO in a release.
The local stack of [ADR 0008](../architecture/adr/0008-local-first-proof-of-concept.md)
was added in a separate worktree. A clean bootstrap into an empty tools directory
on Ubuntu 24.04 amd64 passed the gate before and after the change, using tools
bootstrapped into an empty directory. The local proof of concept was added in a
separate worktree on Ubuntu 24.04 amd64 with the existing workspace tools, and the
gate passed. Three counterpart review rounds found thirteen defects, fixed with checks that
fail without the fix: processes outliving their command, input in the program name,
trailing task-file content, a stalled or slow recipient blocking others or its own
script, writes without a deadline, registration without one, and in the loader
missed connection failures, unchecked input size, unbounded output, empty messages
and progress floods escaping the bounds, and no deadline after the channel opened. `runlink` now links Pion WebRTC as a peer; `runlink-server` links no
Pion package, and neither shipped executable links the local stack.

The gate's checks cover:

| Area | Evidence |
| --- | --- |
| Go foundation | Formatting, vet, compilation, both executable builds, race-enabled tests |
| Local stack | In-process stacks on free ports: A relays B's status, private state and secret modes, a persistent secret, refusal of a shared state directory and of FIFO, symlinked or oversized secrets, loopback-only listeners, port conflicts and a failing B stop the whole stack; built `runlink-dev` starts, the built CLI reaches it through `RUNLINK_SERVICE`, SIGTERM drains both servers; development builds target the default stack |
| TURN stand-in | Credentials computed independently of Pion allocate relays in the coturn port range and carry data relay to relay; expired, forged and wrong-password credentials, a fifth allocation per credential, also under concurrent TCP allocations, and peers outside loopback are refused; a failing TURN listener stops the stack, and shutdown closes TCP clients and frees relay ports; rejections are logged without passwords or the secret. Chromium exchanges data-channel messages through relay-only UDP and TCP paths from the loader origin and gets no relay for forged or expired credentials |
| Signaling | Through A and B: an owner registers a UUID; a browser receives its own TURN REST credential, verified independently, and exchanges one offer and answer, after which signaling closes normally; declined offers, an owner lost mid-exchange, unknown tasks, a full registry, and shutdown with open owner connections end cleanly; a hostile B's binary, non-JSON and oversized messages are never relayed, its handshake headers never reach the client, client cookies and credentials never reach B, and a missing B yields A's JSON 502; non-WebSocket upgrades and plain requests on signaling routes are refused; unsafe TURN configuration and secret files refuse startup |
| Sharing a task | `runlink share` through an in-process stack: the link, signaling through A and B, authentication, the approved GUI and definition, typed values as positional arguments, options with separate or joined values, flags, environment variables, stdin lines, a file's contents on stdin, and JSON stdin, with single-pass placeholders; an absent value leaves out its argument, flag and variable, even one the owner's shell sets, while an empty argument keeps a later one's place; recipients never get how inputs are passed; stages and progress, output, invalid input reported per field, and the session still running tasks after A, B and the TURN server stop. Peer authentication refuses a wrong secret, another task's UUID, proofs over a relayed peer's fingerprint, replayed proofs, and malformed nonces, and the runner's proof does not verify under a wrong secret; descriptions with no, other, or several fingerprints are refused. Task files with unknown fields, missing programs, invalid bounds, options, defaults, file limits or output names are refused, as are flags without their placeholder as a whole argument, invalid, loader- or shell-controlling, or repeated variable names, a file sharing stdin, and inputs that reach the script nowhere; values of the wrong type, out of range, off step, not an option, option-like where they start an argument (but not joined to a flag or after `--`), or unknown are refused, and a run whose empty value would let the next one start an argument with `-` fails, as are files announced too large, with a fractional or negative size, extra fields, or a name that is a path, dots, empty, or has control characters; a file arrives after acceptance at a path under its own name in a private directory, as an argument and in stdin, and is deleted with the directory after the run; files of inputs whose names differ only in case stay apart; a browser sending more than it announced loses its session, and one that never sends its file holds the run only until the timeout; non-zero exits, timeouts, oversized and invalid JSON output fail with their codes; stopping kills the whole process group, and a background process outliving its command is killed when the run ends; input never selects the program; trailing content after a task object is refused; a recipient that stops reading holds only its own session while another completes a run, and is disconnected once a write waits 15 seconds; a slow recipient misses progress lines instead of stalling a script whose stderr outgrows the pipe; a service that upgrades but never registers the task fails the share within its startup deadline; control characters, including terminal escape sequences, reach the owner's event log escaped, one entry per line; the link goes to a clipboard tool's stdin, or through OSC 52 without one, and the platform opener gets the full link. Through a pseudo-terminal: a script's first share asks its questions, explains a choice without options inline, shows the form and command and saves nothing until confirmed, writes a private task file the loader accepts, and serves that form; the next share asks nothing and serves the same form; the setup asks how each input is passed and saves options and flags before arguments, a variable, and a stdin line, which runs then pass; `--edit` suggests the stored answers, so Enter through them leaves the file unchanged, keeps unasked fields and the file's permissions, and regenerates the command it made; a hung browser opener does not keep Ctrl-C from stopping the share; a run still delivering progress does not overwrite the status of a run that started after it; Ctrl-C during the setup exits 130 and leaves no file; stored files that are invalid, malformed (with the line), oversized, carry unknown fields, or run another script stop the share with the path, with and without `--edit`, and are never asked over; without a terminal a script shares as text without input and writes nothing, and `--edit` is refused; `NO_COLOR` output has no styles and, in a C locale, no non-ASCII characters, and `TERM=dumb` output has no escape sequences, both with the link, connections, runs and stderr; every stderr line of a run stays in the terminal in full, under one line marking its start, while a run without stderr stays one line; status lines are cut to the terminal width with their styles intact |
| Chromium end to end | The built `runlink share` on the examples with `runlink-dev`: a one-click link completes the task with its progress and result over a direct path and over a relay-only path, leaving browser storage empty; `examples/sort.json` sorts a 600 KB file sent over a relay-only path, and the result downloads as `sorted.txt`; with `--separate-secret`, a wrong secret is refused by the owner's machine and the right one completes the task; after Ctrl-C the link reports itself inactive; input over 64 KB is refused before sending and the session stays usable; an owner killed mid-run shows as a lost connection; a hostile owner page that withholds its proof is dropped at the 30-second setup deadline, and ones streaming more than 1 MB of output or thousands of empty messages or progress updates, during a run or unasked, are cut off; no link secret reaches the stack's logs |
| Owner CLI | Service origin validation without echoing values; status through the local stack; error statuses, redirects, non-JSON, trailing-data, oversized, truncated and terminal-escape responses rejected; a packaged release defaults to `https://runlink.dev`, proven through an unreachable loopback proxy |
| Runtime | Actual help/version/error commands, config-file overrides, private writable-state setup and shared-directory rejection, health/readiness, frontend-to-app status relay between the built binaries, SIGTERM listener closure, header timeout, request drain and bounded forced shutdown; native HTTPS/HSTS with temporary certificates, obsolete TLS rejection and missing-material fail-closed behavior |
| Browser assets | Deterministic regeneration check, complete asset manifest, Chromium fragment/history cleanup, empty browser storage, incomplete links reported, reload and separate-secret entry, blank and partial secrets refused and the password field cleared, a hostile upstream reported as an unreachable owner without the secret in any request, fonts loading under the page CSP, approved GUI execution and modified-GUI rejection; with a fake task: the helix drawn along the task's wires, owner text rendered as text, required, range and file-type checks before sending with focus on the first problem, typed values with empty optional fields left out, owner field names that cannot shadow form properties, acceptance kept apart from running, sent values locked as text, the result-side wire kept in place through every run state, file results saved only as octet-stream downloads, raster images previewed by their bytes and SVG never, CSV previews that keep quoted empty records and final empty cells, runner field errors focused, the owner's failure message shown in full, uncoded failures shown without detail, definitions with unsupported types, property types, or impossible dates refused |
| Ingress | Malicious HTML/SVG/JS, redirects, unexpected statuses, oversize/error bodies, hostile MIME/CSP/cookie/worker headers, fixed routing, no credential-header forwarding, worker/upgrade rejection, actual browser API navigation/worker isolation |
| CI | YAML parsing, actionlint, immutable action references, Linux/macOS hosted matrix, read-only permissions, short retention, shared gate invocation |
| Infrastructure | OpenTofu format/validate, pinned provider initialization and a signed 13-platform checksum lock; mocked three-server apply and unrestricted-SSH rejection; B's rendered TURN URIs for C's listeners and its secret loaded as a systemd credential, with no TURN secret in cloud-init, and B starting with that rendered configuration; actual cloud-init schema validation, shell syntax/lint, systemd unit validation in a temporary root |
| TURN fixture | Pinned coturn 4.6.1 parses production option/deny-list syntax with fixture addresses, temporary TLS/REST credentials; exact startup-helper private-file generation and invalid-secret rejection; paced same-server loopback relay over UDP, TCP and TLS |
| Releases | Deterministic archives; temporary RSA signatures, checksum/signature failures, unsigned extras, traversal rejection, packaged binary smoke, immutable update, explicit rollback, modified installed-version rejection and automatic link restoration after smoke failure; requested restart/stop behavior using a temporary systemctl stub |
| Dependencies | `govulncheck` source scan and `npm audit --audit-level=high` against public advisory databases |
| Documentation | Local link/anchor and whitespace checks |

### Pin provenance

Reviewed on 2026-10-06 against upstream signatures, with each signing key confirmed
through a second, independent source:

| Pin | Proof |
| --- | --- |
| Go 1.27.3 | Each archive's GPG signature verifies with a subkey of Google's Linux packages key `EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796`, as published on google.com/linuxrepositories |
| Node 24.21.0 | `SHASUMS256.txt` is signed by `5BE8A3F6C8A5C01D106C0AD820B1A390B168D356`, an active releaser in the nodejs/node README; locked hashes match |
| OpenTofu 1.13.1 | `SHA256SUMS` GPG signature by `E3E6E43D84CB852EADB0051D0C0AF313E5FD9F80` (also in opentofu/opentofu docs and keys.openpgp.org); Sigstore certificate for `opentofu/opentofu` `release.yml@refs/heads/v1.13` chains to the Fulcio root, verifies the file and matches Rekor entry 3037839244 |
| hcloud provider 1.70.0 | `SHA256SUMS` signed by `1454BE0E275F52F52467BBF65219EACB3A77198B`, the key both the OpenTofu and Terraform registries list; all 13 `zh:` and `h1:` lock hashes recomputed from the signed zips |
| govulncheck 1.8.0, actionlint 1.7.12 | Built from source; Go checksum database lookups |
| Playwright 1.63.0, YAML 2.9.1 | npm registry signatures for all packages; Playwright provenance attests a GitHub-hosted build from `microsoft/playwright` tag `v1.63.0` |
| GitHub Actions | Pinned SHAs are the `v7.0.1` tag commits in the official repositories; GitHub-verified commits |
| Ubuntu 26.04 libraries | `InRelease` signatures by the Ubuntu Archive Automatic Signing Key (2018) `F6ECB3762474EDA9D21B7022871920D1991BC93C`; all 34 hashes match the signed package indexes |
| Chromium headless shell 153.0.8010.12 | Playwright CDN bytes identical to Google's Chrome for Testing bucket on all four platforms |
| Go modules for `runlink-dev` (pion/turn 4.1.4 and its dependencies, newest stable on 2026-10-07; replaced on 2026-10-16 by pion/turn 5.1.2, reviewed in the next row) | `go.sum` hashes are checked against the sum.golang.org transparency log; all 20 modules in the graph were fetched again directly from their origin repositories and match `go.sum`, and a tampered hash fails. The tagged commits of pion/turn 4.1.4, pion/dtls 3.1.10 and pion/transport 5.1.1 carry valid signatures by `686E6E5F8D157DE2B8DFA974A8CD240651DB01B6` (Jo Turk), published on keys.openpgp.org and registered on the committer's GitHub account; pion/stun 3.1.7 and wlynxg/anet 0.0.5 carry GitHub web-flow signatures by `968479A1AFF927E37D1A566BB5690EEEBB952194` |
| Go modules for the proof of concept, reviewed 2026-10-13 (coder/websocket 1.8.15, pion/webrtc 4.2.23 and its pion modules, google/uuid 1.6.0, golang.org/x/net 0.61.0, x/crypto 0.58.0, x/sys 0.49.0, x/time 0.16.0, pion/dtls 3.1.11; each the newest stable) | Every `go.sum` line matches sum.golang.org and a direct fetch from the origin repository; tag commits resolve to the recorded origin hashes. pion/webrtc, datachannel, ice, interceptor, mdns, rtp, sctp, stun/v4 and turn/v5 commits carry valid signatures by `686E6E5F8D157DE2B8DFA974A8CD240651DB01B6` (Jo Turk), as above; coder/websocket, pion/rtcp, sdp, srtp, dtls 3.1.11 and google/uuid carry GitHub web-flow signatures by `968479A1AFF927E37D1A566BB5690EEEBB952194`, which attest a GitHub-made commit, not its author |

pion/logging 0.2.4, pion/randutil 0.1.0, pion/transport 4.1.1 and the golang.org/x
modules have unsigned commits; they rest on the checksum database and the matching
origin fetch. No Go module tag is signed: every one is lightweight, so signatures
cover commits only. Upstream publishes no signature
for two tool pins. ShellCheck 0.11.0 has no signatures or
attestations; the pinned `.tar.gz` files were uploaded by the maintainer on 2026-01-05
and contain binaries identical to the CI-uploaded `.tar.xz` release assets.
Playwright's ffmpeg 1011 exists only on Microsoft's CDN, so its pin is trust on first
use; the tests do not record video, so it never runs.

Generated infrastructure fixtures use documentation addresses and public test SSH
keys. The coturn fixture changes listeners to loopback, uses test ports and a
loopback-peer exception, and does not apply a host firewall. It proves local relay
behavior, not the production firewall/NAT acceptance criterion.

The final native-TLS release was also cross-built for all four targets as
`dist/v0.0.1-local`, inspected for both binaries and release metadata, signed,
and verified using a temporary RSA key. That test key was discarded; these are local
test artifacts. Repackage and sign with the separately established release key for
an actual public release. Runtime execution was verified on Linux amd64; the other
archives were checked for build success and packaging completeness.

## External or privileged verification

- GitHub must execute the workflow on its real Linux and macOS hosted runners.
  Cross-compiling a macOS binary does not prove macOS runtime behavior.
- nftables userspace checks reach the local kernel permission boundary. Complete
  rule validation and enforcement require CAP_NET_ADMIN on a disposable/new VM;
  no privilege escalation or host firewall change is used to obtain that proof.
- Hetzner credentials and authorized provisioning are needed to inspect actual
  IPs, network attachment, cloud-init boot, SSH rules, runtime permissions, private
  ingress isolation, availability, and machine-level service operations.
- DNS and real certificates are needed for ACME issuance/renewal and production
  HTTPS/TURN TLS checks. Local configuration parsing is not production TLS proof.
- Representative networks and both actual peers are needed for direct/TURN
  connectivity, same-C relay-to-relay selection with TCP/TLS-only clients, and
  IPv4/IPv6 denial of private and management/listener destinations.
- Maintainers must establish signing/public-key trust, release access, monitoring,
  traffic/cost alerts, OS updates, and a future state backup/restore procedure.

Follow the ordered [external setup](operations.md#external-setup-in-order). Product
transport/authentication, registry, task execution, browser recovery, and independent
supervision are intentionally absent; their acceptance criteria remain in the
[implementation plan](../architecture/implementation.md#required-proofs).

## Source files

The foundation consists of these files. Generated tools, caches, binaries, fixtures
and archives are ignored.

### Repository and documentation

- [.claude/check.sh](../.claude/check.sh)
- [.gitignore](../.gitignore)
- [AGENTS.md](../AGENTS.md)
- [README.md](../README.md)
- [architecture/README.md](../architecture/README.md)
- [architecture/adr/0007-local-shipping-foundation.md](../architecture/adr/0007-local-shipping-foundation.md)
- [architecture/adr/0008-local-first-proof-of-concept.md](../architecture/adr/0008-local-first-proof-of-concept.md)
- [architecture/implementation.md](../architecture/implementation.md)
- [docs/development.md](development.md)
- [docs/operations.md](operations.md)
- [docs/verification.md](verification.md)
- [go.mod](../go.mod)
- [go.sum](../go.sum)
- [package-lock.json](../package-lock.json)
- [package.json](../package.json)

### Executables

- [cmd/runlink-dev/main.go](../cmd/runlink-dev/main.go)
- [cmd/runlink-server/main.go](../cmd/runlink-server/main.go)
- [cmd/runlink/main.go](../cmd/runlink/main.go)
- [cmd/runlink/main_test.go](../cmd/runlink/main_test.go)

### Go implementation and browser assets

- [internal/buildinfo/version.go](../internal/buildinfo/version.go)
- [internal/devstack/stack.go](../internal/devstack/stack.go)
- [internal/devstack/stack_test.go](../internal/devstack/stack_test.go)
- [internal/devstack/turn.go](../internal/devstack/turn.go)
- [internal/devstack/turn_test.go](../internal/devstack/turn_test.go)
- [internal/frontend/assets/archivo.woff2](../internal/frontend/assets/archivo.woff2)
- [internal/frontend/assets/capture.js](../internal/frontend/assets/capture.js)
- [internal/frontend/assets/gui.js](../internal/frontend/assets/gui.js)
- [internal/frontend/assets/index.html](../internal/frontend/assets/index.html)
- [internal/frontend/assets/loader.mjs](../internal/frontend/assets/loader.mjs)
- [internal/frontend/assets/manifest.json](../internal/frontend/assets/manifest.json)
- [internal/frontend/assets/martian-mono.woff2](../internal/frontend/assets/martian-mono.woff2)
- [internal/frontend/assets/style.css](../internal/frontend/assets/style.css)
- [internal/frontend/fonts-OFL.txt](../internal/frontend/fonts-OFL.txt)
- [internal/frontend/frontend.go](../internal/frontend/frontend.go)
- [internal/frontend/frontend_test.go](../internal/frontend/frontend_test.go)
- [internal/frontend/helix.js](../internal/frontend/helix.js)
- [internal/frontend/index.template.html](../internal/frontend/index.template.html)
- [internal/frontend/loader.template.mjs](../internal/frontend/loader.template.mjs)
- [internal/server/config.go](../internal/server/config.go)
- [internal/server/config_test.go](../internal/server/config_test.go)
- [internal/server/frontend.go](../internal/server/frontend.go)
- [internal/server/frontend_test.go](../internal/server/frontend_test.go)
- [internal/server/server.go](../internal/server/server.go)
- [internal/server/server_test.go](../internal/server/server_test.go)

### Development, verification and release tools

- [scripts/assets.py](../scripts/assets.py)
- [scripts/bootstrap.py](../scripts/bootstrap.py)
- [scripts/bootstrap.sh](../scripts/bootstrap.sh)
- [scripts/browser.test.mjs](../scripts/browser.test.mjs)
- [scripts/build.sh](../scripts/build.sh)
- [scripts/check.sh](../scripts/check.sh)
- [scripts/config-check.mjs](../scripts/config-check.mjs)
- [scripts/dev.sh](../scripts/dev.sh)
- [scripts/docs-check.py](../scripts/docs-check.py)
- [scripts/env.sh](../scripts/env.sh)
- [scripts/harness.py](../scripts/harness.py)
- [scripts/infra-check.py](../scripts/infra-check.py)
- [scripts/lifecycle.py](../scripts/lifecycle.py)
- [scripts/linux-libs.lock.json](../scripts/linux-libs.lock.json)
- [scripts/release-test.py](../scripts/release-test.py)
- [scripts/release.py](../scripts/release.py)
- [scripts/release.sh](../scripts/release.sh)
- [scripts/stack.test.mjs](../scripts/stack.test.mjs)
- [scripts/tools.lock.json](../scripts/tools.lock.json)
- [scripts/turn-check.py](../scripts/turn-check.py)

### Infrastructure

- [infra/.terraform.lock.hcl](../infra/.terraform.lock.hcl)
- [infra/main.tf](../infra/main.tf)
- [infra/templates/configure-turn.py](../infra/templates/configure-turn.py)
- [infra/templates/firewall.nft.tftpl](../infra/templates/firewall.nft.tftpl)
- [infra/templates/initialize.sh.tftpl](../infra/templates/initialize.sh.tftpl)
- [infra/templates/renew-tls.sh.tftpl](../infra/templates/renew-tls.sh.tftpl)
- [infra/templates/runlink-turn.service](../infra/templates/runlink-turn.service)
- [infra/templates/runlink.service.tftpl](../infra/templates/runlink.service.tftpl)
- [infra/templates/turnserver.conf.tftpl](../infra/templates/turnserver.conf.tftpl)
- [infra/tests/stack.tftest.hcl](../infra/tests/stack.tftest.hcl)
- [infra/variables.tf](../infra/variables.tf)
- [infra/versions.tf](../infra/versions.tf)

### CI

- [.github/actionlint.yaml](../.github/actionlint.yaml)
- [.github/workflows/check.yml](../.github/workflows/check.yml)
