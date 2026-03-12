# Week 2 Performance Changes — Test Requirements

Scope: `internal/watcher/watcher.go`, `internal/store/community_cache.go`,
`internal/store/louvain.go`, `internal/pipeline/communities.go`.

---

## 1. Acceptance Criteria

### 1.1 fsnotify Watcher

| ID | Criterion | Pass condition | Fail condition |
|----|-----------|----------------|----------------|
| W-1 | fsnotify unavailable fallback | When `fsnotify.NewWatcher()` fails, `Run` logs a warning and continues with poll-only mode; `w.fsw` stays nil | `Run` panics or returns early |
| W-2 | Event-driven detection latency | A file write inside a watched directory produces an `indexFn` call within 200 ms (debounce window + headroom) | Call arrives after 200 ms or not at all |
| W-3 | Debounce coalescing | N rapid writes to the same project fire `indexFn` exactly once after a 100 ms quiet window | More than one call, or zero calls |
| W-4 | Debounce timer safety | Calling `triggerDebounced` concurrently from 10 goroutines for the same project never panics or double-fires | Data race detected by `-race`, or >1 `indexFn` call |
| W-5 | New directory auto-watch | Creating a subdirectory inside a watched root causes events in that subdirectory to be detected without waiting for fallback poll | Sub-directory events missed until next poll tick |
| W-6 | Fallback poll baseline | First `pollAll` call captures a snapshot and does NOT call `indexFn` | `indexFn` called on first poll |
| W-7 | Fallback poll detects change | Second `pollAll` after a file mtime/size change calls `indexFn` exactly once | Zero or >1 calls |
| W-8 | Fallback poll skips unchanged | Second `pollAll` with no changes does NOT call `indexFn` | `indexFn` called |
| W-9 | nextPoll gate | `pollAll` skips a project whose `state.nextPoll` is in the future | Poll runs early, consuming cycles |
| W-10 | Missing root path | Project whose `RootPath` does not exist: `pollProject` logs a warning and does not call `indexFn` | `indexFn` called or panic |
| W-11 | `projectForPath` prefix match | Returns the correct project when path is an exact root, a direct child, or a deeply nested child | Wrong project returned or miss |
| W-12 | `projectForPath` no false positives | `/tmp/proj` does not match `/tmp/proj-extra/file.go` | Wrong project returned |
| W-13 | `cachedProjects` hot-path | `handleFSEvent` reads project list from the in-memory cache (`cachedProjects`), not from the DB | DB queried on every fsnotify event |
| W-14 | `watchAllProjects` primes poll state | After `watchAllProjects`, `w.projects` contains an entry for every project returned by `ListProjects`, preventing `pollAll` from re-adding fsnotify watches | Duplicate `fsw.Add` calls logged |
| W-15 | ctx cancellation stops Run | Cancelling the context causes `Run` to return within 3 s | `Run` blocks indefinitely |
| W-16 | ctx cancellation skips pending index | A debounce timer that fires after ctx cancellation does NOT call `indexFn` | `indexFn` called post-cancellation |
| W-17 | indexFn error tolerance | `indexFn` returning an error does not crash the watcher or stop subsequent events from being processed | Watcher panics or stops |

### 1.2 community_cache Store Methods

