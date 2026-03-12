# Week 2 Performance Changes — Test Architecture

## 1. Test File Layout

### New files

| File | Package | Replaces / extends |
|------|---------|-------------------|
| `internal/store/community_cache_test.go` | `package store` | New file; covers C-1 through C-10 |
| `internal/store/louvain_test.go` | `package store` | New file; covers L-1 through L-10 (warm-start tests not yet in `perf_louvain_test.go`) |
| `internal/pipeline/communities_test.go` | `package pipeline` | New file; covers H-1 through H-6 and P-1 through P-6 |

### Existing files to extend

| File | What to add |
|------|-------------|
| `internal/watcher/watcher_test.go` | All W-1 through W-17 tests listed in §3 below that are not already present. The existing file covers `TestSnapshotsEqual`, `TestPollInterval`, `TestCaptureSnapshot`, `TestCaptureSnapshotDetectsChanges`, `TestWatcherTriggersOnChange`, `TestWatcherCancellation`, `TestWatcherSkipsMissingRoot`, `TestWatcherNewFileTriggersIndex`. Everything else is additive. |
| `internal/store/perf_store_test.go` | Add `TestDropCreateIndexesIncludesCacheIndex` (C-11). The existing `indexSet` helper already queries `pragma_index_list('nodes')` — extend it or add a parallel helper for `community_cache`. |

---

## 2. Fixture Design

### 2.1 Shared store helpers (`internal/store/`)

```go
// openMemoryWithProject opens an in-memory SQLite store and registers a project row.
// All community_cache tests use this instead of repeating the boilerplate.
func openMemoryWithProject(t *testing.T, project, rootPath string) *Store {
    t.Helper()
    s, err := OpenMemory()
    if err != nil {
        t.Fatalf("OpenMemory: %v", err)
    }
    t.Cleanup(func() { s.Close() })
    if err := s.UpsertProject(project, rootPath); err != nil {
        t.Fatalf("UpsertProject: %v", err)
    }
    return s
}

// makePartition builds a uniform partition map where every node ID maps to the
// same community value. Used by warm-start seeding tests.
func makePartition(nodeIDs []int64, community int) map[int64]int {
    p := make(map[int64]int, len(nodeIDs))
    for _, id := range nodeIDs {
        p[id] = community
    }
    return p
}

// twoClusterNodes returns nodes and edges for two fully-connected 5-node
// cliques joined by a single bridge edge. Reused across L-10, P-1, P-2, P-3.
func twoClusterNodes() (nodes []int64, edges []louvainEdge) {
    for i := int64(1); i <= 5; i++ {
        nodes = append(nodes, i)
        for j := i + 1; j <= 5; j++ {
            edges = append(edges, louvainEdge{src: i, dst: j})
        }
    }
    for i := int64(6); i <= 10; i++ {
        nodes = append(nodes, i)
        for j := i + 1; j <= 10; j++ {
            edges = append(edges, louvainEdge{src: i, dst: j})
        }
    }
    edges = append(edges, louvainEdge{src: 5, dst: 6}) // bridge
    return
}
```

`twoClusterNodes` mirrors the shape already used in `TestLouvainEarlyExitOnConvergence`
(in `perf_louvain_test.go`) to guarantee consistent test data.

### 2.2 Shared pipeline helpers (`internal/pipeline/`)

```go
// seedCallGraph upserts minimal Function nodes and CALLS edges into store s.
// edges is a slice of [source-index, dest-index] pairs into a flat node ID array.
// Returns the resulting node ID slice in insertion order.
func seedCallGraph(
    t *testing.T,
    s *store.Store,
    project string,
    nodeCount int,
    edgePairs [][2]int,
) []int64 {
    t.Helper()
    ids := make([]int64, nodeCount)
    for i := range ids {
        id, err := s.UpsertNode(&store.Node{
            Project:       project,
            Label:         "Function",
            Name:          fmt.Sprintf("fn%d", i),
            QualifiedName: fmt.Sprintf("%s.fn%d", project, i),
        })
        if err != nil {
            t.Fatalf("UpsertNode fn%d: %v", i, err)
        }
        ids[i] = id
    }
    for _, pair := range edgePairs {
        _, err := s.InsertEdge(&store.Edge{
            Project:  project,
            SourceID: ids[pair[0]],
            TargetID: ids[pair[1]],
            Type:     "CALLS",
        })
        if err != nil {
            t.Fatalf("InsertEdge %v: %v", pair, err)
        }
    }
    return ids
}
```

