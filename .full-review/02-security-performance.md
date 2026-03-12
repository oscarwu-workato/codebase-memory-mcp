# Phase 2: Security & Performance Review

## Security Findings

### Medium
- **S-1** `store/router.go:61-78` — Path traversal via unsanitized project name. Project names flow from MCP tool arguments directly into `filepath.Join(r.dir, name+".db")`. A crafted name like `../../etc/passwd` escapes the cache directory. `DeleteProject` is highest-risk (calls `os.Remove`). Fix: validate project names at `StoreRouter` boundary against `[a-zA-Z0-9._-]`.

### Low
- **S-2** `archCache` unbounded growth — adversarial aspect combinations could fill memory; add LRU eviction cap.
- **S-3** `watcher.go:113` — `os.Stat` on arbitrary event path before `fsw.Add`; symlink races possible on hostile filesystems.
- **S-4** `community_cache.go` SQL dynamic VALUES clause — confirmed safe (only `"(?,?,?,?)"` literal, never user data).
- **S-5** `OpenReadOnly` URI injection — theoretical; mitigated once S-1 project name validation applied.

### Informational
- SQL injection: absent (all queries use `?` placeholders).
- `fsnotify v1.9.0` and `mattn/go-sqlite3 v1.14.34`: no known CVEs as of March 2026.
- `communityGraphHash` XOR weakness: correctness issue (Phase 1 C-2), not a security vulnerability.
- `OpenReadOnly` with `mode=ro`: enforced at SQLite VFS layer, effective.

---

## Performance Findings

### Medium
- **P-1** Missing composite index `community_cache(project, graph_hash)` — `LoadCommunityCache` queries both columns but only `project` is indexed; 47K-row scan on every cache miss (5–500 ms overhead). Fix: add `(project, graph_hash)` composite index in `store.go` migration and `bulk.go`.
- **P-2** `get_architecture` Louvain has no warm-start — `buildClusters` calls `louvain()` (nil warm-start) on every `archCache` miss, running up to 15 cold iterations (~30–80 ms). The `community_cache` warm-start from `passCommunities` is never consulted. Fixing requires consolidating the dual Louvain implementations (C-1/A-H-1).

### Low-Medium
- **P-3** `addProjectDirs` holds `pollMu` during `filepath.WalkDir` — blocks other projects' poll-state access for 200–500 ms on first registration of a large repo. Fix: lock only for map mutation, walk outside the lock.
- **P-4** `SaveCommunityCache` 189 `tx.ExecContext` calls per 47K-node save — 150–300 ms synchronous tail added to first pipeline run after topology change. Use a prepared statement across batches; pre-sort entries by `node_id`.

### Low
- `debounceMap` goroutine count: bounded by project count, not event rate — not a scalability concern.
- `refreshProjectCache` frequency: not called on every fsnotify event; in-memory hot path is correct.
- `archCache` unbounded growth: negligible for typical use; theoretical risk under adversarial aspect iteration.

---

## Critical Issues for Phase 3 Context

1. **S-1 Path traversal**: project name validation should have input sanitization tests.
2. **P-1 Missing composite index**: cache correctness test should verify LoadCommunityCache on hash mismatch is fast.
3. **C-2 graphHash collisions**: test coverage for topology-change detection edge cases (XOR commutative collision).
4. **C-1 Dual Louvain**: no integration test verifies that `get_architecture clusters` and `Community` nodes agree.
