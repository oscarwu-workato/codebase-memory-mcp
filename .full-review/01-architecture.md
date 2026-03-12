# Architectural Review: perf/week1-week2

**Scope:** Performance optimization changes across watcher, store, pipeline, and tools layers.
**Date:** 2026-03-12

---

## 1. Component Boundaries

### 1.1 Separation of Concerns

The performance changes span four packages with distinct responsibilities:

| Package | Responsibility | New Additions |
|---------|---------------|---------------|
| `watcher` | Filesystem change detection, debounce, project routing | fsnotify integration, snapshot fallback, project cache |
| `store` | Persistence, schema, SQL queries, Louvain algorithm | `community_cache` table, batched inserts, `OpenReadOnly` |
| `pipeline` | Indexing orchestration, community detection wiring | `communityGraphHash`, warm-start wiring, `changedFilesAffectCommunities` |
| `tools` | MCP tool handlers, request routing, caching | Architecture result cache, `invalidateArchCache`, `graphWriteVer` |

**Assessment:** The boundaries are well-drawn. Each package owns a coherent set of concerns. The watcher detects changes, the pipeline orchestrates reindexing, the store persists data, and the tools layer serves queries. No package reaches into another's internals.

### 1.2 Dual Louvain Implementation -- Severity: High

**Finding:** There are two independent Louvain community detection implementations:

1. **`store/louvain.go`** -- Used by `store.archClusters()` for the `get_architecture` "clusters" aspect. Uses a compact array-based adjacency representation (`louvainGraph`), random shuffle for convergence stability, and a refine phase. Accepts `louvainEdge` structs with dense index remapping.

2. **`pipeline/communities.go`** -- Used by `passCommunities()` for creating persistent Community nodes and MEMBER_OF edges. Uses `map[int64]map[int64]bool` adjacency, no random shuffle, no refine phase, and a different modularity gain formula.

**Impact:** These two implementations can produce different community assignments for the same graph. The `get_architecture` clusters aspect queries the store implementation, while the Community nodes written by the pipeline use the pipeline implementation. A caller seeing clusters from `get_architecture` and then querying Community nodes via `search_graph` would see inconsistent groupings.

**Recommendation:** Consolidate into a single Louvain implementation. The `store/louvain.go` version is more robust (random shuffle, refine phase, weighted edges, compact representation). The pipeline should call through to the store's implementation, converting its adjacency data as needed. This eliminates a maintenance burden (two algorithms to keep in sync) and a correctness concern (divergent results).

---

## 2. Dependency Management

### 2.1 Dependency Direction -- Severity: Low (Acceptable)

```
tools -> pipeline -> store
tools -> watcher -> store
```

Dependencies flow inward from the application boundary (tools) toward the data layer (store). This is correct. The `watcher` depends on `store` only for `StoreRouter`, `ProjectInfo`, and `Project` types -- all read-only queries. The `pipeline` depends on `store` for read/write operations.

No circular dependencies exist. The `tools` package creates the `watcher` and injects `syncProject` as an `IndexFunc` callback, avoiding a reverse dependency from watcher to tools.

### 2.2 Watcher-to-Store Coupling -- Severity: Low

The watcher takes a `*store.StoreRouter` directly rather than an interface. For a single concrete implementation with no plans for alternatives, this is pragmatic. The `IndexFunc` callback type is a clean abstraction for the reindex trigger.

If the watcher ever needed to be tested without a real SQLite store, the `StoreRouter` dependency would need to be extracted into an interface. The current test setup (using `NewRouterWithDir` with temp directories) works but is heavier than necessary.

### 2.3 Pipeline's Direct Store Dependency -- Severity: Low

`passCommunities()` calls `p.Store.LoadCommunityCache()` and `p.Store.SaveCommunityCache()` directly with `context.Background()`. This is consistent with the existing pattern throughout the pipeline, which uses `p.Store` for all DB operations. The hardcoded `context.Background()` is acceptable here since the pipeline already tracks cancellation via `p.ctx` and `checkCancel()` at pass boundaries, and these cache operations are fast.

---

## 3. Data Model

### 3.1 community_cache Schema -- Severity: Low

