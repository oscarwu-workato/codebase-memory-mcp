# Code Quality Review: perf/week1-week2

**Reviewer:** Claude Code (Opus 4.6)
**Date:** 2026-03-12
**Scope:** Performance changes across 10 files on `perf/week1-week2` branch
**Method:** Manual line-by-line analysis of architecture, code quality, error handling, complexity, duplication, and technical debt

---

## Summary

| Severity | Count |
|----------|-------|
| Critical | 2     |
| High     | 7     |
| Medium   | 12    |
| Low      | 8     |
| **Total** | **29** |

The performance changes are generally well-structured and the codebase shows strong engineering discipline. The two critical findings are a duplicate Louvain implementation creating a maintenance trap, and a hash collision weakness in the community cache invalidation scheme. The high-severity items center on race conditions in the watcher debounce logic, swallowed errors in incremental indexing, and a `computeSQLLimit` asymmetry. Medium and low findings cover naming, complexity, and minor code smells.

---

## Critical

### C-1. Duplicate Louvain Implementations -- Maintenance Trap and Divergence Risk

**Files:**
- `/content/codebase-memory-mcp/internal/store/louvain.go` (full file)
- `/content/codebase-memory-mcp/internal/pipeline/communities.go:108-193`

**Description:**
Two complete, independent implementations of the Louvain community detection algorithm exist in the codebase:

1. `store/louvain.go` -- `louvainWithWarmStart()` operating on `[]int64` + `[]louvainEdge` with compact adjacency lists, weighted edges, float64 modularity, and a `louvainRefine` split-checking phase. Called from `store/architecture.go:642` via `louvain()`.

2. `pipeline/communities.go` -- `louvainCommunities()` operating on `map[int64]map[int64]bool` with unweighted edges, per-community `commSumTot` accumulators, and no refinement phase. Called from `pipeline/communities.go:66`.

These two implementations differ in:
- Data structures (dense adjacency `[][]int` vs sparse `map[int64]map[int64]bool`)
- Edge weighting (weighted vs unweighted)
- Convergence criteria (fraction-based early exit vs pure `improved` flag)
- Post-processing (refinement/split phase vs none)
- Iteration cap (15 vs 50)
- Warm-start initialization logic (remapped dense indices vs direct ID preservation)

Both are called during a single pipeline run -- the `store` version via `archClusters` (architecture aspect) and the `pipeline` version via `passCommunities` (MEMBER_OF edges). They can produce different community assignments for the same graph, meaning the architecture "clusters" view and the "communities" stored as graph nodes may disagree.

**Impact:** Bug-prone divergence. A fix or improvement to one algorithm will not propagate to the other. The warm-start cache (saved by `passCommunities`) is keyed on a graph hash, but `archClusters` runs its own separate Louvain from scratch every time, never benefiting from the cache.

**Recommendation:** Consolidate into a single implementation behind a shared interface. The `store/louvain.go` version is more complete (weighted edges, refinement), so the pipeline should delegate to it. The `pipeline/communities.go` Louvain iteration logic (lines 108-211) should be removed and replaced with a call to the store-package algorithm, adapted to accept the pipeline's adjacency format.

---

### C-2. Weak Community Graph Hash -- Collision-Prone Fingerprint

**File:** `/content/codebase-memory-mcp/internal/pipeline/communities.go:16-25`

```go
func communityGraphHash(allNodes map[int64]bool, callEdges []*store.Edge) string {
    var nodeSum, edgeXOR uint64
    for id := range allNodes {
        nodeSum += uint64(id)
    }
    for _, e := range callEdges {
        edgeXOR ^= uint64(e.SourceID) ^ uint64(e.TargetID)
    }
    return fmt.Sprintf("%d:%d:%x:%x", len(allNodes), len(callEdges), nodeSum, edgeXOR)
}
```