| ID | Criterion | Pass condition | Fail condition |
|----|-----------|----------------|----------------|
| C-1 | Table created by `OpenMemory` | `community_cache` table exists with columns `(project, node_id, community, graph_hash)` and PRIMARY KEY `(project, node_id)` after `OpenMemory()` | Table missing or schema differs |
| C-2 | Index created | `idx_community_cache_project` exists after `OpenMemory()` | Index absent |
| C-3 | `SaveCommunityCache` round-trip | Save then Load returns identical partition map for the same project and graphHash | Any entry missing, extra, or wrong community value |
| C-4 | Load returns nil on hash miss | `LoadCommunityCache` with a different `graphHash` returns `(nil, nil)` | Returns stale data or an error |
| C-5 | Load returns nil on empty DB | `LoadCommunityCache` on a project with no cached rows returns `(nil, nil)` | Returns error or non-nil map |
| C-6 | Save replaces old data | Second `SaveCommunityCache` with the same project but a new `graphHash` and different partition leaves only the new rows | Old rows survive alongside new rows |
| C-7 | Save is atomic | A cancelled context mid-save leaves no partial rows (transaction rolls back) | Partial rows present after cancelled save |
| C-8 | Batch boundary at 249 rows | A partition of exactly 249, 250, and 498 rows saves and loads correctly (covers boundary between batch sizes) | Rows missing at batch seam |
| C-9 | Empty partition | `SaveCommunityCache` with an empty map succeeds (only DELETE runs, zero INSERTs); subsequent `LoadCommunityCache` returns nil | Error on save, or non-nil empty map on load |
| C-10 | Multi-project isolation | Saving a partition for project A does not affect project B's cache | Cross-project contamination |
| C-11 | `DropUserIndexes` / `CreateUserIndexes` include cache index | Both functions include `idx_community_cache_project` | Index missing from one or both lists |

### 1.3 Louvain Warm-Start

| ID | Criterion | Pass condition | Fail condition |
|----|-----------|----------------|----------------|
| L-1 | `louvain` is a thin wrapper | `louvain(nodes, edges)` produces the same structural result as `louvainWithWarmStart(nodes, edges, nil)` (same communities, not necessarily same IDs) | Results structurally diverge |
| L-2 | Warm-start nil falls back to singletons | `louvainWithWarmStart` with `warmStart=nil` initialises every node to its own community (index `i`) | Nodes share community IDs before any iteration |
| L-3 | Warm-start seeds correctly | With a perfect warm-start partition (already optimal), the algorithm converges in 0-1 iterations and returns the same community structure | Community structure changes or iterations exceed 3 |
| L-4 | Warm-start with unknown nodes | Nodes present in the graph but absent from `warmStart` are assigned fresh singleton community IDs, not community 0 | New node assigned community 0, conflicting with existing data |
| L-5 | Warm-start community ID remapping | External community IDs are remapped to compact internal indices; two nodes with the same external ID end up in the same internal community | Nodes incorrectly separated or merged |
| L-6 | Empty nodes | `louvainWithWarmStart(nil, nil, nil)` returns an empty map, not a panic | Panic or non-empty map |
| L-7 | Single node no edges | Returns a map with one entry; community ID is arbitrary but present | Empty map or panic |
| L-8 | No edges (disconnected graph) | Every node gets its own community (each node is its own community) | Any two nodes share a community |
| L-9 | Termination on convergence | `!improved` or `changed/n < 0.001` triggers early exit before iteration 15 | Loop always runs all 15 iterations on a stable graph |
| L-10 | Two-cluster topology | Two fully-connected cliques of 5 nodes joined by one bridge edge produce two distinct communities | All nodes in one community, or more than two communities |

### 1.4 `communityGraphHash`

| ID | Criterion | Pass condition | Fail condition |
|----|-----------|----------------|----------------|
| H-1 | Same graph produces same hash | Two calls with identical `allNodes` and `callEdges` return the same string | Hashes differ |
| H-2 | Added node changes hash | Adding one node to `allNodes` changes the hash (nodeSum changes) | Hash identical |
| H-3 | Added edge changes hash | Adding one edge changes the hash (edgeXOR changes) | Hash identical |
| H-4 | Topology swap with same counts | Deleting node A and adding node B with different ID (same count) changes hash | False cache hit |
| H-5 | Edge direction invariance | The hash must differ when a new edge is added regardless of direction ordering in the slice — the XOR of (src XOR dst) is already symmetric, but adding/removing an edge changes the count term | Hash unchanged when edge count changes |
| H-6 | Empty graph | `communityGraphHash(nil/empty, nil/empty)` returns a deterministic, non-empty string | Panic or empty string |

### 1.5 `passCommunities` Integration

