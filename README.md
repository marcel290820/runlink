# Runlink

Runlink turns a bounded script into a browser tool while execution stays on the
owner's machine. See [VISION](VISION.md), [RATIONALE](RATIONALE.md), and the
[architecture](architecture/README.md).

This repository contains a locally verified development and shipping foundation
and a working local proof of concept: `runlink share` publishes a script as a link,
and a browser runs it over an authenticated WebRTC connection. Run recovery, process
supervision, expiry and revocation, and the real network setup remain future work.

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
[ADR 0008](architecture/adr/0008-local-first-proof-of-concept.md). In another
terminal, share the example task; the owner CLI finds the stack without
configuration:

```bash
build/runlink share examples/greet.json
```

Open the printed link, fill in the form, and run it: the script runs on your
machine, and the result appears in the browser. `examples/sort.json` takes files
and returns one.
Sharing a script directly, as in `build/runlink share ./backup.sh`, asks a few
questions the first time and saves the form beside it.
[Sharing a task](docs/tasks.md) describes task files. Ctrl-C stops sharing, and
in the first terminal stops all three services.

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
| [Sharing a task](docs/tasks.md) | `runlink share`, task files, input mapping, and limits |
| [Development](docs/development.md) | Commands, the local stack, config, asset generation, and check coverage |
| [Operations](docs/operations.md) | Signed packaging, local deployment, rollback, and the deferred external setup |
| [Verification](docs/verification.md) | Local evidence and infrastructure/platform limits |
| [Implementation plan](architecture/implementation.md) | Remaining product build sequence and proofs |
