---
id: TASK-1
title: Introduce StorePathHash32 and NarHash52 types in store package and fix GC bug
status: Done
assignee:
  - sub-agent-1
created_date: '2026-03-10 13:13'
updated_date: '2026-03-10 14:47'
labels:
  - reviewed
dependencies: []
references:
  - pkg/store/gc.go
  - pkg/store/hashes.go
  - pkg/store/refs.go
  - pkg/store/pins.go
  - pkg/store/store.go
  - pkg/store/index.go
  - pkg/store/batching.go
  - pkg/store/iface.go
  - router.go
  - registry_handlers.go
  - main.go
  - cmd/gc/main.go
priority: high
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Context

The GC has a type confusion bug: `deleteUnreachableIndices` calls `extractStorePathHash` on ALL index paths, but NAR indices use 52-char NarHash in their path (not 32-char StorePathHash). The reachable set contains StorePathHash32 values, so NAR indices are NEVER matched and always deleted — even when their narinfo is pinned. This causes incomplete NAR responses in production.

Root cause: both hash types are `string`, so `extractStorePathHash` returns whatever the last path component is, and the caller has no way to distinguish which hash type it got.

## Scope and Dependencies

**TASK-1 changes exported API signatures** (`PinHash`, `UnpinHash`, `IsPinned`, `LoadPinnedHashes`, `GCConfig.PinnedHashes`, `SetNarHashMapping`, `GetStorePathByNarHash`, `CommitWithIndex`, `StoreIndexWithRefs`, `RefTrackingIndexStore`). These are called from the main package (`router.go`, `main.go`, `registry_handlers.go`, `cmd/gc/main.go`). After TASK-1, `go build ./...` will fail until TASK-2 propagates the new types to callers. **TASK-1 and TASK-2 must be committed atomically** (or squashed) so the tree never has a broken build on main.

## Files

- **Create**: `pkg/store/hashes.go` — domain types
- **Modify**: `pkg/store/refs.go` — internal key functions, extractStorePathHash
- **Modify**: `pkg/store/pins.go` — PinHash, UnpinHash, IsPinned, PinEntry, LoadPinnedHashes
- **Modify**: `pkg/store/store.go` — GetStorePathByNarHash, SetNarHashMapping
- **Modify**: `pkg/store/index.go` — addIndexEntries, StoreIndexWithRefs
- **Modify**: `pkg/store/batching.go` — CommitWithIndex
- **Modify**: `pkg/store/iface.go` — RefTrackingIndexStore interface
- **Modify**: `pkg/store/gc.go` — GCConfig, findRoots, transitiveClose, deleteUnreachableIndices, sizeEvict
- **Modify**: `pkg/store/gc_test.go`, `pkg/store/store_test.go`, `pkg/store/pins_test.go`, `pkg/store/refs_test.go`, `pkg/store/batching_test.go`

## Step 1: Create pkg/store/hashes.go

```go
package store

// StorePathHash32 is the 32-character nix-base32 hash from a Nix store path.
// Example: "plsmwx39r785b1dyblqp4lv81gbdpy4c"
type StorePathHash32 string

// NarHash52 is the 52-character nix-base32 hash of a NAR file.
// Example: "0bmwc9fqyilxy46nmvy5wkdfbkqwvi7y80wlw5nks7l7xydzkfnp"
type NarHash52 string

func (h StorePathHash32) String() string { return string(h) }
func (h NarHash52) String() string       { return string(h) }
```

No validation constructors — the mux route patterns already validate the character set. The types exist to prevent mixing, not to parse.

## Step 2: Update refs.go internal functions

Change signatures:
- `narRefKey(storePathHash StorePathHash32) []byte`
- `narHashKey(narHash52 NarHash52) []byte`
- `narHashReverseKey(storePathHash StorePathHash32) []byte`
- `encodeNARRefs(refs []StorePathHash32) []byte`
- `decodeNARRefs(data []byte) []StorePathHash32`
- `extractStorePathHash(name string) string` — keep returning raw string (it extracts the LAST path component regardless of type; callers must cast)
- `GetNARRefs(storePathHash StorePathHash32) ([]StorePathHash32, error)`
- `DeleteNARRef(storePathHash StorePathHash32) error`

