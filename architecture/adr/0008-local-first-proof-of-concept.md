# ADR 0008: Local-first proof of concept with infrastructure stand-ins

## Status

Accepted. Recorded 2026-10-07. Defers the [external setup](../../docs/operations.md#external-setup-in-order);
the production design in ADRs [0002](0002-webrtc-transport.md) through
[0007](0007-local-shipping-foundation.md) stays the target.

## Context

The product idea is not yet validated. Provisioning servers, DNS, certificates,
coturn, and hosted CI costs time and money before the publish, connect, and run
flow exists. Every component except the network itself can run on one machine,
and the network effects can wait for a validated idea.

## Decision

Build and iterate on the proof of concept on one machine. Run the production
components and protocols; replace only what needs infrastructure.

| Production | Local stand-in | Not exercised locally |
| --- | --- | --- |
| A at `https://runlink.dev`: ACME TLS, DNS, public listener | `runlink-server` frontend role on `http://127.0.0.1:8080`; loopback is a browser secure context | TLS, HSTS, certificates, DNS, public reachability |
| B on the private network | `runlink-server` app role on `127.0.0.1:8081`, behind A over loopback | Private-network firewall, separate hosts |
| C: coturn with TURN REST credentials | Pion TURN server on `127.0.0.1:3478`, UDP and TCP, with coturn's credential scheme, relay port range, per-credential and total allocation quotas, and UDP-only relays; relays reach only loopback peers | TURN TLS, IPv6, NAT traversal, the production destination policy and firewall, bandwidth caps, real latency and loss |
| TURN secret delivered to B and C | Generated once into `var/dev/turn-secret` (`0600`), same format | Secret distribution |
| systemd units | One `runlink-dev` supervisor; any service failure stops all | Restart policy, service isolation |
| Signed releases and deployment | Not part of the development loop; the release tooling stays locally tested | Real machines and rollout |

Rules:

- Product code has no development-mode branches. Local and production runs differ
  only in configuration: listen addresses, upstream, state directory, TURN URIs
  and secret, and the owner CLI's service origin. Development builds default that
  origin to the local stack; release builds stamp `https://runlink.dev`;
  `RUNLINK_SERVICE` overrides both.
- Stand-ins listen only on loopback. They lack the exposure controls of the
  production services they replace.
- `cmd/runlink-dev` and `internal/devstack` are development tools. Release
  archives contain only `runlink` and `runlink-server`.
- End-to-end tests start the stack in-process with `devstack.Start` (Go) or as
  `runlink-dev` (browser tests), on free ports with temporary state.

The external setup resumes once the proof of concept validates the idea. Proofs
in the [implementation plan](../implementation.md#required-proofs) that need real
networks or machines stay open until then.

## Consequences

Iteration needs no cloud account, cost, or credentials, and the whole flow can be
tested in one process. A proof of concept that works locally can still fail across
real networks: NAT traversal, TURN TLS, firewall enforcement, certificate issuance,
latency, and relay cost are untested until the external setup. Pion and coturn
may differ in edge cases beyond the shared credential scheme and bounds;
`scripts/turn-check.py` keeps exercising the production coturn configuration where
the pinned coturn is available.