This replaces `upsertTestNode` + inline `InsertEdge` calls scattered across integration
tests. `upsertTestNode` from `perf_pipeline_test.go` is already scoped to that file; do
not move it — simply define `seedCallGraph` alongside it in `communities_test.go`.

### 2.3 Shared watcher helpers (`internal/watcher/`)

`newTestRouter` already exists in `watcher_test.go` and is sufficient. No new helpers
are needed for the watcher package; tests that require `triggerDebounced` or
`projectForPath` construct a `*Watcher` directly via `New(r, indexFn)` and call the
unexported methods as package-internal tests.

---

## 3. Mock / Stub Strategy

### IndexFunc counter (watcher tests)

Follow the pattern already in `watcher_test.go` precisely:

```go
var indexCount atomic.Int32
indexFn := func(_ context.Context, _, _ string) error {
    indexCount.Add(1)
    return nil
}
```

For error-tolerance tests (W-17, P-5, P-6), use a variant that returns a sentinel error
on the first call only:

```go
var calls atomic.Int32
indexFn := func(_ context.Context, _, _ string) error {
    if calls.Add(1) == 1 {
        return errors.New("synthetic index error")
    }
    return nil
}
```

### No DB mocks

All store tests use `OpenMemory()` (in-process SQLite, <1 ms). No interfaces are
swapped. The `*store.Store` passed to `Pipeline` is a live `OpenMemory()` instance.

### Pipeline `Store` field injection

`Pipeline.Store` is an exported field. Integration tests set it directly:

```go
p, s := newTestPipeline(t)   // from perf_pipeline_test.go — reuse as-is
// s is the *store.Store; p.Store == s
```

No interface or mock needed; `passCommunities` calls `p.Store.FindEdgesByType`,
`p.Store.LoadCommunityCache`, `p.Store.SaveCommunityCache` on the live store.

### Error injection for cache methods (P-5, P-6)

`LoadCommunityCache` and `SaveCommunityCache` cannot be made to fail on a healthy
`OpenMemory()` store through normal means. Use a cancelled context:

- **P-5 (load error):** Pass a pre-cancelled `context.Context` to `LoadCommunityCache`
  directly in a unit test; verify it returns an error and that the caller falls back to
  `warmStart = nil`. For the integration test `TestPassCommunitiesLoadsErrorTolerance`,
  call `passCommunities` with a store that has the `community_cache` table dropped via
  `DROP TABLE community_cache` before the call — this forces a real SQL error on load
  without mocking.
- **P-6 (save error):** Same approach: drop `community_cache` after seeding the CALLS
  graph (so `FindEdgesByType` and `communityGraphHash` succeed) but before the save
  path executes. Since `passCommunities` does not accept a context argument, this is the
  only non-mock approach available.

---

## 4. Test Data: Specific Structures

### 4.1 Partition maps for C-8 (batch boundary)

The batch size is `cacheBatchSize = 249`. Test the four critical sizes:

| Test name | Row count | Batch pattern | What it tests |
|-----------|-----------|---------------|---------------|
| `TestCommunityCacheBatchBoundary249` | 249 | 1 × 249 | Exactly one full batch |
| `TestCommunityCacheBatchBoundary250` | 250 | 249 + 1 | First batch full, second has one row |
| `TestCommunityCacheBatchBoundary498` | 498 | 249 + 249 | Two full batches |
| `TestCommunityCacheBatchBoundary499` | 499 | 249 + 250 | Two batches, second is not full |

