# Test Analysis: perf/week1-week2 Performance Changes

## Source files read

- `internal/store/bulk.go` — partial index DDL, `DropUserIndexes`/`CreateUserIndexes`
- `internal/store/store.go` — `OpenReadOnly`, schema init, `OpenMemory`, `OpenPath`
- `internal/store/router.go` — `ForProjectReadOnly`, `StoreRouter`
- `internal/store/architecture.go` — `archLayers(project, cached []CrossPkgBoundary)`; `fetchAspects` wiring
- `internal/tools/tools.go` — `Server` struct with `archCache`/`archCacheVer`/`graphWriteVer`; `bumpGraphVersion`, `syncProject`, `startAutoIndex`
- `internal/tools/architecture.go` — `handleGetArchitecture` cache read/write path
- `internal/store/louvain.go` — `louvain` main loop, `louvainLocalMoving` returning `(int, bool)`
- `internal/pipeline/pipeline.go` — `runFullPasses` concurrent `passTests`/`passHTTPLinks`; `changedFilesAffectCommunities`; `runIncrementalPasses`
- `internal/store/search.go` — `extractLikeHints`, `computeSQLLimit`, `buildSearchConditions`
- `internal/pipeline/resolver.go` — `FunctionRegistry.failedLookups` with `sync.RWMutex`
- `internal/store/store_test.go` — test patterns, `OpenMemory` fixture style
- `internal/store/architecture_test.go` — `setupArchTestStore`, existing arch + Louvain tests
- `internal/pipeline/pipeline_test.go` — integration test shape, `setupTestRepo`

---

## Change 1: Store indexes + OpenReadOnly

### What the code actually does

**Three partial indexes** added in both `initSchema` (store.go:247–257) and `CreateUserIndexes` (bulk.go:41–49):

```sql
CREATE INDEX IF NOT EXISTS idx_nodes_file_line
    ON nodes(project, file_path, start_line, end_line)
    WHERE label IN ('Function','Method','Class','Type','Interface','Enum');

CREATE INDEX IF NOT EXISTS idx_nodes_entry_point
    ON nodes(project)
    WHERE json_extract(properties, '$.is_entry_point') = 1;

CREATE INDEX IF NOT EXISTS idx_nodes_is_test
    ON nodes(project)
    WHERE json_extract(properties, '$.is_test') = 1;
```

**`OpenReadOnly(dbPath string)`** (store.go:116–139):
- Returns error immediately if the file does not exist (`os.Stat` check).
- Opens with DSN flags: `mode=ro`, `_cache_size=-65536`, `_synchronous=OFF`, `_mmap_size=1073741824`.
- Sets `PRAGMA query_only = ON` (non-fatal on older SQLite).
- Sets `PRAGMA temp_store = MEMORY` (fatal on failure).
- Skips `initSchema` entirely.
- Returns `&Store{db, q: db, dbPath}` — identical shape to `OpenPath` stores.

**`ForProjectReadOnly(name string)`** (router.go:82–85):
- Constructs `filepath.Join(r.dir, name+".db")` and delegates to `OpenReadOnly`.
- Does NOT cache the returned store in `r.stores`. Each call opens a fresh connection.
- Returns error if the `.db` file does not exist.

### Acceptance criteria

| # | Criterion | Pass condition | Fail condition |
|---|-----------|----------------|----------------|
| 1.1 | Partial indexes exist after schema init | `pragma_index_list('nodes')` contains all three index names | Any index absent |
| 1.2 | Partial indexes are in `userIndexes` list | `DropUserIndexes` + `CreateUserIndexes` round-trip leaves DB queryable | Index missing after recreate |
| 1.3 | `idx_nodes_file_line` covers correct labels | A node with `label='Function'` at a given file+line is found via the index; a `label='File'` node at same coordinates is not indexed | Wrong nodes indexed |
| 1.4 | `idx_nodes_entry_point` covers only `is_entry_point=1` | Only nodes with that JSON property appear in index scan; nodes with `is_entry_point=false` or absent property are not index-eligible | Filter incorrect |
| 1.5 | `idx_nodes_is_test` mirrors same logic for `is_test` | Analogous to 1.4 | Filter incorrect |
| 1.6 | `OpenReadOnly` errors on missing file | Returns non-nil error with path context when `.db` absent | Returns nil error or wrong error |
| 1.7 | `OpenReadOnly` opens existing DB without schema writes | Can `SELECT` nodes; any `INSERT`/`UPDATE` must fail | Write succeeds on RO store |
| 1.8 | `OpenReadOnly` sets `PRAGMA temp_store = MEMORY` | Query succeeds; PRAGMA value is `2` | Pragma not set |
| 1.9 | `ForProjectReadOnly` returns error for unindexed project | Error returned when no `.db` file | Returns nil error |
| 1.10 | `ForProjectReadOnly` does not cache the store | Two consecutive calls return distinct `*Store` pointers | Returns same pointer (would be a connection-leak risk) |
| 1.11 | `DropUserIndexes` drops all three new indexes | After drop, none of the three appear in `pragma_index_list` | Any survives |
| 1.12 | `CreateUserIndexes` is idempotent | Calling twice does not error | Second call errors |