**Description:**
This fingerprint is the sole guard for the community cache. If two different graph topologies produce the same hash, a stale partition is loaded and used as the warm-start, producing silently wrong community assignments. The hash is weak in two specific ways:

1. **Node sum is commutative and collides trivially.** Swapping node IDs 5 and 10 for IDs 8 and 7 produces the same sum (15) with the same count (2). For a real codebase where nodes are added/removed during incremental indexing, the probability of sum collisions is non-trivial.

2. **Edge XOR is symmetric and self-canceling.** `XOR(A ^ B)` is identical whether the edge is `(A, B)` or `(B, A)`, which is intentional for undirected graphs. But XOR also means that adding two identical edges cancels out (XOR is its own inverse). More importantly, XOR of `(1,4)` and `(2,3)` equals XOR of `(1,2)` and `(3,4)` -- completely different topologies produce identical checksums.

**Impact:** A false cache hit loads a stale partition as warm-start. Since Louvain is iterative, a bad warm-start can converge to a local minimum that differs from the correct partition. The resulting community nodes and MEMBER_OF edges will be incorrect, affecting the `clusters` architecture aspect and any downstream analysis.

**Recommendation:** Replace with a proper hash. Either:
- Use `xxh3` (already a dependency) over a deterministic serialization of sorted `(source, target)` edge pairs.
- Or combine node IDs via a non-commutative hash (e.g., hash of sorted ID list) and edge pairs via a hash of sorted `(min(src,tgt), max(src,tgt))` tuples.

The fix is small and the `xxh3` dependency already exists in `pipeline.go` imports.

---

## High

### H-1. Race Condition in Debounce Timer Callback

**File:** `/content/codebase-memory-mcp/internal/watcher/watcher.go:210-241`

```go
func (w *Watcher) triggerDebounced(ctx context.Context, proj *store.ProjectInfo) {
    w.debounceMu.Lock()
    if t, ok := w.debounceMap[proj.Name]; ok {
        t.Stop()
        t.Reset(debounceWindow)
    } else {
        name := proj.Name
        rootPath := proj.RootPath
        w.debounceMap[proj.Name] = time.AfterFunc(debounceWindow, func() {
            select {
            case <-ctx.Done():
                w.debounceMu.Lock()
                delete(w.debounceMap, name)
                w.debounceMu.Unlock()
                return
            default:
            }
            if err := w.indexFn(ctx, name, rootPath); err != nil {
                slog.Warn("watcher.index", "project", name, "err", err)
            }
            w.debounceMu.Lock()
            delete(w.debounceMap, name)
            w.debounceMu.Unlock()
        })
    }
    w.debounceMu.Unlock()
}
```

**Description:**
The `time.AfterFunc` callback runs in its own goroutine. Between the timer firing and the callback acquiring `debounceMu.Lock()` (line 235), another call to `triggerDebounced` can observe the timer still in the map (line 214), call `t.Stop()` which returns `false` (timer already fired), and then call `t.Reset()`. Per the Go docs, `Reset` on an already-fired timer has undefined channel behavior, and more importantly the callback is already executing -- it will complete `indexFn`, then delete the key from the map. Meanwhile the `Reset` creates a second firing that will also call `indexFn`. This means two concurrent index runs for the same project, bypassing the `TryLock` guard in `syncProject` only if they overlap in time.

**Recommendation:** Track an `inflight` flag per project alongside the timer, or use a channel-based debounce pattern that serializes timer resets and callback execution.

---

### H-2. Swallowed Errors in Incremental File Deletion

**File:** `/content/codebase-memory-mcp/internal/pipeline/pipeline.go:424-426`

```go
for _, f := range changed {
    _ = p.Store.DeleteNodesByFile(p.ProjectName, f.RelPath)
}
```

And similarly at lines 462-469:
```go
_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "CALLS")
_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "USAGE")
// ... 6 more ignored errors
```

