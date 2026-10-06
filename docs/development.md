# Development

## Bootstrap and commands

`scripts/bootstrap.sh` installs checksum-pinned Go 1.27.1, Node 24.21.0, OpenTofu
1.13.1, and ShellCheck 0.11.0. Go builds `govulncheck` 1.8.0 and actionlint 1.7.12
from source, verified against the Go checksum database. Versions and archive checksums
live in [tools.lock.json](../scripts/tools.lock.json). `npm ci` uses the checked-in
lockfile for Playwright 1.63.0 and YAML 2.9.1. Playwright downloads Chromium's headless
shell and ffmpeg through a loopback mirror in bootstrap that serves only files matching
the checksums in `tools.lock.json`; an unpinned or mismatched download fails bootstrap.
Ubuntu 26.04 amd64 validator/browser libraries are checksum-pinned in
[linux-libs.lock.json](../scripts/linux-libs.lock.json), downloaded and extracted
without installing host packages. The host Python/OpenSSL/C compiler are prerequisites.

| Command | Behavior |
| --- | --- |
| `scripts/bootstrap.sh` | Install workspace tools and headless Chromium; no host service changes |
| `scripts/build.sh` | Build both executables into `build/` |
| `scripts/dev.sh` | Build and run the app and trusted frontend on loopback; stop both on exit |
| `scripts/check.sh` | Shared local/CI gate described below |
| `python3 scripts/assets.py` | Regenerate loader approvals, HTML, and asset manifest |
| `python3 scripts/assets.py --check` | Reject stale generated assets without rewriting them |
| `scripts/release.sh --help` | Packaging, signing, verification, local deployment, smoke, rollback |

Downloads need network access, but application tests run only against loopback.
Infrastructure initialization downloads a checksum-locked provider. Tests use a
mocked provider and never call the cloud API. Vulnerability scans query public
advisory databases and fail on applicable findings or scan failures.

## Configuration and runtime

`runlink-server` accepts a JSON file and explicit flag overrides:

```json
{
  "role": "app",
  "listen": "127.0.0.1:8081",
  "state_dir": "var/app"
}
```

```bash
build/runlink-server --config path/to/server.json
build/runlink-server --role frontend --listen 127.0.0.1:8080 \
  --upstream http://127.0.0.1:8081
```

The file must contain one JSON object, at most 64 KiB, with only known fields.
Roles are `app` and `frontend`; listeners require explicit IPs and valid ports.
Plain HTTP and app listeners are restricted to loopback/private addresses. Public
frontend listeners require both absolute `tls_cert` and `tls_key` paths. Port zero
is available to Go tests. Frontend upstreams require a
private/loopback IP, an explicit port, plain HTTP, and no credentials/path/query.
The frontend terminates public TLS on A using Go, with TLS 1.2 as the minimum.
Invalid or missing certificate/key material prevents startup; HTTPS replies include
HSTS. Certbot renewal copies restricted files and restarts the frontend. There is no environment-variable credential
loading or credential-bearing config in this scaffold.

The application state directory must be private (`0700`). Startup checks that it
is writable; it contains no registry yet. `/healthz` reports process health;
`/readyz` reports scaffold readiness and becomes unavailable during shutdown.
Readiness does not claim that future registration or task protocols exist.

HTTP bounds are fixed: 5 s header read, 10 s request read, 15 s response write,
30 s idle, 10 s graceful shutdown, and 16 KiB request headers. SIGINT/SIGTERM drains
requests and closes the listener; expiry of the shutdown deadline forces closure.
Lifecycle logs contain fixed events and the role. Startup and serve failures include
their cause for the operator. Request URLs, headers, bodies, recipient secrets, task
data, and request-path errors are excluded.

## Browser trust boundary

The server binary embeds the served assets with `go:embed`. The trusted frontend
serves only `/`, lowercase UUID task paths, and the exact loader/style/manifest routes.
The GUI bundle is neither embedded nor served by A. The future authenticated owner
transport will embed it in the owner binary and deliver it.

The first inline script captures a fragment in memory and clears the current URL
and history state before the module loader starts. Separately entered secrets are
removed from the password field. No browser storage, request, or logging code
persists those values. The GUI mount function verifies the complete bytes against
the loader's generated approved SHA-256 list before creating executable content.
Tests call the mount function directly; there is no production connection path yet.

`assets.py` covers every asset in a deterministic manifest and embeds the GUI
approval directly into one loader module. HTML uses SRI; the frontend computes a
hash-based CSP from the embedded release. Trusted Types permits only the loader's
verified GUI object URL; the CSP disables workers, framing, base URL changes, and
other executable sources. The GUI uses `textContent` and safe DOM creation.

The sole forwarded API is `GET /api/v1/status`. A forwards no recipient headers,
cookies, query, or body. It accepts bounded JSON and a small status allowlist,
discards every upstream header, and supplies JSON MIME, nosniff, sandbox CSP,
no-referrer, and no-store headers independently. Unknown routes and all upgrades
remain closed. WSS must receive its own boundary checks when implemented.

## Shared checks and CI

The same `scripts/check.sh` runs locally and on standard `ubuntu-26.04` and
`macos-26` hosted runners. It checks generated assets, Go formatting, shell lint,
JavaScript/Python syntax, workflow YAML/action pins, Go vet, race-enabled behavioral
tests, both builds, actual CLI/SIGTERM behavior, Chromium browser behavior,
OpenTofu and rendered configuration, loopback coturn UDP/TCP/TLS, local release lifecycle, Go vulnerabilities,
Node advisories, and whitespace.

The workflow has read-only repository permission, no persisted checkout credentials,
immutable action commits, a 25-minute timeout, and three-day binary artifact retention.
It contains no publishing/deployment step. Branch protection is outside this task;
maintainers may review green checks manually without paid features.

`build/infra/` contains rendered fixture configuration for review. These are test
addresses and public test SSH keys, never deployment credentials. Optional Linux
schema/systemd/nft checks report their availability. A Linux kernel with
CAP_NET_ADMIN and real servers are still needed to prove firewall enforcement.
