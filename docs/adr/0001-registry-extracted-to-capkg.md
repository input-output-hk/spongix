# 1. Package registry extracted to capkg

Date: 2026-06-16

## Status

Accepted

## Context

Spongix carried a per-namespace flake registry under `/{ns}/f/` (~490
production lines, roughly a quarter of the main package). The registry shared
no runtime machinery with the cache proxy: it never touched NARs, narinfos,
chunks, or desync. Its only coupling to the cache was auto-pinning each
registered closure against GC, and its generated `flake.nix` referenced the
cache purely through a `fromStore` URL.

That URL coupling was the whole of the relationship, which meant the registry
was already nearly binary-cache-agnostic. Keeping it embedded tied a generic,
reusable service to spongix's release cycle, config schema, and Pebble layout
for no technical benefit.

## Decision

Extract the registry into its own project, **capkg**
(`ssh://forgejo@forgejo.alpha.internal:2222/manveru/capkg.git`), as a standalone
HTTP service. Spongix drops all registry code and becomes purely a binary
cache plus a Pin API.

Consequences of making it cache-agnostic:

- **Pinning becomes the publisher's job.** Registration no longer protects a
  closure from cache GC. The `capkg.nu` client pins the `fromPath` against the
  cache's Pin API (when one is configured) as part of `push`/`sync`, and does a
  reference-counted unpin on `delete`. capkg never talks to the cache.
- **`fromStore` comes from capkg's own per-namespace `store_url` config**
  (overridable per PUT), so a namespace can point at spongix, a raw S3 bucket,
  or any HTTP substituter interchangeably.
- The symlink builder binary is now `go:embed`-ed into capkg rather than
  injected via ldflags from a Nix store path.

No URL back-compat shim and no data migration: consumers are re-locked against
the new registry host, and the registry is repopulated with one `capkg sync`.

## Alternatives considered

- **Keep it embedded, just tidy it.** Rejected: preserves the coupling and the
  single release cycle without addressing the cache-agnostic goal.
- **Registry consults the cache's GC, or fires a pin webhook.** Both invert or
  retain a cache-integration protocol inside the registry. Rejected in favour
  of a pure-metadata registry with pinning pushed entirely to the publisher.
- **Spongix reverse-proxies `/{ns}/f/`.** Rejected: permanent indirection and
  residual registry knowledge in spongix.

## Consequences

- Spongix is smaller and has a single responsibility; the Pin API and its
  bearer token remain.
- `spongixctl.nu` is now a thin pin-only client; publishing commands moved to
  `capkg.nu` in the capkg repo.
- A bug is left behind, not reproduced: spongix's old `registryDelete` unpinned
  closures unconditionally despite a comment claiming last-reference semantics.
  capkg's client reference-counts surviving versions before unpinning.
- Stale `reg/...` keys may remain in existing spongix Pebble databases; they are
  inert (no code reads them) and can be dropped in a one-off cleanup.