**Description:**
Eight `DeleteEdgesBySourceFile` calls and one `DeleteNodesByFile` call discard errors silently. If any of these fail (e.g., SQLite busy/locked), stale nodes or edges remain in the database while new ones are inserted, leading to duplicates. Since this runs inside a transaction, the error should propagate to trigger a rollback.

**Recommendation:** Check errors and return early. At minimum, accumulate errors with `errors.Join` and return them. The transaction wrapper in `Run()` will handle rollback.

---

### H-3. `computeSQLLimit` Ignores `qnHasLikeHints`

**File:** `/content/codebase-memory-mcp/internal/store/search.go:172-202`

**Description:**
The function accepts both `nameHasLikeHints` and `qnHasLikeHints` as parameters, but only checks `nameHasLikeHints` for the 50K cap optimization (line 186). When a search uses only `qn_pattern` with extractable LIKE hints (and no `name_pattern`), the function falls through to the default path and returns `offset + limit + 1000`, which is correct for small pages but means the 50K cap optimization is never applied to QN-only searches. This is an asymmetry rather than a correctness bug, but:

1. The parameter `qnHasLikeHints` is computed and passed but never consulted, which is dead logic.
2. A user searching by `qn_pattern` alone with extractable hints gets no benefit from the LIKE pre-filtering cap.

The existing test suite (`perf_search_test.go:107-117`) actually tests this case and expects 1010 (the default path), confirming the asymmetry is tested-as-implemented rather than a regression. However, it means the optimization is incomplete.

**Recommendation:** Apply the same 50K cap when `qnHasLikeHints` is true and no name scan is needed. Change line 186 to `if nameHasLikeHints || qnHasLikeHints {`.

---

### H-4. `buildOneCluster` Iterates All Edges Per Community -- O(C * E)

**File:** `/content/codebase-memory-mcp/internal/store/architecture.go:700-758`

```go
func buildOneCluster(commID int, members []int64, edges []louvainEdge, ...) ClusterInfo {
    // ...
    for _, e := range edges {  // ALL edges, not just community edges
        isSrc := memberSet[e.src]
        isDst := memberSet[e.dst]
        // ...
    }
```

**Description:**
`buildOneCluster` is called once per community, and each call iterates over the entire edge list. For a codebase with C communities and E edges, this is O(C * E). For a moderately large codebase (10K edges, 50 communities), this is 500K iterations. For large codebases (100K edges, 200 communities), this becomes 20M iterations.

**Recommendation:** Pre-compute a `map[int][]louvainEdge` (community -> internal edges) and `map[int]int` (community -> total incident edges) in a single O(E) pass inside `buildClusterInfos`, then pass the pre-computed data to `buildOneCluster` instead of the raw edge list.

---

### H-5. Unbounded `failedLookups` Cache Growth

**File:** `/content/codebase-memory-mcp/internal/pipeline/resolver.go:28-29`

```go
failedMu      sync.RWMutex
failedLookups map[string]bool
```

**Description:**
The `failedLookups` map caches simple names that failed to resolve, preventing repeated project-wide scans. However, it grows without bound over the lifetime of a `FunctionRegistry`. For large codebases with many external/stdlib calls (e.g., `fmt.Println`, `http.Get`, `json.Marshal`), this map can accumulate thousands of entries. Since the registry is created per pipeline run and discarded after, this is bounded by pipeline lifetime, but the map is never cleared between incremental passes within a single `Run()` call. If a newly-added file defines a function whose simple name was previously cached as "unresolvable," it will not be found.

**Recommendation:** Clear `failedLookups` after `buildRegistry()` is called, since the registry contents have changed and previously-failed names may now be resolvable.

---

### H-6. `pollAll` Opens Store Per Project Without Closing

**File:** `/content/codebase-memory-mcp/internal/watcher/watcher.go:280-296`

```go
for _, info := range projectInfos {
    st, stErr := w.router.ForProject(info.Name)
    if stErr != nil {
        continue
    }
    proj, projErr := st.GetProject(info.Name)
```

