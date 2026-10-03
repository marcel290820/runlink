# ADR 0003: Trusted browser code and client-held recipient secrets

## Status

Accepted. Recorded 2026-10-02.

## Context

Recipients need a browser-only experience. A compromised application server must not turn control responses into executable content under the trusted `runlink.dev` origin. UUID paths share that origin and provide no browser isolation.

## Decision

### Ingress and executable content

Server A serves the immutable loader and approved GUI hashes. Server B handles registration and signaling and cannot modify loader files, hashes, frontend policies, ingress routing, or release credentials. Runtime identities cannot publish releases; [ADR 0006](0006-hetzner-deployment.md) defines deployment.

A enforces this boundary independently of B:

- Route task URLs and loader assets only to static releases. Forward only fixed API/WSS routes to B; reject unknown routes and unexpected upgrades.
- Force non-upgrade API responses, including errors, to `Content-Type: application/json` and `X-Content-Type-Options: nosniff`. Apply `Content-Security-Policy: default-src 'none'; base-uri 'none'; frame-ancestors 'none'; sandbox`, `Referrer-Policy: no-referrer`, and `Cache-Control: no-store` at A.
- Allowlist upstream response headers and statuses. B cannot supply redirects, cookies, executable MIME types, weaker security policies, or `Service-Worker-Allowed`. A generates bounded JSON errors for rejected upstream responses. Browsers validate control-message schemas and sizes.
- Disable service workers in v1: the loader's CSP includes `worker-src 'none'`; A rejects service-worker script requests on every route. Strip upstream `Service-Worker-Allowed` even on errors and WebSocket handshakes.

These controls use the browser's [MIME enforcement](https://fetch.spec.whatwg.org/#x-content-type-options-header) and [service-worker request and scope rules](https://w3c.github.io/ServiceWorker/#service-worker-script-request). Prove them against malicious upstream bodies and headers, rather than trusting B's configuration.

### Recipient secrets

The CLI generates a 256-bit random secret and retains it locally. UUID routes; the secret authorizes; a separate owner credential protects registration and reconnection.

| Mode | Owner sends | Recipient does |
| --- | --- | --- |
| One-click | `https://runlink.dev/{uuid}#{secret}` | Opens link |
| Separate secret | `https://runlink.dev/{uuid}` and the same generated secret separately | Opens link and enters secret in the loader's password field |

The loader captures any fragment into memory and immediately calls `history.replaceState` with a fragment-free URL and secret-free state, before connection setup or other application work. Clear an entered secret from the password field after capture. Keep recipient secrets and session keys only in memory. Exclude them, and owner credentials, from forwarded peer signaling, telemetry, logs, persistent browser storage, and history state. The nonsecret recovery handles in [ADR 0005](0005-run-recovery-and-supervision.md) are the sole planned persistent browser task state.

After reload or session restoration, require the recipient to re-enter the secret or reopen the original secret-bearing link, then authenticate before recovering a run. Password entry changes delivery only, not secret generation. [Fragments](https://www.rfc-editor.org/rfc/rfc3986.html#section-3.5) are absent from ordinary HTTP requests, and [`replaceState`](https://html.spec.whatwg.org/multipage/nav-history-apis.html#dom-history-replacestate) cleans the current history entry. Neither guarantees the original navigation was never recorded, restored, or synchronized. Document that limitation alongside one-click sharing; separate delivery keeps the secret out of the URL.

### GUI verification

| Layer | Control |
| --- | --- |
| Release approval | Review/test a self-contained GUI bundle; embed its SHA-256 hash in the trusted loader's approved list and its bytes in the CLI; cover all executable assets |
| Before execution | Verify received bytes against loader-approved hashes; reject unknown, altered, or withdrawn versions before execution; never accept expected hashes from the owner |
| Browser execution | Strict hash-based CSP, no `eval` or arbitrary inline scripts, safe DOM APIs, and Trusted Types where supported; no analytics, tag managers, or third-party runtime scripts |
| Untrusted content | Validate definitions and output as data; render safely; deliver active files as downloads; no arbitrary owner HTML/JS in the shared origin |
| Release pipeline | Minimal pinned dependencies, vulnerability checks, reviewed CI, signed immutable artifacts verified by deployment, separate release credentials, hardware-key-protected maintainer accounts |

See [CSP](https://www.w3.org/TR/CSP3/) and [Trusted Types](https://www.w3.org/TR/trusted-types/). Obtain independent review of authentication before public launch.

## Consequences

Anyone holding the secret-bearing link has its permissions. Expiry and revocation deny new runs and further data access, including existing sessions; owners separately control stopping active runs. Downloaded data cannot be recalled.

The loader remains the initial trusted code. Compromise of A, the release system, the owner machine, or the recipient browser can expose secrets and plaintext. Hashes identify bytes, not absence of bugs; [integrity checks](https://www.w3.org/TR/sri/) cannot protect a replaced verifier. Recipients need no extension, key management, account, or per-invocation owner approval.
