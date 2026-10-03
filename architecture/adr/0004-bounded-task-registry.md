# ADR 0004: Durable, bounded UUID registration

## Status

Accepted. Recorded 2026-10-02.

## Context

Task links need stable routing after application-server restarts. Connection and request-rate limits do not prevent an admitted owner from accumulating persistent registrations until storage is exhausted.

## Decision

Use one application-server instance and a durable UUID-to-owner registry. Authenticate owners with credentials separate from recipient secrets. Generate and atomically reserve a UUID for its owner before acknowledging registration. Reconnection authenticates the same owner and restores routing; it does not create another registration. Active signaling connections stay in memory.

Enforce per-owner and global caps on both registration count and stored bytes, including retained registry metadata. Bound each record and any journals/backups. Check quotas atomically with reservation so concurrent requests cannot exceed them. Set numeric caps and reserve disk headroom for updates, cleanup, and recovery before exposing registration publicly.

At capacity, reject new registrations with an explicit `capacity_exceeded` result and no partial reservation. Preserve existing registrations and allow reconnection, reads, and authenticated cleanup. Never evict another owner's task to admit a new one. Request-rate and connection limits apply in addition to storage caps.

Provide idempotent authenticated unregister for the owning credential. The runner revokes local access before unregistering because removing routing does not stop existing peer sessions. Unregister removes routing and reclaims durable storage through bounded cleanup. Offline owners retain registrations within their quota until unregistering; there is no automatic deletion merely for disconnecting. Owners cannot claim a chosen UUID; republishing gets a fresh random UUID and secret.

## Consequences

Restarts restore ownership before accepting new registrations, then rebuild routing as owners reauthenticate. Registry backups need a tested restore path. Owners must remove unused registrations at their quota; global exhaustion rejects new publishing while existing tasks remain recoverable. Multiple application-server replicas require a new registry/signaling decision.