**Description:**
`ForProject` returns a `*store.Store` which wraps a SQLite connection. In the poll loop (every 30 seconds), this is called for every project. If `ForProject` opens a new connection each time (depends on the router implementation), this could leak connections. Even if the router caches connections, the pattern of `continue` on error without any cleanup is concerning -- an error from `GetProject` means the store was obtained but its result was discarded without close.

**Recommendation:** Verify that `StoreRouter.ForProject` returns cached/pooled connections. If it does, add a comment documenting that. If it can return fresh connections, add cleanup.

---

### H-7. `loadConnectedNames` Ignores `rows.Err()`

**File:** `/content/codebase-memory-mcp/internal/store/search.go:46-63`

```go
func (s *Store) loadConnectedNames(sr *SearchResult, nodeID int64) {
    // ...
    for connRows.Next() {
        // ...
    }
    _ = connRows.Err()  // error explicitly discarded
}
```

**Description:**
The `rows.Err()` check after iteration is the standard Go pattern for detecting errors that occurred during `Next()` iteration (e.g., connection dropped mid-scan). Discarding this error means the caller receives a partial result set with no indication that it is incomplete. Since `loadConnectedNames` is called per search result when `include_connected=true`, a transient SQLite error would silently truncate neighbor lists across all results.

**Recommendation:** Return an error from `loadConnectedNames`, or at minimum log the error at Warn level.

---

## Medium

### M-1. `louvainLocalMoving` Has 8 Parameters

**File:** `/content/codebase-memory-mcp/internal/store/louvain.go:144`

```go
func louvainLocalMoving(n int, adj [][]int, weight [][]float64, degree []float64,
    community []int, totalWeight, resolution float64) (int, bool) {
```

**Description:** 7 positional parameters (8 counting both return values). The `louvainGraph` struct already exists and holds `n`, `adj`, `weight`, `degree`, and `totalWeight`. This function should accept `louvainGraph` plus `community` and `resolution`.

**Recommendation:** Refactor to `func louvainLocalMoving(g louvainGraph, community []int, resolution float64) (int, bool)`.

---

### M-2. `louvainRefine` Has 8 Parameters

**File:** `/content/codebase-memory-mcp/internal/store/louvain.go:205`

Same issue as M-1. Both `louvainRefine` and `refineCommunity` (line 219) also accept 8 positional parameters.

---

### M-3. `buildOneCluster` Has 6 Parameters

**File:** `/content/codebase-memory-mcp/internal/store/architecture.go:700`

```go
func buildOneCluster(commID int, members []int64, edges []louvainEdge,
    edgeTypes map[string]bool, fanIn map[int64]int,
    nodeByID map[int64]clusterNodeInfo) ClusterInfo {
```

**Recommendation:** Group `edges`, `edgeTypes`, `fanIn`, `nodeByID` into a `clusterContext` struct passed to all cluster-building functions.

---

### M-4. `initNodeCommunities` Duplicates `initCommunities` Logic

**Files:**
- `/content/codebase-memory-mcp/internal/pipeline/communities.go:80-106` (`initNodeCommunities`)
- `/content/codebase-memory-mcp/internal/store/louvain.go:73-98` (`initCommunities`)

**Description:** Both functions perform the same conceptual operation -- assign initial community IDs to nodes, optionally seeding from a warm-start map. They differ only in type signatures (`map[int64]bool` vs `[]int64`, output `map[int64]int` vs `[]int`). This is a direct consequence of the duplicate Louvain implementations (C-1).

---

### M-5. `communityGraphHash` Uses `fmt.Sprintf` for Hot-Path Hash

**File:** `/content/codebase-memory-mcp/internal/pipeline/communities.go:24`

```go
return fmt.Sprintf("%d:%d:%x:%x", len(allNodes), len(callEdges), nodeSum, edgeXOR)
```

