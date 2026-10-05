# ADR 0007: Local shipping foundation

## Status

Accepted. Recorded 2026-10-04. Scaffold behavior is implemented; deployment and
product protocols still require the proofs in the implementation plan.

## Context

V1 development needs executable local checks and reviewable deployment artifacts
before registration, peer authentication, or task execution is introduced.
The trusted frontend must enforce its boundary independently of B.

## Decision

Use the same standard-library Go server binary with separate `app` and `frontend`
roles. A's trusted role embeds immutable loader bytes, GUI approvals, fixed routing,
and upstream response controls. Go terminates public TLS on A; certbot obtains and renews certificates through
restricted certificate copies and a service restart. B listens privately.
The status API is the only forwarded scaffold route. Keep upgrades and future
product routes closed until their protocols and boundary checks land.

Pin workspace tools, provider checksums, development dependencies, and CI actions.
Local development and hosted Linux/macOS runners use one shared check gate.
Use OpenTofu and rendered cloud-init for three new role-separated VMs; never let
validation commands provision or contact a cloud account.

Package deterministic archives for both binaries and all four supported targets.
Sign the checksum manifest with a separately controlled RSA/SHA-256 key. Deployment
verifies the independently trusted public key, installs an immutable version, and
atomically switches the active symlink. Runtime identities cannot write releases.
Retain signed artifacts to verify explicit rollback and restore the prior version
when a post-switch check fails.

## Consequences

The scaffold is usable for development and release exercises, but it does not
publish tasks or implement the protocols in ADRs 0002, 0004, and 0005. Kernel
firewall enforcement, TURN relay paths, production TLS, GitHub runners, and machine
operations require external verification. Public-key distribution, signing access,
and release review remain maintainer responsibilities.