```sql
CREATE TABLE IF NOT EXISTS community_cache (
    project    TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
    node_id    INTEGER NOT NULL,
    community  INTEGER NOT NULL,
    graph_hash TEXT NOT NULL,
    PRIMARY KEY (project, node_id)
)
```

**Strengths:**
- CASCADE delete ties the cache lifecycle to the project, preventing orphaned cache data.
- The `(project, node_id)` primary key enforces one community assignment per node per project.
- `INSERT OR REPLACE` in `SaveCommunityCache` handles upserts cleanly.

**Index gap -- Severity: Medium:**
The `LoadCommunityCache` query filters on `WHERE project = ? AND graph_hash = ?`, but the only index is `idx_community_cache_project ON community_cache(project)`. The `graph_hash` column is not indexed. For a cache miss (hash mismatch), SQLite must scan all rows for the project, filter by `graph_hash`, and return zero rows. For projects with thousands of nodes, this scan happens on every cold-start load that hits a stale hash.

**Recommendation:** Add a composite index `ON community_cache(project, graph_hash)` to make cache-miss lookups O(1) instead of O(N). Alternatively, since the table is DELETE-then-INSERT on every save, the scan cost may be acceptable for now -- but it grows with project size.

### 3.2 graphHash Fingerprinting -- Severity: Medium

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

**Collision risk with XOR:**
XOR is commutative and self-cancelling: `a ^ b == b ^ a`, and `a ^ a == 0`. Two distinct edge sets can produce the same `edgeXOR` value if their per-edge `(sourceID ^ targetID)` values XOR to the same result. For example:
- Edges (1->4, 2->3): XOR = (1^4) ^ (2^3) = 5 ^ 1 = 4
- Edges (1->2, 3->4): XOR = (1^2) ^ (3^4) = 3 ^ 7 = 4

Both produce edgeXOR = 4 with the same node count and edge count. The nodeSum component provides additional discrimination, but the collision space is real for graphs that evolve by swapping edge endpoints.

**Practical impact:** A collision causes a warm-start from a stale partition. The Louvain algorithm will still converge to the correct result (warm-start is an optimization, not a correctness requirement), but it may take more iterations than a cold start -- negating the performance benefit. The test `TestCommunityGraphHashSameCountsDifferentTopology` covers one case but cannot exhaustively test the XOR collision space.

**Recommendation:** Replace XOR with a rolling hash that is position-sensitive. A simple improvement: hash each edge as `hash(sourceID, targetID)` using a non-commutative combiner (e.g., `nodeSum += sourceID*largePrime + targetID` per edge, or use FNV/xxHash on the sorted edge list). This eliminates the structural collision class.

### 3.3 Schema Migration Pattern -- Severity: Low

```go
// Migration: community_cache table for Louvain warm-start.
_, _ = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS community_cache ...`)
_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_community_cache_project ...`)
```

Migrations use `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS`, which are idempotent. Errors are silently discarded (` _, _ =`). This is consistent with the existing migration pattern for `project_summaries` and the `url_path_gen` column. For an embedded single-user SQLite database, this pragmatic approach avoids a formal migration framework. The risk is that a schema error (e.g., disk full) would be silently swallowed, but the next write operation would surface it.

---

## 4. Design Patterns

### 4.1 Cache Invalidation Strategy -- Severity: Low (Well-designed)

The `tools.Server` implements a two-tier invalidation strategy:

1. **Architecture result cache** (`archCache map[string]string`): Keyed by `"project:aspects"`. Invalidated by `invalidateArchCache(project)` which deletes all keys with the project prefix. Called after every successful index run (both `syncProject` and `startAutoIndex`).

2. **Graph write version** (`graphWriteVer sync.Map`): A monotonic counter per project, incremented via CAS loop on every invalidation. This provides a version stamp that downstream consumers could use to detect staleness without needing the full cache.

**Correctness:** The invalidation is triggered at the right point -- after `pipeline.Run()` succeeds and before the index lock is released. This means no stale reads can occur between the graph write and the cache clear, because the `indexMu` serializes all writes.