| ID | Criterion | Pass condition | Fail condition |
|----|-----------|----------------|----------------|
| P-1 | Cache miss path | First `passCommunities` call (empty cache): `LoadCommunityCache` returns nil, Louvain runs from scratch, `SaveCommunityCache` is called with the resulting partition | Save not called, or called with empty partition |
| P-2 | Cache hit path | Second `passCommunities` call with identical graph: `LoadCommunityCache` returns the prior partition, warm-start is used, `SaveCommunityCache` is called again to refresh | Load returns nil on identical hash, or Save not called |
| P-3 | Cache invalidation on topology change | After adding a new CALLS edge, the hash changes, `LoadCommunityCache` returns nil (miss), and Louvain runs cold | Stale warm-start applied to wrong topology |
| P-4 | No CALLS edges skips community pass | `passCommunities` returns immediately when `FindEdgesByType` returns empty; no save is attempted | Save called with empty data, or panic |
| P-5 | LoadCommunityCache error tolerance | If `LoadCommunityCache` returns an error, `warmStart` is set to nil and Louvain still runs | Entire pass aborted on cache error |
| P-6 | SaveCommunityCache error tolerance | If `SaveCommunityCache` returns an error, only a warning is logged; community nodes and MEMBER_OF edges are still stored | Pass aborted or Community nodes not written |

---

## 2. Edge Cases

### 2.1 Nil / Empty Inputs

- `snapshotsEqual(nil, nil)` — both maps nil; must return true without panic. (The current implementation uses `len()` on nil maps which is safe in Go, but the test should confirm.)
- `snapshotsEqual(map[string]fileSnapshot{}, map[string]fileSnapshot{})` — already tested; keep.
- `louvainWithWarmStart([]int64{}, nil, nil)` — zero-length slice, not nil; must return `map[int64]int{}`.
- `SaveCommunityCache` with `partition = map[int64]int{}` (zero rows) — must succeed and leave no rows.
- `communityGraphHash` with `allNodes = map[int64]bool{}` and `callEdges = nil` — must not panic; returns `"0:0:0:0"`.

### 2.2 Concurrent Access

- `triggerDebounced` called concurrently from multiple goroutines for the same project: protected by `debounceMu`; run with `-race`.
- `refreshProjectCache` write under `cachedProjectsMu.Lock` while `projectForPath` reads under `cachedProjectsMu.RLock`: run with `-race`.
- `pollMu` guards `w.projects`; `pollAll` and `watchAllProjects` both take this lock: verify no deadlock with concurrent `Run` and `pollAll` calls.
- `SaveCommunityCache` called concurrently for different projects (different `Store` instances — one per project in the router): no shared state, safe by construction; confirm with a parallel subtest.

### 2.3 Hash Collisions

- Two graphs where `nodeSum` and `edgeXOR` collide but topology differs: document this as a known limitation (XOR is not collision-resistant; a test should demonstrate a constructed example where the hash collides and verify the system still produces correct output because the Louvain algorithm is deterministic from the warm-start).
- The hash format `"%d:%d:%x:%x"` encodes four independent fields; a collision requires all four to match simultaneously. Add a test with a crafted collision that only the count fields match (`"5:3:..."`) to show that the nodeSum/edgeXOR fields provide additional discrimination.

### 2.4 Context Cancellation

- `SaveCommunityCache` with an already-cancelled context: `db.BeginTx` returns an error immediately; verify rollback path and that no rows are written.
- `pollAll` with a cancelled context passed in: the `pollProject` → `indexFn` call should still be guarded by the debounce ctx-done check.
- `triggerDebounced` fires after `ctx.Done()`: the `select { case <-ctx.Done(): ... default: }` guard must prevent `indexFn` from being called; verify with a test that cancels ctx then lets the timer fire.

### 2.5 fsnotify Unavailable

- Simulate `fsnotify.NewWatcher()` failure by testing on a system where inotify instances are exhausted — not feasible in CI. Instead: test that `New` + `Run` behaves correctly when `w.fsw == nil` (poll-only mode), by calling `addProjectDirs` directly and confirming it is a no-op when `w.fsw == nil`.
- Confirm that `handleFSEvent` is never called when `w.fsw == nil` (the goroutine `runFSNotify` is not started).