### Edge cases

- `OpenReadOnly` on a directory path (not a file): `os.Stat` succeeds but `sql.Open` should fail.
- `OpenReadOnly` on a zero-byte file: `os.Stat` passes; `sql.Open` or first query should fail cleanly.
- `OpenReadOnly` on a WAL-mode DB with an open WAL file: should read committed data, WAL pages accessible in RO mode.
- `ForProjectReadOnly` with `name=""`: constructs `".db"` — should either error or open an unrelated file; verify behavior is defined.
- Node with `is_entry_point=0` (integer zero, not absent): SQLite `json_extract` returns `0`, not `1`; must not be indexed.
- Node with `properties='{}'` (no key): `json_extract` returns NULL; must not be indexed.
- `DropUserIndexes` called when indexes don't exist: `DROP INDEX IF EXISTS` should be a no-op, not an error.
- Concurrent `ForProjectReadOnly` calls for the same project name: each opens independent connections; no shared mutex — verify no data race.

### Test categories

**Unit (in-process, `OpenMemory` not usable for RO tests — need a real file):**
- `TestOpenReadOnlyMissingFile` — create temp dir, call `OpenReadOnly` on nonexistent path, assert error contains path.
- `TestOpenReadOnlySuccess` — `OpenPath` a temp file, write a project row, close, reopen via `OpenReadOnly`, `SELECT` succeeds.
- `TestOpenReadOnlyRejectsWrites` — after `OpenReadOnly`, attempt `INSERT INTO nodes ...`, assert error.
- `TestOpenReadOnlyPragmas` — after `OpenReadOnly`, `PRAGMA temp_store` → `"2"`.
- `TestPartialIndexesExist` — `OpenMemory`, check `pragma_index_list` for all three new names.
- `TestPartialIndexDropAndRecreate` — `OpenMemory`, call `DropUserIndexes`, verify absent, call `CreateUserIndexes`, verify present.
- `TestForProjectReadOnlyMissing` — `NewRouterWithDir(tmpDir)`, call `ForProjectReadOnly("nonexistent")`, assert error.
- `TestForProjectReadOnlyExists` — `OpenInDir` to seed a project, close, call `ForProjectReadOnly`, verify queryable.
- `TestForProjectReadOnlyNoCache` — two calls return `!=` pointer values.

**Integration:** Not required; behavior is fully covered by unit tests with real temp files.

### Mocks / dependencies

- **Filesystem**: tests use `os.MkdirTemp` + real files. `OpenReadOnly` cannot be tested with `:memory:` because it requires a real file path.
- **SQLite**: no mock; use the real driver. The `mode=ro` URI parameter is driver-specific behavior; only a real driver call proves it works.
- No network dependencies.

---

## Change 2: Architecture cache + double-call fix

### What the code actually does

**`archLayers` signature change** (architecture.go:476–488):
```go
func (s *Store) archLayers(project string, cached []CrossPkgBoundary) ([]PackageLayer, error)
```
- If `cached != nil`, uses it directly; skips the `archBoundaries` SQL scan.
- If `cached == nil`, calls `archBoundaries` itself.

**`fetchAspects` wiring** (architecture.go:131):
```go
{"layers", func() error { var e error; info.Layers, e = s.archLayers(project, info.Boundaries); return e }},
```
Since `fetchAspects` runs `boundaries` before `layers` (list order), `info.Boundaries` is already populated when `layers` runs — **only when both are in the `want` set**. When `layers` is requested without `boundaries`, `info.Boundaries` is `nil` and `archLayers` falls back to its own scan.

**Server-level cache** (tools.go:53–57, architecture.go:44–75):
- `Server.archCache map[string][]byte` keyed by `"projectName:aspect1,aspect2,..."`
- `Server.archCacheMu sync.RWMutex` protects reads and writes.
- Cache read: `RLock`, check, `RUnlock` — return serialised JSON bytes on hit.
- Cache write: after successful compute, `Lock`, store `[]byte(tc.Text)`, `Unlock`.
- Invalidation: `syncProject` and `startAutoIndex` both call `bumpGraphVersion(project)` then `archCacheMu.Lock(); delete(archCache, project); archCacheMu.Unlock()` — but the key format is `project:aspects`, not `project`, so **`delete(s.archCache, projectName)` will not match any key** because keys contain `:aspects` suffix.