For `encodeNARRefs`/`decodeNARRefs`, update the encode to join `StorePathHash32` values and decode to return `[]StorePathHash32`.

## Step 3: Update pins.go

- `PinEntry.Hash` → `StorePathHash32`
- `pinKey(hash StorePathHash32) []byte`
- `PinHash(hash StorePathHash32, label string) error`
- `UnpinHash(hash StorePathHash32) error`
- `IsPinned(hash StorePathHash32) (bool, error)`
- `ListPins() ([]PinEntry, error)` — Hash field is now StorePathHash32
- `LoadPinnedHashes(path string) (map[StorePathHash32]struct{}, error)`

## Step 4: Update store.go

- `GetStorePathByNarHash(narHash52 NarHash52) (StorePathHash32, error)` — returns empty StorePathHash32 on not-found (returns `("", nil)`, NOT an error)
- `SetNarHashMapping(narHash52 NarHash52, storePathHash StorePathHash32) error`

## Step 5: Update index.go (addIndexEntries and StoreIndexWithRefs)

- `addIndexEntries(batch, name string, idx desync.Index, narRefs []StorePathHash32, narHash52 NarHash52) error`
- `StoreIndexWithRefs(name string, idx desync.Index, narRefs []StorePathHash32) error`

## Step 6: Update iface.go

- `StoreIndexWithRefs(name string, idx desync.Index, narRefs []StorePathHash32) error`

## Step 7: Update batching.go

- `CommitWithIndex(name string, idx desync.Index, narRefs []StorePathHash32, narHash52 NarHash52) error`

## Step 8: Fix gc.go — THE CRITICAL BUG FIX

Update type signatures:
- `GCConfig.PinnedHashes map[StorePathHash32]struct{}`
- `findRoots` returns `map[StorePathHash32]struct{}`
- `transitiveClose` accepts/returns `map[StorePathHash32]struct{}`
- `deleteUnreachableIndices` accepts `map[StorePathHash32]struct{}`
- `sizeEvict` internal allPins uses `StorePathHash32`

**The fix in `deleteUnreachableIndices`**: When scanning indices, detect the index kind from the path prefix. For NAR indices, resolve the path hash (NarHash52) to a StorePathHash32 via `GetStorePathByNarHash` before checking the reachable set:

```go
// In deleteUnreachableIndices, replace the current hash extraction + reachability check:
hash := extractStorePathHash(indexPath)
if hash == "" {
    continue
}

// Resolve to StorePathHash32 based on index kind.
var storeHash StorePathHash32
if strings.HasPrefix(indexPath, "indices/nar/") {
    // NAR index paths use NarHash52 — resolve via nh/ mapping.
    resolved, err := db.GetStorePathByNarHash(NarHash52(hash))
    if err != nil {
        // DB error reading nh/ mapping; treat as unreachable so we don't
        // block the entire GC run for a transient read failure.
    } else {
        storeHash = resolved
    }
} else if strings.HasPrefix(indexPath, "indices/narinfo/") {
    storeHash = StorePathHash32(hash)
} else {
    // Realisations and logs use non-StorePathHash formats (64-char SHA256
    // hex for realisations, "hash-name" for logs). Leave storeHash empty
    // so they always fail the reachability check and get deleted, matching
    // current behavior.
}

if storeHash != "" {
    if _, ok := reachable[storeHash]; ok {
        continue
    }
}
```

**nh/nhr cleanup for unreachable NAR indices**: The existing generic cleanup (lines 416-426) constructs wrong keys because `hash` is a NarHash52 but `narRefKey` and `narHashReverseKey` expect StorePathHash32. Replace the existing nh/nhr cleanup block with kind-specific logic:

