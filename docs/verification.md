# Local verification and remaining proofs

The foundation was developed and locally verified in an isolated worktree.
Verification did not provision resources, change DNS/accounts, deploy remotely,
change host services, or use retained service credentials.

## Executed locally

The shared gate is `scripts/check.sh`. It has been executed on Ubuntu 24.04 amd64
with workspace-installed tools. A clean copy of all proposed sources also bootstrapped
from empty caches and passed the same gate. The documented `scripts/dev.sh` command
was exercised there: both readiness endpoints, frontend-to-app ingress, private state,
and cleanup of both children after SIGTERM passed. Its checks cover:

| Area | Evidence |
| --- | --- |
| Go foundation | Formatting, vet, compilation, both executable builds, race-enabled tests |
| Runtime | Actual help/version/error commands, private writable-state setup, health/readiness, SIGTERM listener closure, header timeout, request drain and bounded forced shutdown; native HTTPS/HSTS with temporary certificates, obsolete TLS rejection and missing-material fail-closed behavior |
| Browser assets | Deterministic regeneration check, complete embedded manifest, Chromium fragment/history cleanup, empty browser storage, reload and separate-secret entry, password field clearing, approved GUI execution and modified-GUI rejection |
| Ingress | Malicious HTML/SVG/JS, redirects, unexpected statuses, oversize/error bodies, hostile MIME/CSP/cookie/worker headers, fixed routing, no credential-header forwarding, worker/upgrade rejection, actual browser API navigation/worker isolation |
| CI | YAML parsing, actionlint, immutable action references, Linux/macOS hosted matrix, read-only permissions, short retention, shared gate invocation |
| Infrastructure | OpenTofu format/validate, pinned provider initialization and four-platform checksum lock; mocked three-server apply and unrestricted-SSH rejection; actual cloud-init schema validation, shell syntax/lint, systemd unit validation in a temporary root |
| TURN fixture | Pinned coturn 4.6.1 parses production option/deny-list syntax with fixture addresses, temporary TLS/REST credentials; exact startup-helper private-file generation and invalid-secret rejection; paced same-server loopback relay over UDP, TCP and TLS |
| Releases | Deterministic archives; temporary RSA signatures, checksum/signature failures, unsigned extras, traversal rejection, packaged binary smoke, immutable update, explicit rollback, modified installed-version rejection and automatic link restoration after smoke failure; requested restart/stop behavior using a temporary systemctl stub |
| Dependencies | `govulncheck` source scan and `npm audit --audit-level=high` against public advisory databases |
| Documentation | Local link/anchor and whitespace checks |

Generated infrastructure fixtures use documentation addresses and public test SSH
keys. The coturn fixture changes listeners to loopback, uses test ports and a
loopback-peer exception, and does not apply a host firewall. It proves local relay
behavior, not the production firewall/NAT acceptance criterion.

The final native-TLS release was also cross-built for all four targets as
`dist/v0.0.1-local`, inspected for both binaries and GUI/loader manifests, signed,
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

## Changed source files

3 existing documents updated and 56 new source/configuration files added.
Generated tools, caches, binaries, fixtures and archives are ignored.

### Repository and documentation

- [.gitignore](../.gitignore)
- [AGENTS.md](../AGENTS.md)
- [README.md](../README.md)
- [architecture/README.md](../architecture/README.md)
- [architecture/adr/0007-local-shipping-foundation.md](../architecture/adr/0007-local-shipping-foundation.md)
- [architecture/implementation.md](../architecture/implementation.md)
- [docs/development.md](development.md)
- [docs/operations.md](operations.md)
- [docs/verification.md](verification.md)
- [go.mod](../go.mod)
- [package-lock.json](../package-lock.json)
- [package.json](../package.json)

### Executables

- [cmd/runlink-server/main.go](../cmd/runlink-server/main.go)
- [cmd/runlink/main.go](../cmd/runlink/main.go)

### Go implementation and browser assets

- [internal/buildinfo/version.go](../internal/buildinfo/version.go)
- [internal/frontend/assets/capture.js](../internal/frontend/assets/capture.js)
- [internal/frontend/assets/gui.js](../internal/frontend/assets/gui.js)
- [internal/frontend/assets/index.html](../internal/frontend/assets/index.html)
- [internal/frontend/assets/loader.mjs](../internal/frontend/assets/loader.mjs)
- [internal/frontend/assets/loader.template.mjs](../internal/frontend/assets/loader.template.mjs)
- [internal/frontend/assets/manifest.json](../internal/frontend/assets/manifest.json)
- [internal/frontend/assets/style.css](../internal/frontend/assets/style.css)
- [internal/frontend/frontend.go](../internal/frontend/frontend.go)
- [internal/frontend/frontend_test.go](../internal/frontend/frontend_test.go)
- [internal/server/config.go](../internal/server/config.go)
- [internal/server/server.go](../internal/server/server.go)
- [internal/server/server_test.go](../internal/server/server_test.go)
- [internal/server/tls_test.go](../internal/server/tls_test.go)

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
- [scripts/infra-check.py](../scripts/infra-check.py)
- [scripts/lifecycle.py](../scripts/lifecycle.py)
- [scripts/linux-libs.lock.json](../scripts/linux-libs.lock.json)
- [scripts/release-test.py](../scripts/release-test.py)
- [scripts/release.py](../scripts/release.py)
- [scripts/release.sh](../scripts/release.sh)
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

- [.github/workflows/check.yml](../.github/workflows/check.yml)