> **Bug note**: The invalidation deletes `archCache[projectName]` but keys are `"project:aspect1,aspect2"`. No key will ever equal just `projectName`. The cache is never actually invalidated. This should be caught by a test.

**`bumpGraphVersion`** (tools.go:116–123): CAS loop on `sync.Map` to atomically increment a `uint64` per project.

### Acceptance criteria

| # | Criterion | Pass condition | Fail condition |
|---|-----------|----------------|----------------|
| 2.1 | `archLayers` with `cached != nil` skips second SQL scan | Spy/count queries; with non-nil cached boundaries, node+edge queries for boundaries are not re-issued | Extra query observed |
| 2.2 | `archLayers` with `cached == nil` computes its own boundaries | Returns correct `PackageLayer` results without caller providing boundaries | Returns empty or errors |
| 2.3 | `fetchAspects` passes `info.Boundaries` to `archLayers` when both requested | Requesting `["boundaries","layers"]` produces identical layers to requesting `["layers"]` alone (boundary data same) | Layers differ due to double-scan ordering |
| 2.4 | Cache hit returns same JSON bytes | Two consecutive `handleGetArchitecture` calls for same project+aspects; second call does not touch SQLite | DB queried on second call |
| 2.5 | Cache is keyed by project+aspects | `["languages"]` and `["packages"]` are cached independently | Cross-contamination |
| 2.6 | Cache invalidated after index sync | After `bumpGraphVersion`+invalidation, next call recomputes | Stale cached result returned after reindex |
| 2.7 | **Invalidation key bug**: `delete(archCache, projectName)` misses `"project:aspects"` keys | Test demonstrates stale cache survives `syncProject` call | (This is the bug to catch) |
| 2.8 | `bumpGraphVersion` increments atomically | Concurrent calls produce monotonically distinct values | Value stays at 0 or races |
| 2.9 | Cache read under concurrent writers is safe | Parallel goroutines calling `handleGetArchitecture` produce no data race | Race detector fires |

### Edge cases

- `aspects=["all"]` vs `aspects=["languages","packages","entry_points","routes","hotspots","boundaries","services","layers","clusters","file_tree"]` — these expand to the same set but different cache keys; verify no cross-contamination.
- `aspects=["layers"]` alone (no `"boundaries"`) — `info.Boundaries` is `nil`; `archLayers` must call `archBoundaries` internally; result must be correct.
- `aspects=["boundaries","layers"]` — `info.Boundaries` is populated; `archLayers` must receive it and skip internal call.
- Empty project (no nodes): `archLayers` with `cached=[]` (non-nil empty slice) must not call `archBoundaries`; returns empty layers, no error.
- Cache populated then project deleted: `delete(archCache, project)` only removes keys that match exactly; key is `"project:aspects"` — demonstrates the invalidation miss.
- `bumpGraphVersion` called for project never seen before: `LoadOrStore` initializes to `uint64(0)`, CAS increments to `1`.

### Test categories

**Unit (store package — no Server required):**
- `TestArchLayersWithCached` — call `archLayers("test", boundaries)` where `boundaries` is non-nil; assert result is equivalent to result of `archLayers("test", nil)` with same underlying data.
- `TestArchLayersWithNilCached` — call with `nil`; verify it computes correctly.
- `TestFetchAspectsPassesBoundariesToLayers` — call `GetArchitecture("test", []string{"boundaries","layers"})`; verify layers match those computed with the boundaries.

**Unit (tools package):**
- `TestArchCacheHit` — call `handleGetArchitecture` twice; instrument store to count queries; second call must not query.
- `TestArchCacheKeyIsolation` — cache `["languages"]`, then request `["packages"]`; second request must compute fresh.
- `TestArchCacheInvalidationBug` — call `handleGetArchitecture`, then call `syncProject` (or directly invoke the cache delete), then call `handleGetArchitecture` again; assert second result reflects changed data. Currently this test **will fail**, demonstrating the bug.
- `TestBumpGraphVersionConcurrent` — 100 goroutines call `bumpGraphVersion`; assert final value equals 100.
- `TestArchCacheConcurrent` — `go test -race`; parallel `handleGetArchitecture` calls must not race.

**Integration:** Not required beyond above.

### Mocks / dependencies

