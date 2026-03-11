---
id: TASK-2
title: Propagate StorePathHash32 and NarHash52 to main package callers
status: Done
assignee:
  - sub-agent-1
created_date: '2026-03-10 13:13'
updated_date: '2026-03-10 15:17'
labels:
  - reviewed
dependencies:
  - TASK-1
references:
  - registry.go
  - router.go
  - main.go
  - registry_handlers.go
  - cmd/gc/main.go
  - router_refs_test.go
priority: high
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Context

TASK-1 introduced StorePathHash32 and NarHash52 types in the store package. This task updates the main package callers to use those types at the boundary where hashes are produced or consumed.

## Files

- **Modify**: `registry.go` — StorePathHash, ValidateStorePath
- **Modify**: `router.go` — parseNarinfoStorePathHash, parseNarinfoNarHash52, pinHash, unpinHash, insert, proxyNarinfo, proxyNar, proxyNarViaUpstreamNarinfo, largeHeadAndGet
- **Modify**: `main.go` — doCache, cacheNarFromUpstream
- **Modify**: `registry_handlers.go` — registryPut, registryDelete
- **Modify**: `registry_test.go` — update string hashes to typed
- **Modify**: `router_refs_test.go` — update parse function test expectations to use typed values (StorePathHash32, NarHash52)
- **Verify (no changes needed)**: `cmd/gc/main.go` — LoadPinnedHashes return type flows through without manual conversion

## Step 1: Update registry.go

```go
import "github.com/input-output-hk/spongix/pkg/store"

func StorePathHash(p string) store.StorePathHash32 {
    m := storePathRe.FindStringSubmatch(p)
    if m == nil {
        return ""
    }
    return store.StorePathHash32(m[1])
}

func ValidateStorePath(p string) (store.StorePathHash32, error) {
    m := storePathRe.FindStringSubmatch(p)
    if m == nil {
        return "", fmt.Errorf("invalid store path: %q", p)
    }
    return store.StorePathHash32(m[1]), nil
}
```

## Step 2: Update router.go parse functions

```go
func parseNarinfoStorePathHash(body []byte) store.StorePathHash32 {
    // ... same logic, but return store.StorePathHash32(storePath[:32])
}

func parseNarinfoNarHash52(body []byte) store.NarHash52 {
    // ... same logic, but return store.NarHash52(parts[1])
}

func parseNarinfoReferences(body []byte) []store.StorePathHash32 {
    // ... same logic, but refs are []store.StorePathHash32
}
```

## Step 3: Update router.go handlers

In `pinHash`:
```go
hash := store.StorePathHash32(mux.Vars(r)["hash"])
```

In `unpinHash`:
```go
hash := store.StorePathHash32(mux.Vars(r)["hash"])
```

## Step 4: Update router.go insert function

The `narRefs` parameter changes from `[]string` to `[]store.StorePathHash32`.
The `narHash52` variable changes from `string` to `store.NarHash52`.

## Step 5: Update router.go proxyNarinfo

```go
narHash52 := parseNarinfoNarHash52(body)    // now store.NarHash52
storePathHash := parseNarinfoStorePathHash(body) // now store.StorePathHash32
if narHash52 != "" && storePathHash != "" {
    if err := p.pebbleDB.SetNarHashMapping(narHash52, storePathHash); err != nil {
```

## Step 6: Update router.go proxyNar and proxyNarViaUpstreamNarinfo

In `proxyNar`:
```go
narHash := store.NarHash52(mux.Vars(r)["hash"])
storePathHash, err := p.pebbleDB.GetStorePathByNarHash(narHash)
```

`storePathHash` is now `store.StorePathHash32` and is passed to `proxyNarViaUpstreamNarinfo`. Update `proxyNarViaUpstreamNarinfo`'s parameter type:
```go
func (p *Proxy) proxyNarViaUpstreamNarinfo(namespace string, storePathHash store.StorePathHash32, ...) bool {
    narinfoPath := string(storePathHash) + ".narinfo"  // explicit conversion for URL construction
```

## Step 7: Update main.go doCache / cacheNarFromUpstream