**Cache key design:** The key `projName + ":" + strings.Join(aspects, ",")` is deterministic because `parseAspects` returns aspects in the order they appear in the request, and users typically pass a fixed set. However, `["languages","packages"]` and `["packages","languages"]` would produce different cache keys for the same result. This is a minor inefficiency, not a correctness issue.

### 4.2 Warm-Start Pattern -- Severity: Low (Correct)

The warm-start pattern in `passCommunities()` follows a sound design:

```
1. Compute graphHash from current topology
2. Load cached partition matching (project, graphHash)
3. If hit: seed Louvain from prior assignments
4. If miss: cold start (each node in its own community)
5. After Louvain converges: save partition to cache
```

**Correctness properties:**
- **Stale data cannot cause incorrect results:** If the hash matches, the topology is (modulo hash collisions) identical, so the prior partition is a valid starting point. If it does not match, the algorithm cold-starts.
- **Cache is always refreshed after a run:** Even on a cache hit, the result is saved back, ensuring the cache reflects the latest partition.
- **Graceful degradation:** Errors loading/saving the cache are logged and swallowed -- the algorithm falls back to cold-start behavior.

### 4.3 Debounce Pattern -- Severity: Low (Correct)

The watcher's debounce in `triggerDebounced` uses `time.AfterFunc` with explicit `Stop()` before `Reset()`, per Go documentation. The context cancellation check inside the `AfterFunc` callback correctly prevents indexing after shutdown. The `debounceMu` protects the `debounceMap` access consistently.

One subtlety: the debounce timer callback captures `ctx` by closure. If the context is cancelled after the `select` check but before `w.indexFn(ctx, ...)` executes, the index function receives a cancelled context. This is handled correctly by the pipeline's `checkCancel()` calls, which detect context cancellation and abort early.

### 4.4 Fallback Poll Pattern -- Severity: Low

The dual-strategy approach (fsnotify for instant detection + 30s poll for safety net) is a well-established pattern for filesystem watching. The `watchAllProjects` priming step ensures fsnotify is registered before the poll loop starts, and `registerNewProjects` handles dynamically added projects. The `nextPoll` timestamp prevents redundant polls for the same project within the interval.

---

## 5. Architectural Consistency

### 5.1 Store Method Consistency -- Severity: Low

`SaveCommunityCache` and `LoadCommunityCache` use `s.db` directly (the raw `*sql.DB`) rather than `s.q` (the `Querier` interface that supports transactions). This is inconsistent with most other store methods which use `s.q`. The practical impact is low because:

- `SaveCommunityCache` manages its own transaction internally (`s.db.BeginTx`), so using `s.q` would incorrectly nest transactions.
- `LoadCommunityCache` is a read-only query that does not need transaction context.

However, this means `SaveCommunityCache` cannot participate in an outer transaction via `WithTransaction`. If the pipeline ever needs to make cache saves atomic with other writes, this would need refactoring. For the current use case (standalone cache writes), the direct `s.db` usage is correct.

### 5.2 Error Handling Consistency -- Severity: Low

The community cache operations follow the established pattern: errors are wrapped with context and propagated. In `passCommunities()`, cache load/save errors are logged as warnings and swallowed, allowing the algorithm to proceed with degraded performance but correct results. This matches the pattern used elsewhere in the pipeline (e.g., `passHTTPLinks` errors are warned and swallowed).

### 5.3 Batch Size Derivation -- Severity: Low (Well-documented)

```go
const cacheBatchSize = 249  // 999 (SQLite param limit) / 4 (columns) = 249
```

The batch size is derived from SQLite's compile-time `SQLITE_MAX_VARIABLE_NUMBER` limit (default 999) divided by the 4 columns per row. This is correct and well-documented. The batched INSERT loop handles remainder batches cleanly.

---

## 6. Thread Safety

### 6.1 archCache Concurrent Access -- Severity: Low (Correct)

The `archCache` is protected by `archCacheMu sync.RWMutex`:
- **Read path** (`handleGetArchitecture`): `RLock` for cache check, `RUnlock` before computation, `Lock` for cache write. Multiple concurrent reads can proceed in parallel.
- **Invalidation path** (`invalidateArchCache`): `Lock` for prefix-scanning deletion. Blocks reads during invalidation.