- **Store**: use `OpenMemory()` for `archLayers` and `GetArchitecture` tests. The `handleGetArchitecture` tests require a real `Server` with a `StoreRouter` backed by a temp dir.
- **No network** needed.
- Query-count instrumentation: can be done with a wrapping `Querier` implementation that counts `Query` calls.

---

## Change 3: Pipeline — Louvain early-exit, leaf-community skip, parallel post-flush

### What the code actually does

**`louvainLocalMoving` return signature** (louvain.go:100–157):
```go
func louvainLocalMoving(...) (changedCount int, improved bool)
```
Returns `(changed, changed > 0)`.

**Main loop early-exit** (louvain.go:81–88):
```go
maxIter := 15
for iter := 0; iter < maxIter; iter++ {
    changed, improved := louvainLocalMoving(...)
    louvainRefine(...)
    if !improved || float64(changed)/float64(n) < 0.001 {
        break
    }
}
```
Exits when no node moved (`!improved`) OR the fraction of moved nodes is below 0.1% of total nodes.

**`changedFilesAffectCommunities`** (pipeline.go:535–557):
- Returns `false` (skip community pass) when `len(changedFiles) == 0`.
- Returns `true` conservatively on any error, or when no nodes are found in changed files (new file).
- Returns `false` only when all nodes in changed files have zero outbound `CALLS` edges (`len(edgeMap) == 0`).

**Parallel post-flush** (pipeline.go:330–343):
```go
g := new(errgroup.Group)
g.Go(func() error { p.passTests(); return nil })
g.Go(func() error {
    if err := p.passHTTPLinks(); err != nil { slog.Warn(...) }
    return nil
})
_ = g.Wait()
```
Both goroutines always return `nil` — errors from `passHTTPLinks` are demoted to warnings.

### Acceptance criteria

| # | Criterion | Pass condition | Fail condition |
|---|-----------|----------------|----------------|
| 3.1 | `louvainLocalMoving` returns `(0, false)` on no moves | Graph already converged (all nodes optimal); returns `changed=0, improved=false` | Returns `changed=0, improved=true` |
| 3.2 | `louvainLocalMoving` returns `(k, true)` when k nodes moved | k > 0 moves made; `improved=true` | `improved=false` despite moves |
| 3.3 | Main loop exits early when `improved=false` | Fully converged graph exits in fewer than `maxIter=15` iterations | Loop always runs 15 iterations |
| 3.4 | Main loop exits early when `changedFraction < 0.001` | Graph with 1001 nodes where only 1 moves (0.1%) exits after that iteration | Loop continues despite tiny change |
| 3.5 | `changedFilesAffectCommunities` returns `false` for empty list | `changedFilesAffectCommunities([]discover.FileInfo{})` → `false` | Returns `true` |
| 3.6 | `changedFilesAffectCommunities` returns `false` for leaf-only files | Changed file contains only nodes with no outbound CALLS edges → `false` | Returns `true`; community pass runs unnecessarily |
| 3.7 | `changedFilesAffectCommunities` returns `true` when files have CALLS edges | Changed file contains a node with at least one CALLS edge → `true` | Returns `false`; community pass skipped incorrectly |
| 3.8 | `changedFilesAffectCommunities` returns `true` on store error | `FindNodesByFile` or `FindEdgesBySourceIDs` errors → `true` (conservative) | Returns `false`; community pass silently skipped |
| 3.9 | `changedFilesAffectCommunities` returns `true` for new (empty) files | No nodes found for the file → `true` (conservative) | Returns `false` |
| 3.10 | `passTests` and `passHTTPLinks` run concurrently in full index | Both complete; no data corruption; race detector clean | Sequential execution or data race |
| 3.11 | `passHTTPLinks` error does not abort `passTests` | `passHTTPLinks` failure logged as warning; `passTests` result unaffected | `passTests` aborted or error propagated |

### Edge cases

- `louvainLocalMoving` with `n=1`: single node, no neighbors, no moves; must return `(0, false)`.
- `louvainLocalMoving` with `n=0`: cannot be called (`louvain` guards `len(nodes)==0`); test `louvain(nil, nil)` hits the guard.
- Early-exit threshold: exactly `n*0.001` moves (e.g., 1 move with `n=999`) — `float64(1)/float64(999) = 0.001001 > 0.001`, should NOT exit. With `n=1001`, `1/1001 < 0.001`, should exit.
- `changedFilesAffectCommunities`: file with nodes but all edges are `USAGE` type (not `CALLS`) → `edgeMap` is empty → returns `false`. This is correct behavior: only CALLS affect Louvain communities.
- `changedFilesAffectCommunities` with a file that was deleted (nodes removed before this call): `FindNodesByFile` returns empty slice → `nodeIDs` empty → returns `true` (conservative). This is correct.
- Concurrent `passTests` + `passHTTPLinks`: both write to the `edges` table. Since SQLite in WAL mode serialises writes, they contend on the write lock. The `errgroup` does not parallelise the writes themselves, only the Go-side work. Verify no `SQLITE_BUSY` or deadlock.
- `maxIter=15` boundary: a graph that needs exactly 15 iterations must converge within the cap.