```go
// Clean up r/, nh/, and nhr/ entries based on index kind.
if strings.HasPrefix(indexPath, "indices/nar/") {
    // NAR indices don't have r/ entries (only narinfos do).
    // Delete nh/<NarHash52> directly.
    _ = batch.Delete(narHashKey(NarHash52(hash)), nil)
    // If we resolved the StorePathHash32 earlier, also delete nhr/.
    if storeHash != "" {
        _ = batch.Delete(narHashReverseKey(storeHash), nil)
    }
} else if strings.HasPrefix(indexPath, "indices/narinfo/") {
    // Narinfo: delete r/ and look up nhr/ -> nh/ as before.
    _ = batch.Delete(narRefKey(storeHash), nil)
    nhrKey := narHashReverseKey(storeHash)
    if nhrVal, nhrCloser, nhrErr := db.db.Get(nhrKey); nhrErr == nil {
        nh52 := make([]byte, len(nhrVal))
        copy(nh52, nhrVal)
        _ = nhrCloser.Close()
        _ = batch.Delete(narHashKey(NarHash52(string(nh52))), nil)
        _ = batch.Delete(nhrKey, nil)
    }
}
// Realisations/logs: no r/, nh/, or nhr/ entries to clean up.
```

**Also fix `sizeEvict`**: same issue — it scans narinfo indices only, so it's correct. But the `allPins` map needs typed keys.

**Also fix `findRoots`**: only scans `i/indices/narinfo/` — correct, but type the return.

## Step 9: Update all store package tests

Every test that uses raw string hashes in function calls must cast to `StorePathHash32` or `NarHash52`. Examples:

- `gc_test.go`: `GCConfig{PinnedHashes: map[StorePathHash32]struct{}{"aaaa...": {}}}`
- `pins_test.go`: `db.PinHash(StorePathHash32("aaaa..."), "label")`
- `refs_test.go`: `narRefKey(StorePathHash32("hash"))`, `rt.GetNARRefs(StorePathHash32("hash"))`
- `store_test.go`: `db.GetStorePathByNarHash(NarHash52("hash"))`
- `batching_test.go`: `batcher.CommitWithIndex(name, idx, nil, "")` → `batcher.CommitWithIndex(name, idx, nil, NarHash52(""))`

## Step 10: Run tests

```bash
go vet ./pkg/store/
go test ./pkg/store/ -v -count=1
```

All store package tests must pass, including `TestGCDeletesReachableNARWhenHashesDiffer`. Note: `go build ./...` will fail at this point because main-package callers still use raw strings — that is expected and resolved by TASK-2.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 StorePathHash32 and NarHash52 are distinct named types (not aliases)
- [x] #2 SetNarHashMapping(NarHash52, StorePathHash32) is impossible to call with swapped args
- [x] #3 TestGCDeletesReachableNARWhenHashesDiffer passes
- [x] #4 All existing store package tests pass
- [x] #5 go vet ./pkg/store/ passes
- [x] #6 GC resolves NAR index NarHash52 to StorePathHash32 via GetStorePathByNarHash (nh/ mapping) before reachability check
- [x] #7 Unreachable NAR index deletion also cleans up nh/<narHash52> and nhr/<storePathHash32> mapping entries
- [x] #8 TASK-1 and TASK-2 are committed atomically so main branch never has a broken build
<!-- AC:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
## Review Round 1

### Verdict: REVISE -- one critical gap, two medium issues

### Bug Verification: CONFIRMED
The bug is real. `TestGCDeletesReachableNARWhenHashesDiffer` fails as described: NAR indices are always deleted because `extractStorePathHash` returns a 52-char NarHash for NAR index paths, which never matches the 32-char StorePathHash values in the reachable set.

### File and Signature Verification: PASS
All 8 referenced source files exist. All current type signatures match the codebase exactly. `pkg/store/hashes.go` does not exist yet (correct -- task says to create it).

### Issues Found

**CRITICAL -- TASK-1 breaks `go build ./...` with no mitigation plan**

TASK-1 changes exported API signatures (`PinHash`, `UnpinHash`, `IsPinned`, `LoadPinnedHashes`, `GCConfig.PinnedHashes`, `SetNarHashMapping`, `GetStorePathByNarHash`, `CommitWithIndex`, `StoreIndexWithRefs`, `RefTrackingIndexStore`). These are all called from the main package (router.go, main.go, registry_handlers.go, cmd/gc/main.go). Step 10 only runs `go test ./pkg/store/`, which will pass in isolation, but `go build ./...` will fail until TASK-2 is completed.