Generate the partition in test setup with a simple loop:

```go
partition := make(map[int64]int, n)
for i := 0; i < n; i++ {
    partition[int64(i+1)] = i % 7  // 7 distinct community values
}
```

Using `i % 7` (not a constant) ensures the round-trip test catches column ordering
errors that a uniform partition would mask.

Note: The `community_cache` table has `PRIMARY KEY (project, node_id)` — node_id values
must be distinct within a project. These tests do not insert corresponding `nodes` rows
because `node_id` is an unvalidated integer column (no FK on `node_id`), so no pre-seed
is required.

### 4.2 Graph structures for Louvain warm-start (L-3, L-4, L-5)

**L-3: perfect warm-start (already optimal)**

Use the two-clique topology from `twoClusterNodes()`. Run `louvain` once to get the
natural partition, then pass it as `warmStart` to a second `louvainWithWarmStart` call.
The second call should converge in 0 iterations (no moves in `louvainLocalMoving`).
Assert: community membership is structurally equivalent (nodes 1–5 same community,
nodes 6–10 same community, two distinct communities).

**L-4: warm-start with unknown nodes**

Start with 8 nodes and a warm-start that only covers nodes 1–6. Nodes 7 and 8 are
absent from `warmStart`. Assert that nodes 7 and 8 are not assigned community 0 (the
existing community 0 from the warm-start). The implementation assigns `nextComm`
(max+1) to unseen nodes; verify this by checking
`partition[7] != 0 && partition[8] != 0` when community 0 is occupied.

**L-5: community ID remapping**

Construct `warmStart = map[int64]int{1: 100, 2: 100, 3: 200}` where external IDs
100 and 200 are non-contiguous. Assert that after the call, nodes 1 and 2 are in the
same internal community and node 3 is in a different one. The specific internal
community IDs are irrelevant; only relative membership matters.

### 4.3 Graph hash collision demonstration (§2.3 of requirements)

Construct two graphs that differ in topology but happen to share the same `nodeSum` and
`edgeXOR`. The simplest construction: two graphs where one edge is flipped (A→B becomes
C→D such that `A XOR B == C XOR D`). Use node IDs where `1 XOR 4 == 2 XOR 3 == 5`.
Document this test with a comment explaining it is a known limitation, not a bug.

---

## 5. Execution Plan

### Tests requiring `-race`

All concurrency tests must pass under `go test -race`. Mark them with a comment
`// Requires: go test -race` at the top of the function body.

| Test | Reason |
|------|--------|
| `TestWatcherDebounceConcurrentSafety` (W-4) | 10 goroutines calling `triggerDebounced` simultaneously |
| `TestWatcherCachedProjectsRaceCondition` (W-13) | `refreshProjectCache` write vs `projectForPath` read via RWMutex |
| `TestCommunityCacheConcurrentProjects` (C-10 parallel variant) | `SaveCommunityCache` called in parallel subtests for different stores |
| `TestWatcherCancellation` (W-15) | goroutine leak detection |

Run the full concurrency suite in CI with:

```
go test -race -count=1 -timeout=60s \
    ./internal/watcher/... \
    ./internal/store/... \
    ./internal/pipeline/...
```

### Tests requiring `t.TempDir()`

Use `t.TempDir()` only when the test exercises real filesystem paths (fsnotify, snapshot
capture, read-only DB). All other store/pipeline tests use `OpenMemory()` exclusively.