### 2.6 Network / Stale Mounts

- The 30 s fallback poll is the safety net; unit tests cannot exercise this directly. Instead, verify that `runFallbackPoll` ticks at `fallbackInterval` by confirming the ticker period equals the constant (a compile-time constant check, or a test that reads `fallbackInterval`).

### 2.7 Batch Boundary (cacheBatchSize = 249)

- Exactly 249 rows: one batch, one INSERT.
- 250 rows: two batches (249 + 1).
- 498 rows: two batches of 249.
- 499 rows: two batches (249 + 250).
- These must all round-trip correctly through `SaveCommunityCache` → `LoadCommunityCache`.

---

## 3. Test Scenario Matrix

| Test Name | File | Category | Covers |
|-----------|------|----------|--------|
| `TestSnapshotsEqual` | `watcher_test.go` | Unit | W-7, W-8 |
| `TestSnapshotsEqualNilMaps` | `watcher_test.go` | Unit | Edge: nil inputs |
| `TestPollInterval` | `watcher_test.go` | Unit | Retained compatibility test |
| `TestCaptureSnapshot` | `watcher_test.go` | Unit | W-6 (snapshot baseline) |
| `TestCaptureSnapshotDetectsChanges` | `watcher_test.go` | Unit | W-7 |
| `TestWatcherTriggersOnChange` | `watcher_test.go` | Integration | W-7, W-9 |
| `TestWatcherSkipsMissingRoot` | `watcher_test.go` | Integration | W-10 |
| `TestWatcherNewFileTriggersIndex` | `watcher_test.go` | Integration | W-7 |
| `TestWatcherCancellation` | `watcher_test.go` | Integration | W-15 |
| `TestWatcherDebounceCoalescing` | `watcher_test.go` | Unit | W-3 |
| `TestWatcherDebounceConcurrentSafety` | `watcher_test.go` | Unit (race) | W-4 |
| `TestWatcherDebounceCancelledCtxSkipsIndex` | `watcher_test.go` | Unit | W-16 |
| `TestWatcherDebounceTimerReset` | `watcher_test.go` | Unit | W-3, W-4 (Stop before Reset) |
| `TestProjectForPathPrefixMatch` | `watcher_test.go` | Unit | W-11 |
| `TestProjectForPathNoFalsePositive` | `watcher_test.go` | Unit | W-12 |
| `TestProjectForPathExactRoot` | `watcher_test.go` | Unit | W-11 |
| `TestCachedProjectsUsedByHandleFSEvent` | `watcher_test.go` | Unit | W-13 |
| `TestWatchAllProjectsPrimesPollState` | `watcher_test.go` | Integration | W-14 |
| `TestWatcherFSNotifyUnavailableFallback` | `watcher_test.go` | Unit | W-1 |
| `TestAddProjectDirsNoOpWhenFswNil` | `watcher_test.go` | Unit | W-1, W-5 |
| `TestWatcherIndexFnErrorTolerance` | `watcher_test.go` | Integration | W-17 |
| `TestCommunityCacheTableExists` | `store_test.go` or `community_cache_test.go` | Unit | C-1, C-2 |
| `TestCommunityCacheRoundTrip` | `community_cache_test.go` | Unit | C-3 |
| `TestCommunityCacheHashMiss` | `community_cache_test.go` | Unit | C-4 |
| `TestCommunityCacheEmptyDB` | `community_cache_test.go` | Unit | C-5 |
| `TestCommunityCacheReplaces` | `community_cache_test.go` | Unit | C-6 |
| `TestCommunityCacheSaveAtomicity` | `community_cache_test.go` | Unit | C-7 |
| `TestCommunityCacheBatchBoundary249` | `community_cache_test.go` | Unit | C-8 |
| `TestCommunityCacheBatchBoundary250` | `community_cache_test.go` | Unit | C-8 |
| `TestCommunityCacheBatchBoundary498` | `community_cache_test.go` | Unit | C-8 |
| `TestCommunityCacheEmptyPartition` | `community_cache_test.go` | Unit | C-9 |
| `TestCommunityCacheProjectIsolation` | `community_cache_test.go` | Unit | C-10 |
| `TestDropCreateIndexesIncludesCacheIndex` | `bulk_test.go` | Unit | C-11 |
| `TestLouvainWrapperMatchesWarmStartNil` | `louvain_test.go` | Unit | L-1 |
| `TestLouvainWithWarmStartNilSingletons` | `louvain_test.go` | Unit | L-2 |
| `TestLouvainWithWarmStartConvergesFast` | `louvain_test.go` | Unit | L-3 |
| `TestLouvainWithWarmStartNewNodes` | `louvain_test.go` | Unit | L-4 |
| `TestLouvainWithWarmStartRemapping` | `louvain_test.go` | Unit | L-5 |
| `TestLouvainWithWarmStartEmpty` | `louvain_test.go` | Unit | L-6 |
| `TestLouvainWithWarmStartSingleNode` | `louvain_test.go` | Unit | L-7 |
| `TestLouvainWithWarmStartNoEdges` | `louvain_test.go` | Unit | L-8 |
| `TestLouvainEarlyExit` | `perf_louvain_test.go` (existing) | Unit | L-9 |
| `TestLouvainTwoClusterTopology` | `louvain_test.go` | Unit | L-10 |
| `TestCommunityGraphHashDeterministic` | `communities_test.go` | Unit | H-1 |
| `TestCommunityGraphHashNodeChange` | `communities_test.go` | Unit | H-2 |
| `TestCommunityGraphHashEdgeChange` | `communities_test.go` | Unit | H-3 |
| `TestCommunityGraphHashTopologySwap` | `communities_test.go` | Unit | H-4 |
| `TestCommunityGraphHashEmpty` | `communities_test.go` | Unit | H-6 |
| `TestPassCommunitiesCacheMissThenHit` | `communities_test.go` | Integration | P-1, P-2 |
| `TestPassCommunitiesCacheInvalidatedOnTopologyChange` | `communities_test.go` | Integration | P-3 |
| `TestPassCommunitiesSkipsWhenNoCallEdges` | `communities_test.go` | Integration | P-4 |
| `TestPassCommunitiesLoadsErrorTolerance` | `communities_test.go` | Integration | P-5 |
| `TestPassCommunitiesSaveErrorTolerance` | `communities_test.go` | Integration | P-6 |