The task must either: (a) be merged with TASK-2 into a single atomic task, or (b) explicitly state that TASK-1 and TASK-2 must be committed together and add `go build ./...` as a verification step after both are done, or (c) keep the store package API as `string` externally and only use the typed values internally (defeats the purpose).

Recommendation: option (b) -- add a note that TASK-1 and TASK-2 form an atomic commit pair, and move `go build ./...` verification to TASK-2.

**MEDIUM -- Step 8 helper `resolveNarToStoreHash` is confusing**

The helper code starts with the wrong key lookup (`narHashReverseKey`), then self-corrects in comments mid-stream. An implementer could copy the wrong version. The task should simply state: "call `db.GetStorePathByNarHash(NarHash52(rawHash))` directly in the `strings.HasPrefix(indexPath, "indices/nar/")` branch." Remove the dead helper entirely.

**MEDIUM -- Step 8 nh/nhr cleanup for unreachable NAR indices is unaddressed**

When `deleteUnreachableIndices` determines a NAR index IS unreachable (correctly, after the fix), the existing cleanup code at gc.go:419 calls `narHashReverseKey(hash)` where `hash` is still the raw 52-char NarHash from `extractStorePathHash`. This constructs `nhr/<NarHash52>` but the actual key is `nhr/<StorePathHash32>`. The lookup silently fails, leaving stale nh/ and nhr/ entries. The fix should: for unreachable NAR indices, use the resolved StorePathHash32 (if available) to delete nhr/ and nh/ entries, or use the NarHash52 directly to delete the nh/ entry and then read its value to find the nhr/ key.

This is partially mitigated because narinfo deletion (which runs first in the same scan) already handles nh/nhr cleanup for the corresponding StorePathHash32. But if a NAR index exists without a narinfo (orphan NAR), the stale entries accumulate.

**LOW -- Realisation/log index `continue` is correct but should be documented**

Step 8 skips realisations and log indices with `continue`. This is correct because realisations use 64-char SHA256 hex hashes (neither StorePathHash32 nor NarHash52) and logs use `hash-name` format. Both are cleaned up indirectly when their narinfo is deleted. A brief comment explaining this in the implementation would prevent future confusion.

### Acceptance Criteria Assessment

Criteria 1-6 are well-defined and testable. Missing: a criterion (or explicit note) about the full build breaking until TASK-2 is done. Suggest adding: "TASK-1 and TASK-2 must be committed together; `go build ./...` verified in TASK-2."

### What is Correct
- Bug analysis and root cause are accurate
- Type design (named types, no validation constructors, mux handles validation) is sound
- Steps 1-7 signatures are all correct
- The core fix logic (resolve NarHash52 via GetStorePathByNarHash before reachability check) is correct
- Separation of concerns between TASK-1 (store package) and TASK-2 (callers) is reasonable if they are committed atomically
- Test coverage is thorough -- 13 GC tests including the bug reproducer

## Review Round 3

### Verdict: APPROVE with one advisory note

All five round 2 fixes have been correctly integrated. The task is implementable as written.

### Verification of Round 2 Fixes

**Fix 1 (realisations/logs fall-through): CORRECT**
The `else` branch leaves `storeHash` empty (zero-value `StorePathHash32("")`). The subsequent `if storeHash != ""` check fails, so these indices skip the reachability check and always reach the deletion logic. This preserves current behavior where realisations and logs are always deleted (their raw hashes -- 64-char SHA256 hex for realisations, `hash-name` for logs -- never matched the 32-char StorePathHash values in the reachable set anyway).

**Fix 2 (err != nil comment): CORRECT**
The comment now accurately says "DB error reading nh/ mapping." Verified against `store.go:125-138`: `GetStorePathByNarHash` returns `("", nil)` for `pebble.ErrNotFound`, so `err != nil` only fires on genuine DB errors.