| Test | Why TempDir |
|------|------------|
| `TestWatcherTriggersOnChange` | Real `.go` file for snapshot comparison |
| `TestWatcherNewFileTriggersIndex` | File creation must trigger fsnotify |
| `TestWatcherSkipsMissingRoot` | Registers a non-existent root — uses `newTestRouter` which calls `t.TempDir()` for the DB dir |
| `TestWatcherCancellation` | `newTestRouter` → `t.TempDir()` for DB dir |
| `TestWatchAllProjectsPrimesPollState` | Real project root directory for `addProjectDirs` |
| `TestAddProjectDirsNoOpWhenFswNil` | Real directory tree to confirm no walk/watch calls |
| `TestWatcherDebounceCoalescing` | Needs `newTestRouter` (temp DB dir) for project state |
| `TestWatcherDebounceConcurrentSafety` | Needs `newTestRouter` |
| `TestWatcherDebounceCancelledCtxSkipsIndex` | Needs `newTestRouter` |
| `TestOpenReadOnly*` (already exists) | Real DB file |

All community_cache, louvain, communities, and graph-hash tests use `OpenMemory()` —
`t.TempDir()` is absent from those files entirely.

### Timing-sensitive tests

Tests that sleep or wait on timers must use `t.Helper()` and document their maximum
expected wall time:

- Debounce tests sleep up to `debounceWindow + 50ms = 150ms` per subtest.
- `TestWatcherTriggersOnChange` with fsnotify waits up to `200ms` after a write.
- `TestWatcherCancellation` has a 3 s hard deadline (matches existing test).

Set per-test timeouts where appropriate:

```go
ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
defer cancel()
```

---

## 6. Naming Conventions

Match the existing `perf_*_test.go` style exactly:

### File names

- `community_cache_test.go` — store-level cache CRUD (not `perf_community_cache_test.go`
  because these are correctness tests, not performance benchmarks).
- `louvain_test.go` — warm-start algorithm tests (additive to `perf_louvain_test.go`
  which tests `louvainLocalMoving` directly; new file tests `louvainWithWarmStart`).
- `communities_test.go` — pipeline integration (matches the source file `communities.go`).

### Function names

Follow the pattern `Test<Subject><Scenario>` with no underscores except to separate
behaviour from condition, matching the existing tests in `watcher_test.go`:

| Category | Pattern | Example |
|----------|---------|---------|
| Table-schema tests | `TestCommunityCache<Schema aspect>` | `TestCommunityCacheTableExists` |
| Round-trip / CRUD | `TestCommunityCache<Operation>` | `TestCommunityCacheRoundTrip` |
| Boundary / edge | `TestCommunityCache<Subject><Condition>` | `TestCommunityCacheBatchBoundary249` |
| Algorithm | `TestLouvainWithWarmStart<Property>` | `TestLouvainWithWarmStartConvergesFast` |
| Hash | `TestCommunityGraphHash<Property>` | `TestCommunityGraphHashDeterministic` |
| Integration | `TestPassCommunities<Scenario>` | `TestPassCommunitiesCacheMissThenHit` |
| Watcher unit | `TestWatcher<Feature><Condition>` | `TestWatcherDebounceCoalescing` |
| Watcher projection | `TestProjectForPath<Condition>` | `TestProjectForPathNoFalsePositive` |

### Subtest names

Use `t.Run` for table-driven tests (e.g., batch boundaries). Subtest names should be
lower-case with underscores matching the condition, e.g.:

```go
t.Run("249_rows", func(t *testing.T) { ... })
t.Run("250_rows", func(t *testing.T) { ... })
t.Run("498_rows", func(t *testing.T) { ... })
t.Run("499_rows", func(t *testing.T) { ... })
```

---

## 7. Per-File Detailed Outline

### `internal/store/community_cache_test.go`

Package declaration: `package store`

Imports: `context`, `testing` (no external dependencies)