### Test categories

**Unit (store package):**
- `TestLouvainLocalMovingNoMoves` — build a graph already at a local optimum (each node in its community maximises modularity); call `louvainLocalMoving`; assert `changed=0, improved=false`.
- `TestLouvainLocalMovingWithMoves` — build a two-cluster graph, initialize all nodes to community 0; call once; assert `changed > 0, improved=true`.
- `TestLouvainEarlyExitOnConvergence` — instrument iteration count (or use a graph that converges fast); verify loop runs fewer than `maxIter` iterations.
- `TestLouvainFractionThreshold` — synthesise a graph where exactly 1 of 1001 nodes moves; verify loop exits after that iteration (fraction < 0.001).
- Existing tests `TestLouvainBasic`, `TestLouvainConverges`, `TestLouvainEmpty`, `TestLouvainSingleNode` all remain valid and cover the new signature transparently.

**Unit (pipeline package):**
- `TestChangedFilesAffectCommunities_Empty` — `changedFilesAffectCommunities([]discover.FileInfo{})` → `false`.
- `TestChangedFilesAffectCommunities_LeafOnly` — seed store with a node in a file, no CALLS edges; assert `false`.
- `TestChangedFilesAffectCommunities_WithCalls` — seed node with one CALLS edge; assert `true`.
- `TestChangedFilesAffectCommunities_Conservative_NoNodes` — file in `changedFiles` but no nodes in DB for it; assert `true`.
- `TestChangedFilesAffectCommunities_Conservative_Error` — mock `FindEdgesBySourceIDs` to return error; assert `true`.

**Integration (pipeline package):**
- `TestParallelPostFlushPasses` — run `Pipeline.Run()` on a test repo; use the race detector (`go test -race`); verify both TESTS edges and Route nodes are written correctly. This is the primary test for concurrency correctness.
- `TestPassHTTPLinksErrorDemoted` — no standalone test needed; the error is swallowed to `slog.Warn`. Verify via integration that a repo with no HTTP patterns still completes without error.

### Mocks / dependencies