**Fix 3 (nh/nhr cleanup code block): CORRECT**
Kind-specific cleanup uses properly typed keys:
- NAR indices: `narHashKey(NarHash52(hash))` for nh/ deletion, `narHashReverseKey(storeHash)` for nhr/ deletion (where `storeHash` is the resolved `StorePathHash32`). No `narRefKey` call (NAR indices don't have r/ entries).
- Narinfo indices: `narRefKey(storeHash)` for r/ deletion, then nhr/ reverse lookup to find and delete both nhr/ and nh/ entries.
- Realisations/logs: no cleanup needed (no r/, nh/, or nhr/ entries).

All key function parameter types match the signatures proposed in Step 2.

**Fix 4 (GetStorePathByNarHash return semantics): CORRECT**
Step 4 now states: returns empty `StorePathHash32` on not-found (`("", nil)`, NOT an error). Matches codebase exactly.

**Fix 5 (Step 5 title): CORRECT**
Now mentions both `addIndexEntries` and `StoreIndexWithRefs`.

### Bug Verification: CONFIRMED (re-verified)

Ran `TestGCDeletesReachableNARWhenHashesDiffer` and `TestGCNonNarinfoIndicesPreserved` against the current codebase -- both fail as expected, confirming the bug is real and reproducible.

### Acceptance Criteria Assessment: PASS

All 8 criteria are testable and sufficient:
1. Type distinctness: compiler-verifiable (named types, not aliases)
2. Swapped-arg prevention: compiler-verifiable (distinct parameter types)
3. Bug reproducer test: already exists and fails, will pass after fix
4. Existing tests: baseline established (all pass except the 2 bug-related tests)
5. go vet: straightforward
6. NarHash52-to-StorePathHash32 resolution: tested by #3 and `TestGCNonNarinfoIndicesPreserved`
7. nh/nhr cleanup: tested by `TestGCDeletesOrphanNARIndices` (verifies cleanup on deletion)
8. Atomic commit: process criterion, enforceable by committer

### No New Gaps Introduced

The round 2 edits are clean and self-consistent. The nh/nhr cleanup block correctly handles the asymmetry between NAR indices (keyed by NarHash52, no r/ entries) and narinfo indices (keyed by StorePathHash32, have r/ entries).

### Advisory Note (non-blocking)

**`sizeEvict` allPins map type**: The task mentions in Step 8 that `sizeEvict`'s internal `allPins` map needs typed keys (`StorePathHash32`). This is correct, but the task doesn't provide explicit code for this. The current code at `gc.go:118-128` iterates `cfg.PinnedHashes` (which becomes `map[StorePathHash32]struct{}`) and `ListPins()` (whose `PinEntry.Hash` becomes `StorePathHash32`), and then checks membership at line 149 against `hash` from `extractStorePathHash`. Since `sizeEvict` only scans narinfo indices (line 131: `i/indices/narinfo/`), the extracted hash IS a StorePathHash32, so the cast `StorePathHash32(hash)` at line 144 is semantically correct. The implementer should note that `allPins` becomes `map[StorePathHash32]struct{}` and the membership check at line 149 becomes `allPins[StorePathHash32(hash)]`. This is straightforward but worth calling out so the implementer doesn't overlook it.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Introduced `StorePathHash32` and `NarHash52` as distinct named string types in `pkg/store/hashes.go`. Updated all store package APIs to use typed parameters, making it impossible to swap hash kinds at compile time. Fixed the GC bug in `deleteUnreachableIndices`: NAR index paths now resolve their NarHash52 to StorePathHash32 via the `nh/` mapping before checking the reachable set. Added kind-specific nh/nhr cleanup logic. Renamed `extractStorePathHash` to `extractPathHash` (it extracts both hash kinds). Added `NHLookupErrors` counter to `GCStats` to surface DB errors during nh/ resolution. All store package tests pass including `TestGCDeletesReachableNARWhenHashesDiffer`. Committed atomically with TASK-2 as `1f1ec74`.
<!-- SECTION:FINAL_SUMMARY:END -->
