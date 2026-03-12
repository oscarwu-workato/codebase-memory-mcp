# Phase 1: Code Quality & Architecture Review

## Code Quality Findings

### Critical
- **C-1** `store/louvain.go` + `pipeline/communities.go` — Two independent Louvain implementations exist with different data structures, edge weighting, convergence criteria, and iteration caps. Both execute during a single pipeline run and can produce conflicting community assignments. Largest technical debt item.
- **C-2** `pipeline/communities.go:communityGraphHash` — XOR + sum hash has trivial collision properties (commutative operations). A false cache hit loads a stale warm-start partition, silently producing incorrect community assignments.

### High
- **H-1** `watcher.go:210-241` — Debounce timer Reset race: a fired timer can be Reset while its callback executes, causing duplicate index runs.
- **H-2** `pipeline.go:462-469` — Eight `DeleteEdgesBySourceFile` calls discard errors silently inside a transaction; partial failure risks duplicate nodes/edges.
- **H-3** `search.go:172` — `computeSQLLimit` accepts `qnHasLikeHints` but never uses it; 50K cap is incomplete for QN-only searches.
- **H-4** `architecture.go:700` — `buildOneCluster` iterates all edges per community O(C×E); degrades on large codebases.
- **H-5** `resolver.go:28` — `failedLookups` never cleared after `buildRegistry()`; newly-added functions may be incorrectly treated as unresolvable.
- **H-6** `watcher.go:280` — `pollAll` calls `ForProject` per project per poll cycle without documented connection lifecycle guarantees.
- **H-7** `search.go:62` — `loadConnectedNames` discards `rows.Err()`, hiding mid-scan errors.

### Medium (12 total)
Parameter count violations, dead `pollInterval` function, stale docstrings, magic numbers, non-deterministic map iteration in community assignment, naming issues.

### Low (8 total)
Style guide, minor smells.

---

## Architecture Findings

### High
- **A-H-1** Dual Louvain: same as C-1. `store/louvain.go` (used by `get_architecture`) and `pipeline/communities.go` (used for persisted Community nodes) can diverge. Should consolidate.

### Medium
- **A-M-1** `community_cache` missing composite index on `(project, graph_hash)` — `LoadCommunityCache` queries `WHERE project=? AND graph_hash=?` but only `idx_community_cache_project` covers `(project)` alone. Cache-miss queries scan all rows for the project.
- **A-M-2** `communityGraphHash` XOR collision class (same as C-2) — position-sensitive hash would eliminate false hits.

### Low
- Arch cache keys are aspect-order-dependent (cosmetic)
- `SaveCommunityCache` uses `s.db` not `s.q` (intentional)
- `pollProject` modifies `projectState` without `pollMu` (safe under single-goroutine invariant, should be commented)
- `passCommunities` uses `context.Background()` (consistent with existing patterns)

---

## Critical Issues for Phase 2 Context

1. **Dual Louvain** (C-1 / A-H-1): Performance correctness concern — two algorithms may produce inconsistent results in the same run.
2. **graphHash collisions** (C-2 / A-M-2): Correctness concern — false warm-start cache hit corrupts community assignments.
3. **Missing composite index** (A-M-1): Performance concern — cache-miss `LoadCommunityCache` does a partial table scan.
4. **Timer Reset race** (H-1): Concurrency correctness — debounce may fire twice.
5. **Silent edge deletion errors** (H-2): Data integrity risk on incremental re-index.