In `doCache`, the `narHash52` variable is `store.NarHash52`:
```go
narHash52 := store.NarHash52(narHashParts[1])
```

In `cacheNarFromUpstream`, same treatment.

## Step 8: Update registry_handlers.go

In `registryPut` and `registryDelete`, `StorePathHash` now returns `store.StorePathHash32`:
```go
fromHash := StorePathHash(entry.FromPath) // returns store.StorePathHash32
if fromHash != "" {
    // PinHash now accepts StorePathHash32 — no conversion needed
```

## Step 9: Update registry_test.go

In `TestRegistryAutoPin`:
```go
pinned, err := proxy.pebbleDB.IsPinned(store.StorePathHash32("abc00000000000000000000000000001"))
```

In `TestRegistryPutOverwriteUnpinsOld`, update all three `IsPinned` calls (lines ~625, ~646, ~655):
```go
pinned, err := proxy.pebbleDB.IsPinned(store.StorePathHash32("abc00000000000000000000000000001"))
// ... and similarly for the other two hashes in that test
```

## Step 10: Update router_refs_test.go

Add `store` import and wrap test expectations with typed values. Five expectations need updating (lines ~21, ~60, ~69, ~86, ~108):
- `parseNarinfoStorePathHash` expectations → `store.StorePathHash32(...)`
- `parseNarinfoNarHash52` expectations → `store.NarHash52(...)`
- `parseNarinfoReferences` expectations → `[]store.StorePathHash32{...}`

## Step 11: Update pins_test.go sort comparison

`ListPins` now returns `PinEntry` with `Hash StorePathHash32`. The sort and comparison in tests needs:
```go
sort.Slice(pins, func(i, j int) bool { return pins[i].Hash < pins[j].Hash })
```
This still works because StorePathHash32 supports `<` comparison.

## Step 12: Note on largeHeadAndGet (no changes needed)

`largeHeadAndGet` calls `parseNarinfoNarHash52(body)` and uses the result for string operations (`!= ""`, `len()`, `[:4]`, `fmt.Sprintf`). All of these work on named string types without conversion. No code changes needed here.

## Step 13: Run full test suite

```bash
go vet ./...
go test ./... -count=1
```

ALL tests must pass.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 parseNarinfoStorePathHash returns StorePathHash32
- [x] #2 parseNarinfoNarHash52 returns NarHash52
- [x] #3 StorePathHash returns store.StorePathHash32
- [x] #4 All tests pass: go test ./... -count=1
- [x] #5 go vet ./... passes
- [x] #6 go build ./... passes (TASK-1 + TASK-2 atomic)
- [x] #7 proxyNarViaUpstreamNarinfo parameter updated from string to StorePathHash32
<!-- AC:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
## Review Round 1

### Verdict: REVISE -- one medium issue, two low issues

### File and Function Verification: PASS

All four source files exist and contain the functions/types listed:
- `registry.go`: `StorePathHash` (line 35), `ValidateStorePath` (line 26)
- `router.go`: `parseNarinfoStorePathHash` (line 876), `parseNarinfoNarHash52` (line 859), `parseNarinfoReferences` (line 895), `pinHash` (line 151), `unpinHash` (line 163), `insert` (line 743), `proxyNarinfo` (line 355), `proxyNar` (line 406), `largeHeadAndGet` (line 625)
- `main.go`: `doCache` (line 402), `cacheNarFromUpstream` (line 489)
- `registry_handlers.go`: `registryPut` (line 18), `registryDelete` (line 105)
- `registry_test.go`: `TestRegistryAutoPin` (line 572), `TestRegistryPutOverwriteUnpinsOld` (line 609)

`pins_test.go` is not listed in Files but referenced in Step 10 -- acceptable since only a cosmetic note.

### `cmd/gc/main.go` caller verification (LOW)

TASK-1 lists `cmd/gc/main.go` in its References. After TASK-1 changes `LoadPinnedHashes` to return `map[StorePathHash32]struct{}` and `GCConfig.PinnedHashes` to the same type, `cmd/gc/main.go` lines 132-142 still compile because it just passes `filePins` through to `GCConfig` -- the types match. Same for `main.go` lines 78-86. No code changes needed in either caller, but `cmd/gc/main.go` should be listed in Files for completeness as a verify-only entry.