---

## 4. Unit vs Integration Classification

**Unit tests** exercise a single function or method in isolation, with no real filesystem, no real DB (use `OpenMemory()`), and no goroutines beyond what the function itself creates.

**Integration tests** exercise multiple components together, may require a real temporary directory (via `t.TempDir()`), and verify end-to-end behaviour through the public API of the package.

### Unit

- All `snapshotsEqual`, `captureSnapshot`, `projectForPath`, `triggerDebounced`, `addProjectDirs` tests.
- All `louvainWithWarmStart`, `louvain`, `louvainLocalMoving` tests.
- All `communityGraphHash` tests.
- All `SaveCommunityCache` / `LoadCommunityCache` tests using `OpenMemory()`.
- `TestCommunityCacheTableExists`, `TestDropCreateIndexesIncludesCacheIndex`.

### Integration

- `TestWatcherTriggersOnChange`, `TestWatcherCancellation`, `TestWatcherSkipsMissingRoot`, `TestWatcherNewFileTriggersIndex`, `TestWatchAllProjectsPrimesPollState` — require `t.TempDir()` + real files + `store.NewRouterWithDir`.
- `TestPassCommunitiesCacheMissThenHit`, `TestPassCommunitiesCacheInvalidatedOnTopologyChange` — exercise the full `passCommunities` call path: `FindEdgesByType` → `communityGraphHash` → `LoadCommunityCache` → `louvainCommunities` → `SaveCommunityCache` → `storeCommunities`, against a real `OpenMemory()` store with seeded nodes and edges.
- `TestPassCommunitiesSkipsWhenNoCallEdges`, error-tolerance tests — also integration (require a seeded store).