- **`changedFilesAffectCommunities`** tests: use `OpenMemory()` store + real `Pipeline` struct; call the method directly (it's unexported but tests in `package pipeline` can access it).
- **Concurrent passes**: race detector via `go test -race ./internal/pipeline/...`. No mock needed; `OpenMemory` is thread-safe for WAL-mode semantics.
- **`louvainLocalMoving`**: pure function, no I/O — no mocks needed, test in-process.

---

## Change 4: Search LIKE hints + resolver memoization

### What the code actually does

**`extractLikeHints`** (search.go:448–497):

Two fast paths prepended before the general character walk:
1. `.*X.*` pattern: strips leading/trailing `.*`, checks remainder has no regex metacharacters and `len >= 3`; returns `[]string{stripped}`.
2. Plain literal: `len(pattern) >= 3 && !hasRegexMetachar(pattern)`; returns `[]string{pattern}`.

Falls through to the existing character-walk for other patterns. Returns `nil` for patterns containing `|` (alternation).

**`computeSQLLimit`** (search.go:172–199):
```go
if nameHasLikeHints {
    limit := params.Offset + params.Limit + 5000
    if limit > 50000 {
        limit = 50000
    }
    return limit
}
```
When LIKE hints are present, caps at 50K (instead of 200K). The `qnHasLikeHints` flag is computed and returned but **not used in `computeSQLLimit`** — only `nameHasLikeHints` triggers the cap. This means a search with only a QN pattern hint still uses the 200K path.

**`FunctionRegistry.failedLookups`** (resolver.go:27–29):
```go
failedMu     sync.RWMutex
failedLookups map[string]bool
```
In `resolveViaNameLookup` (resolver.go:139–145):
```go
r.failedMu.RLock()
_, known := r.failedLookups[simple]
r.failedMu.RUnlock()
if known {
    return ResolutionResult{}
}
```
And after a failed resolution (resolver.go:168–172):
```go
if result.QualifiedName == "" {
    r.failedMu.Lock()
    r.failedLookups[simple] = true
    r.failedMu.Unlock()
}
```
Only caches "unresolvable" for simple names that reach `resolveViaNameLookup`. Names resolved earlier (import map, same-module, or strategy 3/4 in `byName`) are never written to `failedLookups`.

### Acceptance criteria

| # | Criterion | Pass condition | Fail condition |
|---|-----------|----------------|----------------|
| 4.1 | `extractLikeHints(".*X.*")` returns `["X"]` for literal X | Single-element slice with inner literal | Returns nil or wrong value |
| 4.2 | Fast path requires `len(stripped) >= 3` | `".*ab.*"` → `nil`; `".*abc.*"` → `["abc"]` | Short literals returned |
| 4.3 | Fast path requires no metacharacters in stripped | `".*foo.*bar.*"` → falls through to walk (has `.*` inside) | Fast path incorrectly fires |
| 4.4 | Plain literal fast path works | `"handler"` → `["handler"]`; `"ab"` → `nil` | Wrong result |
| 4.5 | Alternation always returns nil | `"foo\|bar"` → `nil` | Hint returned for OR pattern |
| 4.6 | `computeSQLLimit` caps at 50K when `nameHasLikeHints=true` | `offset=0, limit=10` → SQL limit = 5010 (capped); `offset=45000, limit=10` → 50000 | Returns 200K |
| 4.7 | `computeSQLLimit` uses 200K when only `qnHasLikeHints=true` | `nameHasLikeHints=false, qnHasLikeHints=true` → 200K | Returns 50K incorrectly |
| 4.8 | `computeSQLLimit` cap formula: `min(offset+limit+5000, 50000)` | `offset=44999, limit=10` → 50009 → capped to 50000 | Arithmetic wrong |
| 4.9 | Search with `.*Submit.*` pattern uses LIKE pre-filter | `buildSearchConditions` returns `nameHasLikeHints=true`; SQL includes `n.name LIKE '%Submit%'` | Condition absent |
| 4.10 | LIKE pre-filter + Go regex produce correct results | Search for `".*Submit.*"` returns only nodes containing "Submit" | False positives from LIKE, false negatives from regex |
| 4.11 | `failedLookups` skips re-resolution on second call | First call for unknown name resolves and caches; second call reads cache without scanning `byName` | Cache miss on second call |
| 4.12 | `failedLookups` only caches truly unresolvable names | A name that resolves via import map is NOT added to `failedLookups` | Resolvable name incorrectly blacklisted |
| 4.13 | `failedLookups` is goroutine-safe | Concurrent `Resolve` calls for same unknown name produce no data race | Race detector fires |
| 4.14 | `failedLookups` not populated for strategy 1/2 resolutions | Import-map and same-module resolutions never touch `failedMu` | Lock contention on fast path |

### Edge cases

**`extractLikeHints`:**
- `".*.*"` (double wildcard, no literal) → stripped is `".*"` → `hasRegexMetachar(".*") = true` → fast path misses; general walk produces no hints ≥ 3 chars → `nil`.
- `".*ab.*"` → stripped `"ab"`, `len=2 < 3` → fast path does not fire → walk produces `"ab"` which is also `< 3` → `nil`. Verified by existing test.
- `"(?i).*Submit.*"` → `buildLikeHintCondition` calls `stripCaseFlag` first, yielding `".*Submit.*"` before `extractLikeHints` — fast path fires correctly.
- Pattern with escaped dot `"\\.Submit"` → general walk: `'\'` triggers escape branch (emits `.`), then `S`... → literal segment `".Submit"` but `'.'` is a metachar — actually `\\.` in the pattern string means the two bytes `\` `.`; the walk emits `.` as a literal, then continues with `S`, `u`, `b`... → final segment `".Submit"` of length 7, which would be a hint. Test this case explicitly.
- `"Submit"` (plain literal, no dots) → fast path 2: `len=6 >= 3`, no metachar → `["Submit"]`.

**`computeSQLLimit`:**
- Both `nameHasLikeHints=true` and `qnHasLikeHints=true`: only name branch is checked; 50K cap applies.
- `params.Limit=0` (default to 100000 in `Search`): `computeSQLLimit` is called after the default is applied; `Limit=100000` → `offset+limit+5000 = 105000 > 50000` → cap at 50000.
- `params.Offset=49000, params.Limit=10, nameHasLikeHints=true`: `49000+10+5000=54010 > 50000` → 50000.

**`failedLookups`:**
- Same `simple` name registered then unregistered: `byName[simple]` becomes empty; `Resolve` returns empty; `failedLookups[simple]=true`. Then if `Register` is called again with same simple name, `byName` grows; but `failedLookups` still marks it as known-fail. This stale entry would cause the re-registered function to be unresolvable. Test for this regression if `FunctionRegistry` is ever reused across index runs (it is not — `New()` creates a fresh registry each run, so this is not a live bug, but worth documenting).
- `FuzzyResolve` does NOT check or populate `failedLookups` — verify this is intentional (fuzzy is a separate code path).
- Concurrent `Register` + `Resolve`: `Register` holds `r.mu.Lock()`; `resolveViaNameLookup` holds `r.mu.RLock()` then drops it before acquiring `failedMu.RLock()`. Verify the RLock release order is correct and no deadlock is possible when `Register` is called concurrently with `Resolve`.

### Test categories

**Unit (store package — `extractLikeHints`, `computeSQLLimit`):**
- Extend the existing `TestExtractLikeHints` table with fast-path cases:
  - `{".*Submit.*", []string{"Submit"}}` — fast path 1.
  - `{".*ab.*", nil}` — fast path 1, too short.
  - `{".*foo.*bar.*", nil}` — no single stripped literal (has `.*` in middle).
  - `{"Submit", []string{"Submit"}}` — fast path 2.
  - `{"ab", nil}` — fast path 2, too short.
- `TestComputeSQLLimit`:
  - `nameHasLikeHints=true, offset=0, limit=10` → 5010.
  - `nameHasLikeHints=true, offset=44999, limit=10` → 50000.
  - `nameHasLikeHints=false, qnHasLikeHints=true` → 200K (or computed by needsNameScan logic).
  - `nameHasLikeHints=false, qnHasLikeHints=false` → `offset+limit+1000` formula.
- `TestSearchLikeHintPreFilter` — insert 1000 nodes named `"foo_N"` and 1 named `"Submit"`; search with `NamePattern=".*Submit.*"`; assert `Results` has exactly 1 node and it is `"Submit"`. Validates the full SQL→Go pipeline.
- `TestBuildSearchConditionsLikeHints` — call `buildSearchConditions` directly; assert `nameHasLikeHints=true` and condition slice contains a LIKE clause for `.*Submit.*`-style pattern.

**Unit (pipeline package — `FunctionRegistry`):**
- `TestFailedLookupsCache` — register no functions; call `Resolve("unknown", ...)` twice; second call must return `ResolutionResult{}` without scanning `byName` (verify via coverage or by observing `failedLookups` size is 1 after first call).
- `TestFailedLookupsNotCachedOnSuccess` — register `"pkg.Foo"`; call `Resolve("Foo", "pkg", nil)`; assert `failedLookups` is empty.
- `TestFailedLookupsConcurrent` — 50 goroutines call `Resolve("unknown", ...)` concurrently; run with `-race`; assert no data race.
- `TestFailedLookupsNotPopulatedByFuzzyResolve` — call `FuzzyResolve("unknown", ...)` for unknown name; `failedLookups` must remain empty.

**Integration:** The full pipeline integration test (`TestPipelineRun`) exercises `FunctionRegistry` end-to-end. Add a variant with a large number of unique callee names to verify performance improvement is measurable (optional benchmark).

### Mocks / dependencies

- `extractLikeHints`, `computeSQLLimit`, `buildSearchConditions`: pure functions — no mocks needed.
- `Search` integration tests: `OpenMemory()` — no filesystem mock.
- `FunctionRegistry`: pure in-memory struct — no mocks needed.
- For `-race` tests: use `go test -race ./internal/store/... ./internal/pipeline/...`.

---

## Test scenario matrix

Each row is a test function; columns indicate which of the four changes it covers.

| Test | Ch.1 Indexes+RO | Ch.2 ArchCache | Ch.3 Pipeline | Ch.4 SearchHints |
|------|:-:|:-:|:-:|:-:|
| `TestPartialIndexesExist` | X | | | |
| `TestPartialIndexDropAndRecreate` | X | | | |
| `TestOpenReadOnlyMissingFile` | X | | | |
| `TestOpenReadOnlySuccess` | X | | | |
| `TestOpenReadOnlyRejectsWrites` | X | | | |
| `TestOpenReadOnlyPragmas` | X | | | |
| `TestForProjectReadOnlyMissing` | X | | | |
| `TestForProjectReadOnlyExists` | X | | | |
| `TestForProjectReadOnlyNoCache` | X | | | |
| `TestArchLayersWithCached` | | X | | |
| `TestArchLayersWithNilCached` | | X | | |
| `TestFetchAspectsPassesBoundariesToLayers` | | X | | |
| `TestArchCacheHit` | | X | | |
| `TestArchCacheKeyIsolation` | | X | | |
| `TestArchCacheInvalidationBug` | | X | | |
| `TestBumpGraphVersionConcurrent` | | X | | |
| `TestArchCacheConcurrent` (race) | | X | | |
| `TestLouvainLocalMovingNoMoves` | | | X | |
| `TestLouvainLocalMovingWithMoves` | | | X | |
| `TestLouvainEarlyExitOnConvergence` | | | X | |
| `TestLouvainFractionThreshold` | | | X | |
| `TestChangedFilesAffectCommunities_Empty` | | | X | |
| `TestChangedFilesAffectCommunities_LeafOnly` | | | X | |
| `TestChangedFilesAffectCommunities_WithCalls` | | | X | |
| `TestChangedFilesAffectCommunities_Conservative_NoNodes` | | | X | |
| `TestChangedFilesAffectCommunities_Conservative_Error` | | | X | |
| `TestParallelPostFlushPasses` (race) | | | X | |
| `TestExtractLikeHints` (extended) | | | | X |
| `TestComputeSQLLimit` | | | | X |
| `TestSearchLikeHintPreFilter` | | | | X |
| `TestBuildSearchConditionsLikeHints` | | | | X |
| `TestFailedLookupsCache` | | | | X |
| `TestFailedLookupsNotCachedOnSuccess` | | | | X |
| `TestFailedLookupsConcurrent` (race) | | | | X |
| `TestFailedLookupsNotPopulatedByFuzzyResolve` | | | | X |
| Existing: `TestLouvainBasic` | | | X | |
| Existing: `TestLouvainConverges` | | | X | |
| Existing: `TestLouvainEmpty` | | | X | |
| Existing: `TestExtractLikeHints` | | | | X |
| Existing: `TestSearchCaseInsensitiveDefault` | | | | X |
| Existing: `TestPipelineRun` (integration) | | | X | X |

---

## External dependencies requiring real implementations (not mocks)

### SQLite / filesystem (Change 1 only)

`OpenReadOnly` **cannot** be tested with `OpenMemory()` because:
1. `OpenMemory` uses `:memory:` — no real file path exists.
2. `OpenReadOnly` calls `os.Stat` before opening, requiring a real filesystem path.
3. The `mode=ro` URI parameter is enforced by the SQLite driver based on the on-disk file; in-memory has no equivalent.

**Pattern**: use `os.MkdirTemp` + `store.OpenPath(filepath.Join(dir, "test.db"))` to create a seeded on-disk database, close it, then call `OpenReadOnly`.

### SQLite partial index behavior (Change 1)

Partial indexes using `json_extract` require SQLite 3.9+ (released 2015). The `mattn/go-sqlite3` driver bundles SQLite ≥ 3.43 as of 2024, so this is safe. No mock needed; verify with real driver.

### `sync.Map` / `sync.RWMutex` (Changes 2, 4)

These are standard library types. No mock needed. Race detection via `go test -race` is the correct verification tool.

### `errgroup` concurrency (Change 3)

`golang.org/x/sync/errgroup` — real library, no mock. Test via `go test -race`.

### Network / GitHub API

No changes touch network code. No mocks needed for these changes.

---

## Cross-cutting notes

### Existing tests that break or need updating

- **`TestArchLayers`** (architecture_test.go:219–242) calls `s.archLayers("test")` with **one argument** — this will fail to compile now that the signature is `archLayers(project string, cached []CrossPkgBoundary)`. The call site must be updated to `s.archLayers("test", nil)`.

- **`TestLouvain*`** tests call `louvain(nodes, edges)` which internally calls `louvainLocalMoving`. The signature of `louvainLocalMoving` changed (now returns two values instead of none). Because `louvainLocalMoving` is package-private and not called directly in tests, no test compilation breaks — but the return values are now used in the loop condition. All existing Louvain tests exercise the new exit logic and remain valid.

### Tests that do NOT exist and should be added (summary)

1. Any test for `OpenReadOnly` — none exist in the codebase.
2. Any test for `ForProjectReadOnly` — none exist.
3. Direct test for `louvainLocalMoving` return values.
4. Test for the early-exit fraction threshold (`< 0.001`).
5. Tests for `changedFilesAffectCommunities` — none exist.
6. Test for the arch-cache invalidation bug (delete key mismatch).
7. `TestComputeSQLLimit` as a unit test (currently only exercised indirectly).
8. `TestFailedLookupsCache` and concurrency variant.

### Race-detector-required tests

Run the following with `-race`:
```
go test -race ./internal/store/...
go test -race ./internal/pipeline/...
go test -race ./internal/tools/...
```
Specifically critical for: `TestArchCacheConcurrent`, `TestParallelPostFlushPasses`, `TestFailedLookupsConcurrent`, `TestBumpGraphVersionConcurrent`.
