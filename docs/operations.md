# Releases and infrastructure

All commands here are operator instructions. The local check gate never provisions
resources, changes DNS, deploys remotely, or changes host services.

## Package, sign, and verify

```bash
scripts/check.sh
scripts/release.sh package --version v0.1.0 --output dist/v0.1.0
scripts/release.sh sign dist/v0.1.0 --private-key /path/to/release-signing.key
scripts/release.sh verify dist/v0.1.0 --public-key /path/to/trusted-release.pub
```

Packaging builds both executables for Linux/macOS amd64/arm64 and includes the
reviewable browser assets and release metadata. Archives normalize timestamps,
owners, ordering, and modes. Metadata records the source revision and dirty state;
a clean reviewed source tree is required by the operator for a public release.
No script commits or pushes it.

The RSA/SHA-256 signature covers the complete checksum manifest. Generate a new
3072-bit-or-larger signing key on the designated signing system; use a protected key
or hardware-backed signing process before public shipping. CI checks do not receive
the signing key. Distribute and verify the public key fingerprint independently;
never trust a key supplied alongside a downloaded release. Keep the trusted key
outside the release operator's writable installation tree on deployed machines.
Verification rejects unsigned extras, duplicate/unsafe manifest entries, checksum
mismatches, and invalid signatures. Extraction rejects links, traversal, duplicates,
unexpected paths, and oversized expanded archives.

## Local deployment and rollback

The deploy command operates on a directory on the machine running it. It has no SSH
or remote deployment behavior. The release operator transfers the signed directory
and repository deployment scripts separately after obtaining deployment authorization.

```bash
scripts/release.sh deploy dist/v0.1.0 \
  --root /permitted/path/runlink --version v0.1.0 --platform linux-amd64 \
  --public-key /path/to/trusted-release.pub
scripts/release.sh rollback --root /permitted/path/runlink \
  --version v0.0.9 --platform linux-amd64 --public-key /path/to/trusted-release.pub
```

Both commands verify signatures before changing `current`. Deploy installs into
`releases/VERSION`, stores signed rollback evidence, and atomically switches the
symlink under a deployment lock. Existing versions are immutable; use a new version
for another build. Rollback compares installed bytes to the signed archive before
switching. No command removes prior releases or application state.

On an authorized new Runlink VM, use `/opt/runlink` as the root and add
`--restart runlink-frontend --smoke-url https://runlink.dev` on A, or
`--restart runlink-app --smoke-url http://10.77.0.20:8081` on B. These flags explicitly
request systemd changes. A failed smoke or restart restores the previous link and
restarts the previous service when available. Smoke checks retry for up to five
seconds to allow service startup. Run
`scripts/release.sh smoke https://runlink.dev` afterward. Grant the release operator
only the specific restart permission needed; runtime users receive none.

A rejected first installation restores no active link and stops the newly restarted
unit when `--restart` was requested. Inspect its status before any retry. The failed version remains installed for inspection; use a
new version. Keep deployment scripts and the trusted verification key under
administrator ownership; review them before execution. Never replace the runtime
service with an unverified binary or share A release access with B's runtime identity.

## External setup, in order

1. Review [the pins](../scripts/tools.lock.json), the provider lockfile, infrastructure,
   and signed browser release. Run the local shared gate; then execute the workflow
   on GitHub to verify the Linux and macOS hosted environments. Configure no paid
   features or branch protection as part of this foundation.
2. Create a dedicated Hetzner project and separate administrator/release SSH keys.
   Supply their **public** keys, restricted administrator CIDRs, ACME email, domains,
   and chosen eu-central location in a private ignored `infra/terraform.tfvars`.
   Set `HCLOUD_TOKEN` only in the operator's ephemeral environment. No token, TURN
   shared secret, TLS private key, or release signing private key belongs in tfvars,
   cloud-init, the provider lockfile, or Git.
3. After explicitly authorizing provisioning and cost, run `tofu -chdir=infra init`,
   `tofu -chdir=infra plan -out=runlink.tfplan`, review exactly three servers and their
   protected public IPs, and run `tofu -chdir=infra apply runlink.tfplan`. Protect local
   state and plan files and establish a protected state backup for this new stack.
   No real plan/apply has been run locally by this task.