**Description:** `fmt.Sprintf` with `%x` formatting allocates and is relatively slow. Since this function is called on every community detection pass, and the result is used as a cache key, a `strconv`-based or binary encoding approach would be faster and avoid the format string parsing overhead.

**Recommendation:** Use `strconv.AppendUint` into a reusable buffer, or encode as raw bytes for the hash key.

---

### M-6. `SearchParams.MinDegree` / `MaxDegree` Zero-Value Ambiguity

**File:** `/content/codebase-memory-mcp/internal/store/search.go:19-20`

**Description:** The zero value of `int` is 0, but the semantic "no filter" sentinel is -1 (set by callers via `getIntArg(args, "min_degree", -1)`). If any caller forgets the -1 default and constructs `SearchParams{}` directly, `MinDegree=0` and `MaxDegree=0` become active filters that exclude all nodes with any edges. The test files consistently use -1, but the struct type itself does not enforce this.

**Recommendation:** Use `*int` (pointer) for optional filters, where `nil` means "no filter." Alternatively, document the -1 sentinel in the struct field comments.

---

### M-7. `extractLikeHints` Minimum Hint Length Is Hardcoded

**File:** `/content/codebase-memory-mcp/internal/store/search.go:485-486`

```go
if current.Len() >= 3 { // only use hints >= 3 chars to be selective
```

**Description:** The magic number 3 appears twice (lines 460, 465, 485, 495) with the same comment. This threshold determines when a literal substring is considered selective enough for SQL LIKE pre-filtering. It should be a named constant.

**Recommendation:** Extract `const minLikeHintLen = 3`.

---

### M-8. `addProjectDirs` Silently Skips Unreadable Entries

**File:** `/content/codebase-memory-mcp/internal/watcher/watcher.go:176-186`

```go
walkErr := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
    if err != nil {
        return nil // skip unreadable entries
    }
```

**Description:** Permission errors, broken symlinks, and other filesystem errors are silently swallowed during the recursive directory walk. While this is a common pattern for non-critical directory enumeration, it means entire subtrees can be silently excluded from watching. A single permission error on a parent directory skips all its children.

**Recommendation:** Log at Debug level with the path and error, consistent with how `fsw.Add` errors are logged at line 182.

---

### M-9. `compareVersions` Silently Treats Non-Numeric Segments as 0

**File:** `/content/codebase-memory-mcp/internal/tools/tools.go:365-366`

```go
ai, _ := strconv.Atoi(aParts[i])
bi, _ := strconv.Atoi(bParts[i])
```

**Description:** If a version string contains non-numeric segments (e.g., "0.2.1-beta"), `Atoi` returns 0 for the unparseable segment. This means "0.2.1-beta" compares equal to "0.2.0" (both parse the third segment as 0). While pre-release versions are uncommon for this tool, the silent fallback could cause an update notice to not appear when it should.

**Recommendation:** At minimum, strip known suffixes (`-alpha`, `-beta`, `-rc`) before parsing. Or use a proper semver library.

---

### M-10. `handleFSEvent` Does Not Filter Ignored Paths

**File:** `/content/codebase-memory-mcp/internal/watcher/watcher.go:110-126`

**Description:** Every filesystem event triggers `projectForPath` and potentially `triggerDebounced`, including events in `.git/`, `node_modules/`, `__pycache__/`, and other directories that should never trigger reindexing. The `discover.Discover` function presumably filters these during indexing, but the watcher debounce timer fires for every event, causing unnecessary lock contention and timer churn.

**Recommendation:** Add a fast-path exclusion check for common ignored directories before calling `projectForPath`.

---

### M-11. `buildArchResponse` Verbose Nil-Check Chain

**File:** `/content/codebase-memory-mcp/internal/tools/architecture.go:109-142`