```
TestCommunityCacheTableExists        — C-1, C-2
    Query pragma_table_info('community_cache') for columns.
    Query pragma_index_list('community_cache') for idx_community_cache_project.

TestCommunityCacheRoundTrip          — C-3
    openMemoryWithProject → SaveCommunityCache → LoadCommunityCache.
    Assert returned map is identical to input.

TestCommunityCacheHashMiss           — C-4
    Save with hash "abc", Load with hash "xyz" → nil, nil.

TestCommunityCacheEmptyDB            — C-5
    Load on a project with no rows → nil, nil.

TestCommunityCacheReplaces           — C-6
    Save partition A (hash "h1"), Save partition B (hash "h2").
    Load with "h2" → partition B only.
    Load with "h1" → nil (old rows deleted).

TestCommunityCacheSaveAtomicity      — C-7
    Use context.WithCancel, cancel before BeginTx fires.
    Pass cancelled ctx to SaveCommunityCache.
    Assert error returned; Load returns nil (no partial rows).

TestCommunityCacheBatchBoundary      — C-8  (table-driven, 4 subtests)
    249 / 250 / 498 / 499 rows, partition i → i%7.
    Save then Load; compare maps with reflect.DeepEqual.

TestCommunityCacheEmptyPartition     — C-9
    Save empty map{} → no error.
    Load → nil, nil (zero rows stored).

TestCommunityCacheProjectIsolation   — C-10
    Two calls to openMemoryWithProject with different project names.
    Save partition for project A; Load for project B → nil.
    (Two separate *Store instances, one per project, to mirror production.)

TestCommunityCacheConcurrentProjects — C-10 race variant
    t.Run("projA", ...) + t.Run("projB", ...) in parallel.
    Each subtest: separate store, separate project, concurrent Save + Load.
    Run with -race.
```

Helpers defined in this file: `openMemoryWithProject`, `makePartition`.

### `internal/store/louvain_test.go`

Package declaration: `package store`

Imports: `testing` only

Reuses: `addTestEdge` from `perf_louvain_test.go` (same package, same file set)

```
TestLouvainWrapperMatchesWarmStartNil    — L-1
    louvain(nodes, edges) vs louvainWithWarmStart(nodes, edges, nil).
    Assert structural equivalence: same number of communities,
    same community membership per node (community IDs may differ).

TestLouvainWithWarmStartNilSingletons   — L-2
    louvainWithWarmStart(nodes, edges, nil) with 6 nodes.
    After internal init loop: each node[i] has community[i] == i.
    Test this by passing a graph with no edges (totalWeight == 0 path)
    so community[] is never modified after init; verify all entries distinct.

TestLouvainWithWarmStartConvergesFast   — L-3
    twoClusterNodes() → first louvain() call → natural partition.
    Second call: louvainWithWarmStart(nodes, edges, naturalPartition).
    Structural result must match (same two-cluster membership).
    Verify convergence speed indirectly: the second call must return the
    same partition as the first (not tested by iteration count — that is
    internal state; test observable output only).

TestLouvainWithWarmStartNewNodes       — L-4
    8 nodes; warmStart covers only nodes 1–6 with community values 0 and 1.
    Assert partition[7] != 0 && partition[8] != 0 (not assigned to
    existing community 0, per the nextComm = max+1 assignment logic).

TestLouvainWithWarmStartRemapping      — L-5
    warmStart = {1: 100, 2: 100, 3: 200}.
    3-node graph with no edges (so community init is the only assignment).
    Assert partition[1] == partition[2] && partition[1] != partition[3].

TestLouvainWithWarmStartEmpty          — L-6
    louvainWithWarmStart(nil, nil, nil) → map[int64]int{} (empty, no panic).
    louvainWithWarmStart([]int64{}, nil, nil) → same.

TestLouvainWithWarmStartSingleNode     — L-7
    louvainWithWarmStart([]int64{42}, nil, nil) → map with 1 entry, key 42.

TestLouvainWithWarmStartNoEdges        — L-8
    5 nodes, 0 edges.
    Every node must be its own community (all 5 community values distinct).

TestLouvainTwoClusterTopology          — L-10
    twoClusterNodes() → louvainWithWarmStart(nodes, edges, nil).
    Nodes 1–5 same community; nodes 6–10 same community; two distinct.
    This is the warm-start variant of TestLouvainEarlyExitOnConvergence.
```

