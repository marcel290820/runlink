# Development

## Bootstrap and commands

`scripts/bootstrap.sh` installs checksum-pinned Go 1.27.2, Node 24.21.0, OpenTofu
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
| `scripts/dev.sh` | Regenerate assets, build, and run the [local stack](#local-stack) until Ctrl-C |
| `scripts/check.sh` | Shared local/CI gate described below |
| `.claude/check.sh` | The same gate, where agent stop and commit hooks look for it |
| `python3 scripts/assets.py` | Regenerate loader approvals, HTML, and asset manifest |
| `python3 scripts/assets.py --check` | Reject stale generated assets without rewriting them |
| `scripts/release.sh --help` | Packaging, signing, verification, local deployment, smoke, rollback |

Downloads need network access, but application tests run only against loopback.
Infrastructure initialization downloads a checksum-locked provider. Tests use a
mocked provider and never call the cloud API. Vulnerability scans query public
advisory databases and fail on applicable findings or scan failures.

## Local stack

`scripts/dev.sh` runs the whole product on loopback, as decided in
[ADR 0008](../architecture/adr/0008-local-first-proof-of-concept.md). It
regenerates the browser assets, builds, and starts `build/runlink-dev`. A and B
are the production server roles; only infrastructure is replaced.

| Server | Local address | Runs |
| --- | --- | --- |
| A frontend | `http://127.0.0.1:8080` | `runlink-server` frontend role: loader, CSP, fixed routes, ingress to B |
| B app | `http://127.0.0.1:8081` | `runlink-server` app role with state in `var/dev/app` |
| C turn | `127.0.0.1:3478`, UDP and TCP | Pion TURN stand-in for coturn, relays on `127.0.0.1` ports 49160-49259 |

The ADR lists what each stand-in replaces and what is not exercised locally. The
stack reports ready once every listener is bound and A and B answer `/readyz`,
then prints its addresses and ICE URIs. Ctrl-C or SIGTERM drains A and B and stops
the TURN server, closing its TCP clients and waiting until every relay port is
released. If any service fails, TURN included, all stop and `runlink-dev` exits
non-zero with the cause. Logs are JSON on stderr, tagged with `service`; the `stack_ready`
event lists the addresses for scripts. TURN rejections are logged with the
username and reason, never a password or the secret.

`var/dev` keeps B's state and the TURN shared secret `turn-secret` across
restarts: 64 lowercase hex characters, mode `0600`, generated on first start and
validated as C validates it. The directory must be private (`0700`) and the secret
a regular file. Delete `var/dev` to start fresh. `--frontend`,
`--app`, `--turn`, and `--state-dir` move the stack; addresses must be loopback,
TURN's IPv4 because Pion relays IPv4 only, and port 0 picks a free port.

Development builds of the owner CLI target `http://127.0.0.1:8080`; release
builds stamp `https://runlink.dev`. `RUNLINK_SERVICE` overrides either, with plain
HTTP accepted only for a loopback IP. `build/runlink status` checks the status
route through A.

The stand-in accepts coturn's TURN REST credentials, as B will issue them. For
manual tests, mint one and force the relay path with
`iceTransportPolicy: 'relay'` and the ICE server
`{ urls: 'turn:127.0.0.1:3478?transport=udp', username, credential }`:

```bash
username="$(($(date +%s) + 600)):manual"
credential=$(printf %s "$username" | openssl dgst -sha1 -hmac "$(cat var/dev/turn-secret)" -binary | base64)
```

Go end-to-end tests start a stack in-process on free ports, as
`cmd/runlink/main_test.go` does; browser tests start `build/runlink-dev` and read
`stack_ready`, as `scripts/stack.test.mjs` does.

```go
stack, err := devstack.Start(t.Context(), devstack.Config{
	Frontend: "127.0.0.1:0", App: "127.0.0.1:0", TURN: "127.0.0.1:0", StateDir: t.TempDir(),
}, slog.New(slog.DiscardHandler))
```

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
selects a free port; tests use it. A frontend upstream must be exactly
`http://IP:port` with a private or loopback IP, so it carries no credentials, path,
query, or fragment.
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
Lifecycle logs contain fixed events, the role, and the bound listen address, which
`server_started` reports once the server is ready. Startup and serve failures include
their cause for the operator. Request URLs, headers, bodies, recipient secrets, task
data, and request-path errors are excluded.

## Browser trust boundary

The server binary embeds the served assets with `go:embed`. The trusted frontend
serves only `/`, lowercase UUID task paths, and the exact loader, style, manifest,
and two font routes. The GUI bundle is neither embedded nor served by A. The future
authenticated owner transport will embed it in the owner binary and deliver it.

The first inline script captures a fragment in memory and clears the current URL
and history state before the module loader starts. Separately entered secrets are
trimmed and removed from the password field. No browser storage, request, or logging
code persists those values. `mountGUI(bytes, task)` verifies the complete bytes
against the loader's generated approved SHA-256 list before creating executable
content. The GUI registers `runlinkGUI`, and the loader hands it the task object;
the header of [gui.js](../internal/frontend/assets/gui.js) documents the task
definition and the run contract the transport must provide. Tests call the mount
function directly with a fake task; there is no production connection path yet.

`assets.py` fills `internal/frontend/loader.template.mjs` with the GUI approval
and `internal/frontend/index.template.html` with the inline capture script and SRI
hashes, then covers every asset in a deterministic manifest. The frontend computes
its hash-based CSP from the embedded page's inline script and the loader bytes. Trusted Types permits only the loader's
verified GUI object URL; the CSP disables workers, framing, base URL changes, and
other executable sources. The GUI uses `textContent` and safe DOM creation.

The page sets interface text in Archivo and data in Martian Mono, vendored as WOFF2
subsets that only A serves, under `font-src 'self'`. Both use the SIL Open Font
License 1.1 ([fonts-OFL.txt](../internal/frontend/fonts-OFL.txt)). They were built
from the variable fonts in google/fonts at commit
`bd8f81ddb5c74d5c8897b36ad88b440266245103`: `ofl/archivo/Archivo[wdth,wght].ttf`
(SHA-256 `0e094a7d3c7c4c25cf1310c4b30014f1dae9332220b1c2c88f4fa996f0b05053`) and
`ofl/martianmono/MartianMono[wdth,wght].ttf`
(SHA-256 `c3467843ec1c2574b05fbcfd7147c7bfbcf63ddca8fc2bcb9d117f1bfb1b22e7`).
fontTools 4.60.1 with Brotli reproduces the manifest's hashes when
`SOURCE_DATE_EPOCH=1791763200` is set:

```bash
U='U+0020-007E,U+00A0-017F,U+0192,U+02C6-02DC,U+2000-206F,U+20A0-20CF,U+2100-214F,U+2190-21FF,U+2200-22FF,U+25A0-25FF'
fonttools varLib.instancer 'Archivo[wdth,wght].ttf' wght=400:900 wdth=100:125 -o archivo.ttf
pyftsubset archivo.ttf --unicodes="$U" --layout-features+=tnum,zero,case,lnum --flavor=woff2 --output-file=archivo.woff2
fonttools varLib.instancer 'MartianMono[wdth,wght].ttf' wght=400:700 wdth=87.5:112.5 -o martian.ttf
pyftsubset martian.ttf --unicodes="$U" --layout-features+=zero,case --flavor=woff2 --output-file=martian-mono.woff2
```

The sole forwarded API is `GET /api/v1/status`. A forwards no recipient headers,
cookies, query, or body. It accepts bounded JSON and a small status allowlist,
discards every upstream header, and supplies JSON MIME, nosniff, sandbox CSP,
no-referrer, and no-store headers independently. Unknown routes and all upgrades
remain closed. WSS must receive its own boundary checks when implemented.

## Shared checks and CI

The same `scripts/check.sh` runs locally and on standard `ubuntu-26.04` and
`macos-26` hosted runners. It checks generated assets, Go formatting, shell lint,
JavaScript/Python syntax, workflow YAML/action pins, Go vet, race-enabled behavioral
tests, both builds, actual CLI behavior, config-file overrides, frontend-to-app
relay and SIGTERM shutdown, the local stack and Chromium WebRTC through its TURN
stand-in, Chromium browser behavior,
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