**Description:** 10 sequential `if info.X != nil { data["x"] = info.X }` blocks. This is repetitive but not incorrect. The `ArchitectureInfo` struct has JSON tags with `omitempty`, so the response map construction mirrors what `json.Marshal` would do if the struct were serialized directly.

**Recommendation:** Consider marshaling `ArchitectureInfo` to JSON and then unmarshaling to `map[string]any`, or use `reflect` to iterate fields. Low priority since the current code is explicit and readable.

---

### M-12. `passCallsForFiles` Silently Continues on ReadFile Error

**File:** `/content/codebase-memory-mcp/internal/pipeline/pipeline.go:673-676`

```go
source, err := os.ReadFile(f.Path)
if err != nil {
    continue
}
```

**Description:** If a file cannot be read during incremental call resolution, it is silently skipped. No log message, no error propagation. The file's call edges will be missing from the graph with no indication to the user.

**Recommendation:** Log at Warn level with the file path and error.

---

## Low

### L-1. `pollInterval` Is Dead Code

**File:** `/content/codebase-memory-mcp/internal/watcher/watcher.go:375-381`

```go
func pollInterval(fileCount int) time.Duration {
    ms := 1000 + (fileCount/500)*1000
    if ms > 60000 {
        ms = 60000
    }
    return time.Duration(ms) * time.Millisecond
}
```

**Description:** The comment says "retained for tests that reference it directly," but the watcher now uses the constant `fallbackInterval` (30s) instead of adaptive polling. If tests depend on this function, the tests are testing dead logic. Per the global CLAUDE.md: "Replace, don't deprecate."

**Recommendation:** Remove `pollInterval` and update any tests that reference it.

---

### L-2. Inconsistent Docstring Comment on `getBoolArg`

**File:** `/content/codebase-memory-mcp/internal/tools/tools.go:840-841`

```go
// getBoolArg extracts a boolean argument from parsed args.
// getFloatArg extracts a float64 argument with a default value.
func getFloatArg(args map[string]any, key string, defaultVal float64) float64 {
```

**Description:** The comment for `getBoolArg` is attached to `getFloatArg`. The actual `getBoolArg` at line 853 has no docstring.

---

### L-3. `captureSnapshot` Creates Background Context

**File:** `/content/codebase-memory-mcp/internal/watcher/watcher.go:336`

```go
files, err := discover.Discover(context.Background(), rootPath, nil)
```

**Description:** The parent `pollProject` is called with a context (via `pollAll`), but `captureSnapshot` creates `context.Background()` instead of accepting and propagating the caller's context. If the watcher is shutting down, `captureSnapshot` will complete its full directory walk before the cancellation takes effect.

**Recommendation:** Pass the caller's context through.

---

### L-4. `SaveCommunityCache` Uses `INSERT OR REPLACE` but Deletes First

**File:** `/content/codebase-memory-mcp/internal/store/community_cache.go:21-24`

```go
if _, err := tx.ExecContext(ctx, `DELETE FROM community_cache WHERE project = ?`, project); err != nil {
    _ = tx.Rollback()
    return err
}
// ... later:
q := "INSERT OR REPLACE INTO community_cache(...) VALUES " + ...
```

**Description:** The `DELETE` on line 21 removes all rows for the project, so the subsequent `INSERT OR REPLACE` will never trigger the `REPLACE` (conflict) path. The `OR REPLACE` clause is a no-op. This is not a bug, but it is misleading -- a reader might think the code handles partial updates when it actually does full replacement.

**Recommendation:** Change to plain `INSERT INTO` since the preceding `DELETE` guarantees no conflicts.

---

### L-5. `LoadCommunityCache` Checks `sql.ErrNoRows` on `QueryContext`

**File:** `/content/codebase-memory-mcp/internal/store/community_cache.go:66-68`

```go
if errors.Is(err, sql.ErrNoRows) {
    return nil, nil
}
```