### MEDIUM -- Missing `proxyNarViaUpstreamNarinfo` parameter type update

Step 6 correctly updates `proxyNar` to use typed values:
```go
narHash := store.NarHash52(mux.Vars(r)["hash"])
storePathHash, err := p.pebbleDB.GetStorePathByNarHash(narHash)
```

But `storePathHash` (now `store.StorePathHash32`) is then passed to `proxyNarViaUpstreamNarinfo` at line 428:
```go
if p.proxyNarViaUpstreamNarinfo(namespace, storePathHash, location, w, r) {
```

`proxyNarViaUpstreamNarinfo` (line 519) currently takes `storePathHash string`. After the change, `GetStorePathByNarHash` returns `store.StorePathHash32`, so either:
- Change the parameter type to `store.StorePathHash32` and convert to string at line 525: `narinfoPath := string(storePathHash) + ".narinfo"`
- Or convert at the call site: `string(storePathHash)`

Recommendation: Change the parameter type for consistency. Add this to Step 6.

### LOW -- `largeHeadAndGet` call site not mentioned

In `largeHeadAndGet` (router.go lines 686-695), `parseNarinfoNarHash52(body)` is called and the result is used for string slicing and comparison. After TASK-2, `parseNarinfoNarHash52` returns `store.NarHash52`. All operations (`!= ""`, `len()`, `[:4]`, `fmt.Sprintf`) work on named string types, so this compiles without changes. The task should mention this call site with a "no changes needed" note to prevent implementer confusion.

### LOW -- Step 9 test update is incomplete

Step 9 shows `IsPinned(store.StorePathHash32(...))` for `TestRegistryAutoPin` (line 587). However, `TestRegistryPutOverwriteUnpinsOld` has three `IsPinned` calls (lines 625, 646, 655) with different hashes that all need wrapping. The implementer will find them via compiler errors, but listing them improves clarity.

### Acceptance Criteria Assessment

Criteria 1-4 are testable. Missing: `go vet ./...` and explicit `go build ./...` checks. Added via this review.