The read-then-compute-then-write pattern has a benign race: two concurrent requests for the same cache key may both miss the cache and compute the result independently. Both will write the same value, so this is safe (last-write-wins with identical data).

### 6.2 debounceMap Concurrent Access -- Severity: Low (Correct)

The `debounceMap` is protected by `debounceMu sync.Mutex`. The `AfterFunc` callback also acquires `debounceMu` before deleting the timer entry. The `Stop()` + `Reset()` pattern inside the lock prevents the documented race between timer firing and resetting.

### 6.3 cachedProjects Concurrent Access -- Severity: Low (Correct)

Protected by `cachedProjectsMu sync.RWMutex`:
- **Write** (`refreshProjectCache`): `Lock` to replace the slice.
- **Read** (`projectForPath`): `RLock` to copy the slice reference, then iterate outside the lock.

The read path copies the slice pointer under `RLock` and iterates over it after releasing the lock. This is safe because the writer replaces the entire slice (never mutates in place), and Go's memory model guarantees the reader sees a consistent snapshot of the slice reference.

### 6.4 graphWriteVer CAS Loop -- Severity: Low (Correct)

```go
for {
    v, _ := s.graphWriteVer.LoadOrStore(project, uint64(0))
    if s.graphWriteVer.CompareAndSwap(project, v, v.(uint64)+1) {
        break
    }
}
```

The CAS loop correctly handles concurrent increments. `LoadOrStore` initializes the entry if absent, and `CompareAndSwap` retries if another goroutine incremented between load and swap. Since `invalidateArchCache` is only called under `indexMu` (which serializes index operations), contention on this CAS loop is extremely unlikely in practice, but the implementation is correct even without that guarantee.

### 6.5 pollMu and projects Map -- Severity: Low (Mostly Correct)

The `projects` map is protected by `pollMu` in most access paths. One concern: `pollProject` receives a `*projectState` pointer obtained under `pollMu` (in `getOrCreateState`), but `pollProject` itself does not hold `pollMu` while modifying `state.snapshot` and `state.nextPoll`. This is safe only because:
- `pollAll` is called from a single goroutine (the ticker loop in `runFallbackPoll`).
- The fsnotify goroutine never touches `projects` entries' snapshots (it only triggers debounced reindexing).

If `pollAll` were ever called concurrently (e.g., from multiple goroutines), the unsynchronized `state.snapshot` writes would race. The current single-goroutine design makes this safe, but it is an implicit invariant worth documenting.

---

## 7. Summary of Findings

| # | Severity | Component | Finding | Recommendation |
|---|----------|-----------|---------|----------------|
| 1 | **High** | store + pipeline | Two independent Louvain implementations that can produce divergent community assignments | Consolidate into a single implementation in `store/louvain.go`; pipeline calls through |
| 2 | **Medium** | pipeline | `communityGraphHash` XOR-based fingerprint has structural collision classes | Replace XOR with a position-sensitive hash combiner |
| 3 | **Medium** | store | `community_cache` index on `(project)` alone; query filters on `(project, graph_hash)` | Add composite index `ON community_cache(project, graph_hash)` |
| 4 | **Low** | tools | Architecture cache key is aspect-order-dependent | Sort aspects before joining to normalize cache keys |
| 5 | **Low** | store | `SaveCommunityCache` uses `s.db` directly instead of `s.q` | Acceptable for current use; document the design choice |
| 6 | **Low** | watcher | `pollProject` modifies `projectState` without holding `pollMu` | Safe under current single-goroutine design; add a comment documenting the invariant |
| 7 | **Low** | pipeline | `passCommunities()` uses `context.Background()` for cache operations | Consistent with existing pipeline patterns; acceptable |

### Overall Assessment

The performance changes are architecturally sound. Component boundaries are clean, dependency direction is correct, and thread safety is handled properly throughout. The cache invalidation and warm-start patterns are well-designed with correct fallback behavior.

The most actionable finding is the dual Louvain implementation (#1), which creates both a maintenance burden and a consistency gap between `get_architecture` clusters and persistent Community nodes. The graph hash collision risk (#2) and missing composite index (#3) are worth addressing but do not affect correctness -- only performance characteristics of the warm-start optimization itself.
