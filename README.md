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

Open <http://127.0.0.1:8080>. The trusted loader captures a link secret and explains
that secure owner connections are not available yet. Ctrl-C stops both services.
The app listens on `127.0.0.1:8081`; the frontend listens on `127.0.0.1:8080`.

```bash
scripts/build.sh
build/runlink --help
build/runlink --version
build/runlink-server --help
```

Tools, caches, browsers, and extracted shared libraries stay in `.tools/`. Set
`RUNLINK_TOOLS` to another permitted workspace directory to reuse them.
No bootstrap or check command needs sudo, Docker, a cloud token, or a GitHub secret.

| Guide | Contents |
| --- | --- |
| [Development](docs/development.md) | Actual commands, config, asset generation, and check coverage |
| [Operations](docs/operations.md) | Signed packaging, local deployment, rollback, and exact external setup |
| [Verification](docs/verification.md) | Local evidence and infrastructure/platform limits |
| [Implementation plan](architecture/implementation.md) | Remaining product build sequence and proofs |