Note: L-9 (`TestLouvainEarlyExit`) is already covered by
`TestLouvainEarlyExitOnConvergence` in `perf_louvain_test.go`. Do not duplicate it.

### `internal/pipeline/communities_test.go`

Package declaration: `package pipeline`

Imports: `context`, `fmt`, `testing`,
`github.com/DeusData/codebase-memory-mcp/internal/store`

Reuses: `newTestPipeline` from `perf_pipeline_test.go` (same package)

```
TestCommunityGraphHashDeterministic  — H-1
    Two identical calls → same string.

TestCommunityGraphHashNodeChange     — H-2
    Add one node to allNodes map → hash changes.

TestCommunityGraphHashEdgeChange     — H-3
    Add one *store.Edge to callEdges → hash changes.

TestCommunityGraphHashTopologySwap   — H-4
    Replace node A (id=1) with node B (id=2), same total count.
    Hash must change (nodeSum changes: was ...+1, now ...+2).

TestCommunityGraphHashEdgeDirectionInvariance  — H-5
    Add edge (src=3, dst=7) → hash differs from base (edge count term changes).
    Add edge (src=7, dst=3) instead → same hash as the (3,7) case because
    XOR is symmetric: 3^7 == 7^3. Document this as expected behaviour,
    not a collision — same edge in opposite orientation is the same edge.

TestCommunityGraphHashEmpty          — H-6
    communityGraphHash(nil, nil) → deterministic non-empty string.
    communityGraphHash(map[int64]bool{}, nil) → same string.
    Assert both equal "0:0:0:0".

TestPassCommunitiesCacheMissThenHit  — P-1, P-2
    newTestPipeline → seedCallGraph (10 nodes, twoCluster edges).
    First passCommunities: Load returns nil, communities written, Save called.
    Verify: Community nodes exist in store, MEMBER_OF edges exist.
    Second passCommunities (same graph, same store): Load returns partition.
    Verify: Community nodes still exist, MEMBER_OF edges still exist.
    (Both calls succeed without error; cache hit verified via node counts
    being stable — no duplicate Community nodes.)

TestPassCommunitiesCacheInvalidatedOnTopologyChange  — P-3
    After first passCommunities, add a new CALLS edge → graph hash changes.
    Second passCommunities: Load returns nil (miss), Louvain runs cold.
    Verify: Community nodes present after second call.

TestPassCommunitiesSkipsWhenNoCallEdges  — P-4
    newTestPipeline with no edges inserted.
    passCommunities() → returns immediately.
    Assert: no Community nodes in store, no MEMBER_OF edges.

TestPassCommunitiesLoadErrorTolerance  — P-5
    Drop community_cache table after seeding.
    passCommunities() must still complete and write Community nodes.
    (Load will error; warm-start falls back to nil; Save will also error
    but communities are still stored.)

TestPassCommunitiesSaveErrorTolerance  — P-6
    Same as P-5 (drop table). The save will fail, but the test verifies
    Community nodes and MEMBER_OF edges are written regardless.
    Assert communityCount > 0 by querying the nodes table directly.
```

---

## 8. Complete Test Scenario Cross-Reference

The table below maps every criterion ID from the requirements to its test function,
file, and execution flags.