### What is Correct
- All code examples match real function signatures in the codebase
- The propagation order (registry.go -> router.go parse fns -> router.go handlers -> main.go -> registry_handlers.go -> tests) is logical
- The task correctly identifies that `insert`'s `narRefs []string` and `narHash52 string` parameters must change
- Steps 1-9 cover all call sites where TASK-1's API changes would cause compiler errors
- The atomic commit requirement with TASK-1 (from TASK-1's implementation notes) is sound

## Review Round 2

### Verdict: APPROVE with one low-priority advisory

All three round-1 issues have been correctly addressed. No correctness problems found. The task is implementable as written.

### Verification of Round 1 Fixes

**Fix 1 (MEDIUM -- proxyNarViaUpstreamNarinfo parameter update): CORRECT**

Step 6 now explicitly includes the parameter type change with `string(storePathHash)` conversion for URL construction. Verified against `router.go:519` where `proxyNarViaUpstreamNarinfo` currently takes `storePathHash string` and uses it at line 525 for concatenation with `.narinfo`. The proposed change is correct.

**Fix 2 (LOW -- largeHeadAndGet not mentioned): CORRECT**

Step 11 was added with a clear "no changes needed" note. Verified against `router.go:686-695` -- `parseNarinfoNarHash52(body)` result is used for `!= ""`, `len()`, `[:4]`, and `fmt.Sprintf`, all of which work on named string types without explicit conversion.

**Fix 3 (LOW -- Step 9 incomplete test listing): CORRECT**

Step 9 now lists all three `IsPinned` calls in `TestRegistryPutOverwriteUnpinsOld` at lines ~625, ~646, ~655. Verified against `registry_test.go:625,646,655` -- all three use bare string hashes that need `store.StorePathHash32(...)` wrapping.

### Full Step-by-Step Verification Against Codebase

Every step was verified against the actual source code:

- **Step 1** (registry.go): `StorePathHash` at line 35 and `ValidateStorePath` at line 26 return `string`. Changes to `store.StorePathHash32` are correct.
- **Step 2** (router.go parse functions): All three parse functions at lines 876, 859, 895 return `string`/`[]string`. Return type changes are correct.
- **Step 3** (pinHash/unpinHash): Lines 152, 164 use `mux.Vars(r)["hash"]`. Wrapping with `store.StorePathHash32(...)` matches TASK-1's `PinHash`/`UnpinHash` signatures.
- **Step 4** (insert): Lines 750-751 declare `narRefs []string` and `narHash52 string`. These flow into `CommitWithIndex` (line 778) and `StoreIndexWithRefs` (line 793). Type change is correct.
- **Step 5** (proxyNarinfo): Lines 387-390 flow into `SetNarHashMapping`. Parameter order `(NarHash52, StorePathHash32)` matches TASK-1's API.
- **Step 6** (proxyNar + proxyNarViaUpstreamNarinfo): Line 423 wraps with `NarHash52`, line 424 receives `StorePathHash32` from `GetStorePathByNarHash`. Parameter type change at line 519 is correct.
- **Step 7** (doCache/cacheNarFromUpstream): Lines 456 and 507 wrap with `NarHash52`. Subsequent `[:4]` and `fmt.Sprintf` work on named string types.
- **Step 8** (registry_handlers.go): Lines 55, 65, 122 call `StorePathHash` which returns typed value. Flows into typed `PinHash`/`UnpinHash`.
- **Step 9** (registry_test.go): All identified `IsPinned` calls are correct.
- **Step 10** (pins_test.go): Sort comparison with `<` works on `StorePathHash32`.
- **Step 11** (largeHeadAndGet): No changes needed -- verified.
- **Step 12** (test suite): `go vet ./...` and `go test ./... -count=1` is comprehensive.

### New Issues Found

**LOW -- `router_refs_test.go` not listed in Files or Steps**

`router_refs_test.go` contains tests for all three parse functions (`parseNarinfoReferences`, `parseNarinfoNarHash52`, `parseNarinfoStorePathHash`). After Step 2 changes their return types, the test expectations must be updated:

- Line 21: `want := []string{...}` becomes `want := []store.StorePathHash32{...}`
- Line 60: same
- Line 69: `want := "1h6m2q7..."` becomes `want := store.NarHash52("1h6m2q7...")`
- Line 86: `want := "hyhrnrn..."` becomes `want := store.StorePathHash32("hyhrnrn...")`
- Line 108: same as line 21
- Import `"github.com/input-output-hk/spongix/pkg/store"` must be added.

The implementer will discover these via compiler errors, but the file should be listed for completeness. Does not block approval.

### Acceptance Criteria Assessment: PASS

All 7 criteria are testable, well-defined, and sufficient. The `go build ./...` criterion (#6) ensures the atomic commit with TASK-1 works. The `cmd/gc/main.go` verify-only entry is already in the Files section.

### What is Correct

- All code examples match real function signatures in the codebase
- The propagation order is logical and complete
- Every call site where TASK-1's API changes cause compiler errors is covered
- The atomic commit requirement with TASK-1 is sound
- The "no changes needed" notes for `largeHeadAndGet`, `listPins`, and `cmd/gc/main.go` are all accurate
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Propagated `StorePathHash32` and `NarHash52` types to all main package callers: `registry.go`, `router.go`, `main.go`, `registry_handlers.go`, and all test files. Parse functions (`parseNarinfoStorePathHash`, `parseNarinfoNarHash52`, `parseNarinfoReferences`) now return typed values. Handler functions cast URL parameters at the boundary. `proxyNarViaUpstreamNarinfo` parameter updated from `string` to `store.StorePathHash32`. `cmd/gc/main.go` needed no changes (types flow through). `go vet ./...`, `go test ./... -count=1`, and `go build ./...` all pass. Committed atomically with TASK-1 as `1f1ec74`.
<!-- SECTION:FINAL_SUMMARY:END -->
