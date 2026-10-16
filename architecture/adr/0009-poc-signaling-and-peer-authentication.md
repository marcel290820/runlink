# ADR 0009: Proof-of-concept signaling and peer authentication

## Status

Accepted. Recorded 2026-10-13. Chooses the authentication protocol that
[ADR 0002](0002-webrtc-transport.md) leaves open, for the local proof of concept
([ADR 0008](0008-local-first-proof-of-concept.md)). It needs the independent review
[ADR 0003](0003-trusted-browser-client.md#gui-verification) requires before public launch.

## Context

The first end-to-end path needs a wire protocol between the owner CLI, B, and the
browser, and a way for browser and runner to prove they share the link secret.
ADR 0002 forbids home-made cryptographic primitives and requires binding to the
task and the actual WebRTC peers, with freshness and replay protection. The
browser offers WebCrypto but no DTLS keying-material exporter.

## Decision

### Signaling

B serves two WebSocket routes; A forwards only these upgrades. A terminates each
WebSocket itself, opens the same route on B, and relays only JSON text messages up
to 64 KiB, so B's handshake headers never reach the browser and the browser's
headers never reach B.

| Route | Peer | Messages |
| --- | --- | --- |
| `/api/v1/share` | Owner CLI, for as long as it shares | B sends `{task}`, a fresh UUID, or `{error: "capacity_exceeded"}`; then `{session, sdp, ice}` per recipient; the CLI replies `{session, sdp}`, an empty `sdp` declining |
| `/api/v1/connect/{uuid}` | Browser, once per connection | B sends `{ice}`; the browser sends `{sdp}`; B returns the owner's `{sdp}` and closes, or `{error}`: `unavailable` when nobody shares the UUID, `unanswered` when the owner declines or does not answer within 30 seconds |

Descriptions carry all their candidates; nothing trickles. B issues every peer its
own coturn TURN REST credential, valid for one hour. Registrations live in memory
for as long as the owner's connection, with one global cap.

### Peer authentication

The link secret is 32 random bytes in unpadded base64url. Over the open data
channel, before any task data:

1. The browser sends `{nonce: Nb}`.
2. The runner replies `{nonce: Nr, proof: P("runner")}`. The browser verifies it.
3. The browser sends `{proof: P("browser")}`. The runner verifies it.

`P(role)` is HMAC-SHA256 keyed by the secret over the newline-joined fields
`runlink-auth-v1`, role, task UUID, browser fingerprint, runner fingerprint, `Nb`,
and `Nr`. Nonces are 32 random bytes; proofs and nonces use unpadded base64url.
A fingerprint is the SHA-256 DTLS certificate fingerprint from a peer's session
description, as lowercase hex; a description declaring any other or a second
fingerprint is refused, because DTLS accepts a certificate matching any of them.
Each side takes its own fingerprint from its local description and the peer's
from the remote one, which DTLS has verified.

- Possession: only a secret holder can compute either proof.
- Binding: an attacker relaying the connection holds different certificates, so
  the two peers' transcripts differ and no proof verifies. The task UUID is bound too.
- Freshness and replay: each proof covers both sides' fresh nonces.
- Reflection: the role is part of the transcript.

Browsers use `crypto.subtle` HMAC sign and verify; the runner uses Go's `crypto/hmac`
with a constant-time comparison. The runner sends the GUI and definition, and the
browser sends input, only after verifying. Afterwards the authenticated DTLS
channel carries everything.

### Data channel

One reliable, ordered channel. The runner sends the GUI as one binary message,
then `{definition}`. Each `{values}` from the browser starts one run, answered by
`{stage: "accepted"}`, `{stage: "running", message?}`, the output's bytes as binary
messages of up to 16 KiB, and `{done: true}` or `{error: {code, message?, fields?}}`.
`{values}` announces each file as `{name, size}`; after `accepted`, the browser
sends the files' bytes in input order as binary messages of up to 16 KiB, and the
runner ends a session that sends more than it announced or not within the task's
timeout.

Both sides bound what the other can make them hold. The runner reads messages of
up to 64 KiB, waits on writes the browser does not take, ends a session whose
write waits 15 seconds, and drops progress lines a slow browser cannot take rather
than stall the script. The browser sends at most 64 KiB per message, accepts at
most 1 MiB of output and 1000 control messages per run, counts every message as
at least 1 KiB, and ends a connection whose authentication and GUI do not arrive
within 30 seconds.

## Consequences

The protocol uses only standard primitives and needs no new browser or Go
library. Its composition is ours and must pass independent review before public
launch, as must its implementations. A link secret leaked with its link grants
access until the owner stops sharing; there is no revocation besides stopping, and
restarting sharing issues a new UUID and secret.

The registry does not survive B restarts and has no owner credential or per-owner
quota yet ([ADR 0004](0004-bounded-task-registry.md)). Established sessions need
neither A nor B, but a relayed session ends when its TURN credential expires.
Without trickle ICE, a slow candidate is left out after five seconds of gathering.
