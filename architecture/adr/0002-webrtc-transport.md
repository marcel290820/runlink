# ADR 0002: WebRTC data channels with signaling and TURN fallback

## Status

Accepted. Recorded 2026-10-02.

## Context

Owners should not configure inbound ports. Direct transfer avoids server payload bandwidth, but some networks require a relay. Discovering an owner by UUID alone does not establish peer connectivity.

## Decision

Use reliable, ordered WebRTC data channels between browser and runner, with no retransmission-count or message-lifetime limit. Use coturn for STUN discovery and TURN fallback as specified in [ADR 0006](0006-hetzner-deployment.md).

The application server routes UUIDs and exchanges initial offers, answers, and ICE candidates. It does not perform peer connectivity checks. Close browser signaling as soon as the initial exchange finishes, including trickled candidates; do not wait for the data channel to open or authentication to finish. ICE checks, DTLS, and channel establishment may overlap candidate exchange.

The owner keeps one registration/signaling connection for new recipients. The browser reopens signaling when a new connection or recovery needs fresh ICE negotiation. Established sessions, GUI transfer, task commands, results, and local access checks require no application-server connection. TURN remains a dependency for a relayed session.

Before any task data flows, mutually authenticate secret possession, bound to the task and actual WebRTC peers, with freshness and replay protection. Never send the secret over an unauthenticated connection. Select a reviewed browser/Go protocol and libraries before implementation; do not create custom cryptographic primitives. WebRTC DTLS alone does not authenticate peers against malicious signaling. See [WebRTC security](https://www.rfc-editor.org/rfc/rfc8827.html#section-9.1).

## Consequences

Signaling or TURN compromise can deny service and expose routing, timing, and traffic metadata. With the required authentication, UUIDs and setup traffic do not grant invocation or decryption rights. The browser and runner still handle plaintext.

Reliable channels recover packet loss, not permanent disconnection; [run recovery](0005-run-recovery-and-supervision.md) is an application responsibility. See the [data-channel specification](https://www.w3.org/TR/webrtc/#rtcdatachannel).

This decision replaces the earlier TLS passthrough/per-owner certificate and all-payload WebSocket relay approaches.
