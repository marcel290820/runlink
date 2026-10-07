# Runlink

Runlink turns a bounded script into a browser tool while execution stays on the
owner's machine. See [VISION](VISION.md), [RATIONALE](RATIONALE.md), and the
[architecture](architecture/README.md).

This repository now contains a locally verified development and shipping
foundation. Publishing, peer authentication, registration, WebRTC, task execution,
run recovery, and process supervision remain future product work.

## Quick start

Prerequisites: Git, Python 3.12 or newer, OpenSSL, a C compiler for Go's race
instrumentation, and internet access for pinned tool downloads. Supported developer
platforms are Linux and macOS on amd64 or arm64. Linux browser tests need Chromium's
shared libraries; bootstrap supplies workspace-local libraries on Ubuntu 26.04 amd64.
Other Linux distributions need compatible libraries already installed.

```bash
scripts/bootstrap.sh
scripts/check.sh
scripts/dev.sh
```

`scripts/dev.sh` runs the whole product on loopback: the trusted frontend on
`127.0.0.1:8080`, the app on `127.0.0.1:8081`, and a TURN stand-in for coturn on
`127.0.0.1:3478`. Nothing needs a server, DNS, certificate, or cloud account; see
the [local stack](docs/development.md#local-stack) and
[ADR 0008](architecture/adr/0008-local-first-proof-of-concept.md). Open
<http://127.0.0.1:8080>: the loader captures a link secret and explains that secure
owner connections are not available yet. In another terminal, the owner CLI finds
the stack without configuration. Ctrl-C stops all three services.

```bash
build/runlink status
build/runlink --help
build/runlink-server --help
build/runlink-dev --help
```

Tools, caches, browsers, and extracted shared libraries stay in `.tools/`. Set
`RUNLINK_TOOLS` to another permitted workspace directory to reuse them.
No bootstrap or check command needs sudo, Docker, a cloud token, or a GitHub secret.

| Guide | Contents |
| --- | --- |
| [Development](docs/development.md) | Commands, the local stack, config, asset generation, and check coverage |
| [Operations](docs/operations.md) | Signed packaging, local deployment, rollback, and the deferred external setup |
| [Verification](docs/verification.md) | Local evidence and infrastructure/platform limits |
| [Implementation plan](architecture/implementation.md) | Remaining product build sequence and proofs |