| Criterion | Test Function | File | Unit/Integration | -race | TempDir |
|-----------|--------------|------|-----------------|-------|---------|
| W-1 | `TestWatcherFSNotifyUnavailableFallback` | `watcher_test.go` | Unit | No | No |
| W-1 | `TestAddProjectDirsNoOpWhenFswNil` | `watcher_test.go` | Unit | No | Yes |
| W-2 | `TestWatcherNewFileTriggersIndex` | `watcher_test.go` | Integration | No | Yes |
| W-3 | `TestWatcherDebounceCoalescing` | `watcher_test.go` | Unit | No | No |
| W-3 | `TestWatcherDebounceTimerReset` | `watcher_test.go` | Unit | No | No |
| W-4 | `TestWatcherDebounceConcurrentSafety` | `watcher_test.go` | Unit | **Yes** | No |
| W-5 | `TestWatcherNewSubdirWatched` | `watcher_test.go` | Integration | No | Yes |
| W-6 | `TestCaptureSnapshot` (exists) | `watcher_test.go` | Unit | No | Yes |
| W-7 | `TestWatcherTriggersOnChange` (exists) | `watcher_test.go` | Integration | No | Yes |
| W-8 | `TestSnapshotsEqual` (exists) | `watcher_test.go` | Unit | No | No |
| W-9 | `TestWatcherTriggersOnChange` (nextPoll reset) | `watcher_test.go` | Integration | No | Yes |
| W-10 | `TestWatcherSkipsMissingRoot` (exists) | `watcher_test.go` | Integration | No | Yes |
| W-11 | `TestProjectForPathPrefixMatch` | `watcher_test.go` | Unit | No | No |
| W-11 | `TestProjectForPathExactRoot` | `watcher_test.go` | Unit | No | No |
| W-12 | `TestProjectForPathNoFalsePositive` | `watcher_test.go` | Unit | No | No |
| W-13 | `TestCachedProjectsUsedByHandleFSEvent` | `watcher_test.go` | Unit | **Yes** | No |
| W-14 | `TestWatchAllProjectsPrimesPollState` | `watcher_test.go` | Integration | No | Yes |
| W-15 | `TestWatcherCancellation` (exists) | `watcher_test.go` | Integration | **Yes** | Yes |
| W-16 | `TestWatcherDebounceCancelledCtxSkipsIndex` | `watcher_test.go` | Unit | No | No |
| W-17 | `TestWatcherIndexFnErrorTolerance` | `watcher_test.go` | Integration | No | Yes |
| C-1 | `TestCommunityCacheTableExists` | `community_cache_test.go` | Unit | No | No |
| C-2 | `TestCommunityCacheTableExists` | `community_cache_test.go` | Unit | No | No |
| C-3 | `TestCommunityCacheRoundTrip` | `community_cache_test.go` | Unit | No | No |
| C-4 | `TestCommunityCacheHashMiss` | `community_cache_test.go` | Unit | No | No |
| C-5 | `TestCommunityCacheEmptyDB` | `community_cache_test.go` | Unit | No | No |
| C-6 | `TestCommunityCacheReplaces` | `community_cache_test.go` | Unit | No | No |
| C-7 | `TestCommunityCacheSaveAtomicity` | `community_cache_test.go` | Unit | No | No |
| C-8 | `TestCommunityCacheBatchBoundary` (4 subtests) | `community_cache_test.go` | Unit | No | No |
| C-9 | `TestCommunityCacheEmptyPartition` | `community_cache_test.go` | Unit | No | No |
| C-10 | `TestCommunityCacheProjectIsolation` | `community_cache_test.go` | Unit | No | No |
| C-10 | `TestCommunityCacheConcurrentProjects` | `community_cache_test.go` | Unit | **Yes** | No |
| C-11 | `TestDropCreateIndexesIncludesCacheIndex` | `perf_store_test.go` | Unit | No | No |
| L-1 | `TestLouvainWrapperMatchesWarmStartNil` | `louvain_test.go` | Unit | No | No |
| L-2 | `TestLouvainWithWarmStartNilSingletons` | `louvain_test.go` | Unit | No | No |
| L-3 | `TestLouvainWithWarmStartConvergesFast` | `louvain_test.go` | Unit | No | No |
| L-4 | `TestLouvainWithWarmStartNewNodes` | `louvain_test.go` | Unit | No | No |
| L-5 | `TestLouvainWithWarmStartRemapping` | `louvain_test.go` | Unit | No | No |
| L-6 | `TestLouvainWithWarmStartEmpty` | `louvain_test.go` | Unit | No | No |
| L-7 | `TestLouvainWithWarmStartSingleNode` | `louvain_test.go` | Unit | No | No |
| L-8 | `TestLouvainWithWarmStartNoEdges` | `louvain_test.go` | Unit | No | No |
| L-9 | `TestLouvainEarlyExitOnConvergence` (exists) | `perf_louvain_test.go` | Unit | No | No |
| L-10 | `TestLouvainTwoClusterTopology` | `louvain_test.go` | Unit | No | No |
| H-1 | `TestCommunityGraphHashDeterministic` | `communities_test.go` | Unit | No | No |
| H-2 | `TestCommunityGraphHashNodeChange` | `communities_test.go` | Unit | No | No |
| H-3 | `TestCommunityGraphHashEdgeChange` | `communities_test.go` | Unit | No | No |
| H-4 | `TestCommunityGraphHashTopologySwap` | `communities_test.go` | Unit | No | No |
| H-5 | `TestCommunityGraphHashEdgeDirectionInvariance` | `communities_test.go` | Unit | No | No |
| H-6 | `TestCommunityGraphHashEmpty` | `communities_test.go` | Unit | No | No |
| P-1 | `TestPassCommunitiesCacheMissThenHit` | `communities_test.go` | Integration | No | No |
| P-2 | `TestPassCommunitiesCacheMissThenHit` | `communities_test.go` | Integration | No | No |
| P-3 | `TestPassCommunitiesCacheInvalidatedOnTopologyChange` | `communities_test.go` | Integration | No | No |
| P-4 | `TestPassCommunitiesSkipsWhenNoCallEdges` | `communities_test.go` | Integration | No | No |
| P-5 | `TestPassCommunitiesLoadErrorTolerance` | `communities_test.go` | Integration | No | No |
| P-6 | `TestPassCommunitiesSaveErrorTolerance` | `communities_test.go` | Integration | No | No |