**Description:** `db.QueryContext` returns `*sql.Rows` and does not return `sql.ErrNoRows` -- that error is only returned by `QueryRow().Scan()`. This check is dead code. An empty result set from `QueryContext` simply means `rows.Next()` returns `false` on the first call, which is correctly handled by the `len(result) == 0` check at line 86-88.

**Recommendation:** Remove the dead `ErrNoRows` check.

---

### L-6. `louvainIteration` Iterates Over Map (Non-Deterministic Order)

**File:** `/content/codebase-memory-mcp/internal/pipeline/communities.go:153`

```go
for nodeID, neighbors := range adj {
```

**Description:** Go map iteration order is randomized. In `store/louvain.go`, nodes are shuffled explicitly for convergence stability (line 158). In `pipeline/communities.go`, the map iteration provides implicit randomization, but the behavior depends on Go's internal map implementation and is not guaranteed to be uniform. This can cause non-reproducible community assignments between runs.

**Recommendation:** Not a functional bug (Louvain is inherently non-deterministic), but document the intentional reliance on map randomization to prevent future "fix" attempts that sort the keys.

---

### L-7. `archClusters` Scans All Edges Twice

**File:** `/content/codebase-memory-mcp/internal/store/architecture.go:658-676`

**Description:** After calling `louvain(nodeIDs, edges)`, the function iterates `edges` again to build `commEdgeTypes` and `fanIn` maps (lines 658-676). The Louvain algorithm already processes all edges internally. If the algorithm returned edge-type and fan-in metadata as a side product, this second pass could be avoided.

**Recommendation:** Low priority. The second pass is O(E) and edges are already in memory. Note this in a future optimization pass.

---

### L-8. Magic Number 999 in `cacheBatchSize`

**File:** `/content/codebase-memory-mcp/internal/store/community_cache.go:12-13`

```go
const cacheBatchSize = 249
```

**Description:** The comment explains the derivation (999 / 4 = 249), but the SQLite parameter limit (999) is a well-known constraint that could change in different SQLite builds or future versions. The magic number should reference the source constant or be derived programmatically.

**Recommendation:** Define `const sqliteMaxParams = 999` and derive `cacheBatchSize = sqliteMaxParams / 4`.

---

## Cross-Cutting Observations

### Positive Patterns

1. **Consistent error wrapping** with `fmt.Errorf("context: %w", err)` throughout the pipeline.
2. **Batch operations** for SQL writes (community cache, node/edge upserts) respect the SQLite 999-parameter limit.
3. **Context cancellation checks** at pass boundaries in the pipeline prevent wasted work.
4. **Read-write lock discipline** in `FunctionRegistry` -- write path uses `Lock()`, read paths use `RLock()`.
5. **Architecture cache invalidation** via CAS-loop version counter is correct and lock-free.
6. **Debounce-then-index** pattern in the watcher is the right architecture for filesystem watching.
7. **Warm-start Louvain** is a sound optimization for incremental community detection.

### Technical Debt Indicators

1. The dual Louvain implementations (C-1) are the largest debt item. They add ~300 lines of duplicated algorithmic code.
2. The `pollInterval` dead code (L-1) indicates incomplete cleanup from the Week 2 watcher rewrite.
3. The `qnHasLikeHints` unused parameter (H-3) indicates the optimization was partially implemented.
4. Several functions exceed the 100-line guideline: `runFullPasses` (~130 lines), `runIncrementalPasses` (~120 lines), `archClusters` (~100 lines).

### Test Coverage Assessment

The test files listed in the scope (8 test files, 30+ tests) cover the key performance-sensitive paths:
- Community cache save/load (11 tests)
- Louvain warm-start (4 tests)
- Search LIKE hints and SQL limit (multiple tests)
- Watcher debounce and polling (8 tests)
- Architecture cache (perf tests)

Notable gap: No test for `communityGraphHash` collision resistance, which is the foundation of the cache correctness (C-2).