---

## 5. External Dependencies — Real vs Mock

### Do not mock

| Dependency | Rationale |
|------------|-----------|
| SQLite (`mattn/go-sqlite3`) | `OpenMemory()` is fast (<1 ms), deterministic, and tests real SQL execution including transactions, batch INSERTs, and PRIMARY KEY constraints. Mocking would test nothing meaningful. |
| `fsnotify` | The library is included in the module. Use real temporary directories with `t.TempDir()` and real file writes. This exercises the actual inotify/kqueue kernel path, which is what the feature guarantees. |
| `discover.Discover` | Called by `captureSnapshot`; exercises real filesystem traversal. Use `t.TempDir()` with seeded `.go` files. |
| `time.AfterFunc` / `time.Timer` | The debounce logic must be tested with real timers (short durations: 100–200 ms). Mocking time in Go requires interface injection; the added complexity is not justified for a 100 ms window. |

### May stub / inject via interface

| Dependency | How |
|------------|-----|
| `IndexFunc` (watcher callback) | Already an injectable function literal — use `atomic.Int32` counter as in existing tests. |
| `store.StoreRouter.ListProjects` | Use `newTestRouter` helper (already in `watcher_test.go`) backed by a real temp-dir store. No mock needed. |
| `Pipeline.Store` | For `communities_test.go`, use `OpenMemory()` directly. `Pipeline` accepts a `*store.Store`; construct one with `OpenMemory()` and set `p.Store` directly. |

### Avoid

- Do not introduce `gomock`, `testify/mock`, or any mock framework. The existing test suite uses only the standard library; that pattern must be preserved.
- Do not mock `time.Now` or `time.AfterFunc`; test with real short durations instead.

---

## 6. Test Infrastructure Notes

### Existing helpers to reuse

- `newTestRouter(t, projectName, rootPath)` in `watcher_test.go`: creates a `StoreRouter` in a temp dir with one registered project.
- `OpenMemory()` in `store/store.go`: in-memory SQLite with full schema including `community_cache`.
- `setupArchTestStore(t)` in `architecture_test.go`: seeded store with functions and CALLS edges — reusable as a starting point for `communities_test.go`.
- `addTestEdge` in `perf_louvain_test.go`: helper for building adjacency slices for `louvainLocalMoving` unit tests.

### New helpers needed

- `openMemoryWithProject(t, project, rootPath) *store.Store` — thin wrapper around `OpenMemory()` + `UpsertProject`. Avoids boilerplate across community cache tests.
- `makePartition(nodeIDs []int64, community int) map[int64]int` — constructs a uniform partition for warm-start tests.
- `seedCallGraph(t, s *store.Store, project string, edges [][2]int64)` — upserts minimal Function nodes and CALLS edges for pipeline integration tests.

### Race detector

All concurrency tests (`TestWatcherDebounceConcurrentSafety`, `TestCachedProjectsUsedByHandleFSEvent`) must pass under `go test -race`. The CI pipeline should run `go test -race ./internal/watcher/... ./internal/store/... ./internal/pipeline/...`.

### Build tags

No build tags required. `fsnotify` supports Linux inotify and macOS kqueue transparently. All tests run on the standard CI Linux runner.

### Test file placement

| New test file | Package | Reason |
|---------------|---------|--------|
| `internal/store/community_cache_test.go` | `package store` | Needs access to unexported `cacheBatchSize` for boundary tests |
| `internal/pipeline/communities_test.go` | `package pipeline` | Needs access to unexported `communityGraphHash`, `louvainCommunities` |
| Additions to `internal/watcher/watcher_test.go` | `package watcher` | Needs access to unexported `triggerDebounced`, `projectForPath`, `addProjectDirs`, `debounceMap`, `cachedProjects` |
| Additions to `internal/store/perf_louvain_test.go` or new `louvain_test.go` | `package store` | Needs access to unexported `louvainWithWarmStart` |