---

## 9. Key Design Decisions and Rationale

**Why `package store` / `package pipeline` (not `_test` suffix packages)**

`louvainWithWarmStart`, `communityGraphHash`, `louvainCommunities`, `cacheBatchSize`,
`triggerDebounced`, `projectForPath`, `addProjectDirs`, `debounceMap`, and
`cachedProjects` are all unexported. White-box access requires same-package tests.
This matches every existing test file in the codebase (`package store`, `package watcher`,
`package pipeline`).

**Why not mock `LoadCommunityCache` / `SaveCommunityCache` for P-5 and P-6**

The requirements explicitly forbid mock frameworks. `OpenMemory()` provides a real SQL
engine. Dropping the `community_cache` table forces a real SQL error at the driver level.
This tests the same code path that would fire if the schema migration failed in
production, making it a higher-fidelity test than a mock.

**Why `reflect.DeepEqual` for partition round-trips (C-3, C-8)**

`map[int64]int` comparison with `reflect.DeepEqual` is safe and idiomatic in Go test
code. It reports the full diff on failure because `t.Errorf("%v vs %v", got, want)`
renders the maps. No third-party assertion library is introduced.

**Why `atomic.Int32` for `indexCount` (all watcher tests)**

Matches the existing pattern in `watcher_test.go` exactly. `atomic.Int32` is safe under
`-race` without any additional synchronization, and it avoids the channel-based
synchronization overhead for simple counting.

**Why a single `twoClusterNodes()` helper (not inline per test)**

The two-clique topology with a bridge edge is used in at least five distinct tests
(L-3, L-10, P-1, P-2, P-3). Defining it once prevents the tests from diverging silently.
It mirrors the approach used by `setupArchTestStore` in `architecture_test.go`.

**Why `snapshotsEqualNilMaps` is a separate test, not a subtest**

The requirements explicitly call out `snapshotsEqual(nil, nil)` as an edge case that
must be confirmed (not assumed). A separate named function makes it trivially findable
in test output and in the scenario matrix.