4. Set the loader domain's A/AAAA records to A and the TURN domain's A/AAAA records
   to C from `tofu output addresses`. B has no public application listener. Inspect
   cloud-init completion and package updates on each new VM. Verify the host firewall
   before starting services: Hetzner firewalls do not protect private network traffic.
5. Install the separately verified signing public key and deployment scripts under
   administrator control. On A, obtain its certificate with `certbot certonly
   --standalone --preferred-challenges http --non-interactive --agree-tos
   --email OPERATOR_EMAIL -d LOADER_DOMAIN` after DNS and TCP 80 are reachable.
   Deploy the signed Linux amd64 release to A and B without restarting A yet.
   Run `/etc/letsencrypt/renewal-hooks/deploy/runlink-tls` on A to install restricted
   certificate copies and start/restart `runlink-frontend`; use Go HTTPS on port 443.
   Start B's `runlink-app` unit. Test HTTPS smoke after A starts.
6. On C, obtain its certificate using the same standalone certbot command with
   `TURN_DOMAIN`. Supply a new random 32-byte shared secret as 64 lowercase hex
   characters in `/etc/runlink/turn-secret`, root-owned, group `runlink`, mode `0640`.
   Run `/etc/letsencrypt/renewal-hooks/deploy/runlink-tls` on C to install restricted
   certificate copies and start/restart `runlink-turn`. B will receive the same secret
   separately when credential issuance is implemented. The scaffold never issues TURN
   credentials. On both A and C, enable/verify the certbot renewal timer and test renewal.
   `/etc/letsencrypt` holds persistent ACME state; `/etc/runlink/tls` holds the restricted
   runtime copies. TCP 80 is reserved for issuance/renewal, not an application listener.
7. Confirm A's public HTTPS and health/readiness; confirm B:8081 is reachable only
   from A's private address. Attempt unknown routes, upgrades, worker script requests,
   and malicious B responses through the actual TLS ingress. Verify runtime accounts
   cannot alter `/opt/runlink`, ingress config, the public verification key, or releases.
8. On disposable test VMs, validate nftables with kernel privileges, then test direct,
   UDP TURN, and TCP/TLS TURN from representative IPv4/IPv6 networks. Restrict both
   peers to C and TURN TCP/TLS, confirm a same-C relay-to-relay selected path, and
   prove private/link-local/loopback/multicast and A/B/C management/listener destinations
   cannot be reached through allocations. C's sole self-address exception is UDP to
   ports 49160–49259. `no-tcp-relay` disables TCP peer allocations while preserving
   TCP/TLS client transport to UDP allocations.
9. Set availability, certificate expiry, traffic/cost alerts, OS/coturn update ownership,
   and a restore drill for future registry state. Test service restart, signed rollout,
   failed smoke recovery, and rollback on the actual machines. Complete the product
   authentication review and all remaining [architecture proofs](../architecture/implementation.md)
   before public task publishing.

## Runtime ownership and limits

| Server | Runtime and persistent paths | Network |
| --- | --- | --- |
| A | `runlink` trusted Go HTTPS frontend; `/opt/runlink/releases`, `/etc/letsencrypt`, restricted `/etc/runlink/tls` | Public TCP 443; TCP 80 for certificate renewal; fixed API to B |
| B | `runlink` app; reserved private `/var/lib/runlink` | Private 8081 from A; administrator SSH only publicly |
| C | `runlink` coturn UID 980; `/etc/runlink/turn-secret`, restricted TLS copies; `/var/lib/runlink-turn`; SQLite state in `/var/lib/runlink-turn/turn.sqlite`; PID and generated config in `/run/runlink-turn` | UDP/TCP 3478, TCP TLS 5349, UDP relay 49160–49259; TCP 80 for certificate renewal |

TURN is bounded to 100 allocations, four per credential username, ten-minute
allocation lifetime, 1 MiB/s per allocation, and 50 MiB/s aggregate. These bandwidth
settings use bytes per second. Issuance rate/credential lifetimes and abuse controls
remain product work on B; no public issuance endpoint exists. Renewed allocations
still require traffic/cost monitoring. Coturn is pinned to Ubuntu's 4.6.1-1build4 package
for this baseline; OS security updates are enabled and operators must review future
package changes and rerun connectivity proofs. The Ubuntu 24.04 image is an updated
OS baseline rather than an immutable image snapshot.
