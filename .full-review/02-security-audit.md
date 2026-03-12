# Phase 2: Security Audit -- Performance Changes (perf/week1-week2)

## Threat Model

**Binary**: Local CLI / MCP server running on a developer's machine.
**Trust boundary**: The binary indexes the local filesystem and processes project names, file paths, and Cypher-like queries supplied by the MCP client (typically an AI assistant). There is no network-facing API, no user authentication, and no multi-tenancy. The attacker model is:

1. **Malicious MCP client input** -- A compromised or adversarial MCP client sends crafted tool arguments (project names, paths, queries, aspects).
2. **Malicious repository content** -- The developer clones a repo containing adversarial filenames, symlinks, or file content that the indexer processes.
3. **Supply chain** -- Compromised or vulnerable dependencies.

Scope is limited to the files introduced or modified in the performance branch.

---

## Finding Summary

| ID | Severity | CWE | Component | Title |
|----|----------|-----|-----------|-------|
| S-1 | Medium | CWE-22 | `store/router.go` | Path traversal via unsanitized project name in `ForProject` / `DeleteProject` |
| S-2 | Low | CWE-89 | `store/community_cache.go` | SQL injection -- not present (parameterized queries) |
| S-3 | Low | CWE-22 | `watcher/watcher.go` | Symlink-following in `addProjectDirs` / `handleFSEvent` |
| S-4 | Low | CWE-400 | `tools/tools.go` | Unbounded `archCache` growth via distinct cache keys |
| S-5 | Low | CWE-400 | `watcher/watcher.go` | Unbounded `debounceMap` accumulation under sustained event storm |
| S-6 | Low | CWE-400 | `pipeline/resolver.go` | Unbounded `failedLookups` growth, never cleared |
| S-7 | Low | CWE-400 | `watcher/watcher.go` | Unbounded `cachedProjects` / `projects` maps (proportional to project count) |
| S-8 | Info | CWE-200 | Multiple | Information disclosure via slog and error messages |
| S-9 | Info | CWE-327 | `pipeline/communities.go` | Weak `communityGraphHash` -- correctness, not security |
| S-10 | Info | -- | `go.mod` | fsnotify v1.9.0 -- no known CVEs |
| S-11 | Info | CWE-284 | `store/store.go` | `OpenReadOnly` mode enforcement analysis |
| S-12 | Low | CWE-400 | `cypher/executor.go` | Unbounded `regexCache` in Cypher executor |
| S-13 | Medium | CWE-1333 | `cypher/executor.go` | ReDoS via user-supplied regex in Cypher `=~` operator |

---

## Detailed Findings

### S-1: Path Traversal via Unsanitized Project Name

**Severity**: Medium (CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:L/I:L/A:L -- 5.3)
**CWE**: CWE-22 (Improper Limitation of a Pathname to a Restricted Directory)
**Files**:
- `internal/store/router.go:61-78` (`ForProject`)
- `internal/store/router.go:82-85` (`ForProjectReadOnly`)
- `internal/store/router.go:161-179` (`DeleteProject`)
- `internal/store/router.go:182-186` (`HasProject`)
- `internal/store/store.go:67-74` (`Open`)

**Description**: Project names flow from MCP tool arguments (`project`, `project_name`) into `StoreRouter.ForProject(name)` which constructs a database path via `filepath.Join(r.dir, name+".db")`. There is no validation that `name` does not contain path separators or `..` components.

**Attack scenario**: An MCP client sends `project_name: "../../etc/passwd"`. This resolves to a path outside the cache directory. With `DeleteProject`, this could delete arbitrary files (that end in `.db`, `.db-wal`, `.db-shm`). With `ForProject` / `OpenInDir`, it could create or open a database at an arbitrary location.

The `resolveStore` function in `tools.go:267-281` only checks for `"*"` and `"all"` -- it does not sanitize path traversal characters.

`ProjectNameFromPath` (the auto-detect path) does sanitize by replacing `/` and `:` with `-`, but manually-supplied project names from tool arguments bypass this function entirely. The `handleIndexRepository` flow goes through `ProjectNameFromPath`, but `handleDeleteProject`, `handleGetArchitecture`, `handleSearchGraph`, `handleQueryGraph`, and other tools accept a raw `project` string argument.

**Mitigating factors**: (a) `filepath.Join` on Go normalizes `..` components but does not prevent escaping the base directory. (b) The binary runs as the developer's own user, so the blast radius is the user's own files. (c) The attacker must control MCP client messages. (d) File creation requires the `.db` suffix, limiting targets.

**Remediation**: Validate project names at the `StoreRouter` boundary. Reject names containing `/`, `\`, `..`, or null bytes. A simple allowlist of `[a-zA-Z0-9_-.]` would suffice. Apply this check in `ForProject`, `ForProjectReadOnly`, `HasProject`, and `DeleteProject`.

---

### S-2: SQL Injection in `community_cache.go` -- NOT PRESENT

**Severity**: Low (informational -- no vulnerability found)
**CWE**: CWE-89 (reviewed, not applicable)
**File**: `internal/store/community_cache.go:16-90`

**Analysis**: Both `SaveCommunityCache` and `LoadCommunityCache` use parameterized queries (`?` placeholders) for all user-supplied values (`project`, `graphHash`, `nodeID`, `community`). The dynamic SQL construction in `SaveCommunityCache:48-49` builds a `VALUES` clause using `(?,?,?,?)` placeholders, not string interpolation.

The `project` and `graphHash` parameters always flow through `?` placeholders:
- Line 21: `DELETE FROM community_cache WHERE project = ?`
- Line 48: `INSERT OR REPLACE INTO community_cache(project, node_id, community, graph_hash) VALUES (?,?,?,?)`
- Line 63-65: `SELECT node_id, community FROM community_cache WHERE project = ? AND graph_hash = ?`

**Verdict**: No SQL injection. The use of `strings.Join(placeholders, ",")` is safe because `placeholders` is a slice of the literal string `"(?,?,?,?)"` -- no user data enters the query template.

---

### S-3: Symlink-Following in Watcher

**Severity**: Low (CVSS:3.1/AV:L/AC:H/PR:L/UI:R/S:U/C:L/I:N/A:N -- 2.6)
**CWE**: CWE-22 (Improper Limitation of a Pathname to a Restricted Directory)
**Files**:
- `internal/watcher/watcher.go:172-190` (`addProjectDirs`)
- `internal/watcher/watcher.go:110-126` (`handleFSEvent`)
- `internal/discover/discover.go:301+` (`Discover`)

**Description**: `addProjectDirs` uses `filepath.WalkDir` which follows symlinks by default on the initial walk. The `handleFSEvent` function at line 113-118 calls `os.Stat` (which follows symlinks) and then `w.fsw.Add(event.Name)` on any newly created directory, without checking whether the path is a symlink pointing outside the project root.

The `Discover` function in `discover.go` also lacks symlink handling (no `Lstat` or `EvalSymlinks` calls found in the codebase).

**Attack scenario**: A malicious repository contains a symlink `project/link -> /etc/` or `project/link -> /home/user/.ssh/`. The watcher and indexer would follow the symlink and index files outside the intended project boundary. For the watcher, this means `fsnotify` watches are registered on directories outside the project root.

**Mitigating factors**: (a) The developer must clone and index a malicious repository. (b) The indexer only reads files with recognized source code extensions. (c) The indexed content is stored locally, not exfiltrated.

**Remediation**: Use `filepath.EvalSymlinks` on the project root, then verify that every walked path, after symlink resolution, still falls under the resolved root. Alternatively, skip symlinks in `WalkDir` by checking `d.Type()&fs.ModeSymlink != 0`.

---

### S-4: Unbounded `archCache` Growth

**Severity**: Low (CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:N/I:N/A:L -- 3.3)
**CWE**: CWE-400 (Uncontrolled Resource Consumption)
**File**: `internal/tools/tools.go:54-55`, `internal/tools/architecture.go:44-73`

**Description**: The `archCache` is keyed by `projName + ":" + aspects` where `aspects` is a sorted, comma-joined string of user-supplied aspect names. The cache is invalidated per-project on re-index (lines 111-119), which deletes entries with the matching project prefix. However:

1. The cache key includes the full aspects string. While aspects are validated against `validArchAspects` (12 possible values), the number of distinct subsets is 2^12 = 4096 unique keys per project. Each cached value is a serialized JSON string that can be tens of kilobytes.
2. Aspect ordering is preserved from user input (no sort before join), so `["languages","packages"]` and `["packages","languages"]` produce different cache keys for identical results. Phase 1 flagged this as "A-Low: aspect-order-dependent keys (cosmetic)" but it is also a memory concern -- it doubles the cache entry count for each permutation.
3. Cache entries for deleted projects are never cleaned up (the invalidation only runs on re-index, not on project deletion).

**Attack scenario**: Repeated calls with distinct aspect orderings or subset permutations grow the cache without bound. In practice, the 12-aspect limit and per-project invalidation make this a slow leak rather than an acute DoS.

**Remediation**: Sort the aspects array before building the cache key. Add an LRU eviction policy or cap the total cache size. Clear cache entries in `DeleteProject`.

---

### S-5: Unbounded `debounceMap` Under Event Storm

**Severity**: Low (CVSS:3.1/AV:L/AC:H/PR:L/UI:N/S:U/C:N/I:N/A:L -- 2.5)
**CWE**: CWE-400 (Uncontrolled Resource Consumption)
**File**: `internal/watcher/watcher.go:48-49`, `internal/watcher/watcher.go:210-241`

**Description**: `debounceMap` maps `proj.Name` to a pending timer. Since project names come from `cachedProjects` (database-backed), the map is bounded by the number of indexed projects. Each entry is cleaned up after the timer fires (lines 226-228, 235-237). This is well-bounded in practice.

**Assessment**: Not a real concern. The map grows at most to the number of indexed projects, and entries are cleaned up on timer expiry. No remediation needed beyond the race condition noted in Phase 1 (H-1).

---

### S-6: Unbounded `failedLookups` in FunctionRegistry

**Severity**: Low (CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:N/I:N/A:L -- 3.3)
**CWE**: CWE-400 (Uncontrolled Resource Consumption)
**File**: `internal/pipeline/resolver.go:28`, lines 140-171

**Description**: The `failedLookups` map caches names that failed to resolve, preventing repeated full scans. It is never cleared during a pipeline run, and the registry is reconstructed on each index run, so the map resets between runs. However, within a single large indexing operation, every unresolvable callee name (stdlib functions, external library calls) adds an entry. For a large codebase with thousands of external call references, this map could hold tens of thousands of entries.

Phase 1 flagged this as H-5: "failedLookups never cleared after buildRegistry(); newly-added functions may be incorrectly treated as unresolvable."

**Mitigating factors**: (a) The registry is short-lived (per pipeline run). (b) Each entry is a `string -> bool` pair -- small. (c) The map correctly prevents O(n) rescans.

**Remediation**: If the registry is long-lived (e.g., reused across incremental runs), clear `failedLookups` when new functions are registered. For single-run lifetime, the current behavior is acceptable.

---

### S-7: Unbounded `cachedProjects` / `projects` Maps

**Severity**: Low (informational)
**CWE**: CWE-400
**File**: `internal/watcher/watcher.go:52-57`

**Description**: `cachedProjects` is a slice refreshed every poll cycle from the database. `projects` maps project names to `projectState` (containing a snapshot map). Both grow proportionally to the number of indexed projects and their file counts.

**Assessment**: Proportional to the number of real indexed projects -- not attacker-controllable beyond what the developer indexes. The snapshot maps could be large for massive codebases (one entry per source file), but this is inherent to the polling design. No security remediation needed, though the memory profile should be considered for very large monorepos.

---

### S-8: Information Disclosure via Error Messages and Logging

**Severity**: Info
**CWE**: CWE-200 (Exposure of Sensitive Information)
**Files**: Multiple

**Description**: Error messages returned to the MCP client include internal details:

- `store/store.go:126-127`: `fmt.Errorf("open read-only %s: %w", dbPath, err)` -- exposes the full database file path (`~/.cache/codebase-memory-mcp/...`).
- `tools/tools.go:99`: `fmt.Errorf("store for %s: %w", projectName, err)` -- exposes project name and internal error.
- `tools/index.go:43`: `fmt.Sprintf("invalid path: %v", err)` -- exposes path resolution errors.
- `watcher/watcher.go:75,104,188,233,304,310`: `slog.Warn` and `slog.Debug` calls include `err` fields, file paths, and project names.

**Mitigating factors**: This is a local CLI tool. The error messages go to the local MCP client and local logs. There is no remote attacker to receive them. The exposed paths are the developer's own filesystem.

**Remediation**: For a local tool, this is acceptable. If the tool ever gains network exposure, wrap internal errors before returning to the client.

---

### S-9: Weak `communityGraphHash` -- Correctness Issue

**Severity**: Info (not a security vulnerability)
**CWE**: CWE-327 (Use of a Broken or Risky Cryptographic Algorithm) -- loosely applicable
**File**: `internal/pipeline/communities.go:16-25`

**Description**: `communityGraphHash` uses XOR for edge checksumming and addition for node checksumming. As Phase 1 noted (C-2/A-M-2), XOR is commutative and self-canceling: swapping two edges or adding/removing pairs with the same XOR produces identical hashes. The addition-based node sum is similarly collision-prone (any two sets of node IDs with the same sum collide).

**Security impact**: None. This hash is used only for cache invalidation of the Louvain warm-start partition. A collision results in loading a stale partition, which is a correctness issue (incorrect community assignments), not a security issue. An attacker cannot exploit this because they would need to manipulate the code graph structure, which requires write access to the developer's repository.

**Remediation**: Replace with a position-sensitive hash (e.g., FNV or xxHash over sorted ID sequences). This is a correctness fix, not a security fix.

---

### S-10: fsnotify v1.9.0 -- Dependency CVE Check

**Severity**: Info (no vulnerability found)
**File**: `go.mod:15`, `go.sum`

**Analysis**: `github.com/fsnotify/fsnotify v1.9.0` (indirect dependency). As of March 2026:

- No CVEs have been assigned to fsnotify v1.9.0 in the NVD, GitHub Advisory Database, or Go vulnerability database (govulncheck).
- The library is actively maintained (latest release in the v1.9.x series).
- `github.com/mattn/go-sqlite3 v1.14.34` is also current; no known CVEs for this version.

**Recommendation**: Continue monitoring via `govulncheck` in CI. Pin exact versions (already done via `go.sum`).

---

### S-11: `OpenReadOnly` Mode Enforcement Analysis

**Severity**: Info
**CWE**: CWE-284 (Improper Access Control)
**File**: `internal/store/store.go:116-144`

**Description**: `OpenReadOnly` constructs the DSN as:
```
"file:" + dbPath + "?mode=ro&_cache_size=-65536&_synchronous=OFF&_mmap_size=1073741824"
```

**Analysis of `mode=ro` effectiveness**:

1. The `file:` URI prefix activates SQLite's URI filename interpretation, which makes `mode=ro` a SQLite-level enforcement. SQLite will refuse `INSERT`, `UPDATE`, `DELETE`, and `CREATE` statements at the VFS layer -- this is not bypassable via SQL.

2. `PRAGMA query_only = ON` (line 136) provides a second layer. If the underlying driver supports it, this prevents write operations even if `mode=ro` were somehow bypassed.

3. **SQLite URI injection risk**: If `dbPath` is user-controlled and contains `?` or `&` characters, it could inject additional URI parameters. For example, `dbPath = "/tmp/test.db?mode=rw&"` would produce `"file:/tmp/test.db?mode=rw&&mode=ro&..."`. SQLite uses the **last** occurrence of a duplicate parameter, so the injected `mode=rw` would be overridden by the appended `mode=ro`. However, other parameters could be injected.

    In practice, `dbPath` in `ForProjectReadOnly` is `filepath.Join(r.dir, name+".db")` where `name` comes from the project name (see S-1). With the S-1 fix (restricting project names to `[a-zA-Z0-9_-.]`), URI injection is not possible.

**Verdict**: `mode=ro` is effective for preventing writes. The URI injection concern is theoretical and addressed by S-1's remediation.

---

### S-12: Unbounded `regexCache` in Cypher Executor

**Severity**: Low (CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:N/I:N/A:L -- 3.3)
**CWE**: CWE-400 (Uncontrolled Resource Consumption)
**File**: `internal/cypher/executor.go:20`, lines 920-933

**Description**: The `Executor.regexCache` map caches compiled `*regexp.Regexp` objects keyed by the raw pattern string from Cypher `=~` operators. The `Executor` is created per `handleQueryGraph` call (line 27 of `query.go`), so the cache is short-lived. However, within a single query execution, there is no cap on the number of distinct regex patterns.

**Assessment**: Since the executor is per-request and Go's `regexp.Regexp` objects are not excessively large, this is a minor concern. The real risk is CPU, not memory (see S-13).

---

### S-13: ReDoS via User-Supplied Regex in Cypher `=~` Operator

**Severity**: Medium (CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:N/I:N/A:L -- 3.3)
**CWE**: CWE-1333 (Inefficient Regular Expression Complexity)
**File**: `internal/cypher/executor.go:927-932`, `internal/tools/code_search.go:55-63`

**Description**: Go's `regexp` package uses a Thompson NFA implementation which guarantees O(n) matching time, so classic ReDoS (catastrophic backtracking) is **not possible**. However, compilation of very large or complex patterns can still be expensive, and the `regexp` package does not enforce a size limit on the pattern string.

An adversarial MCP client could send a Cypher query like:
```
MATCH (f:Function) WHERE f.name =~ '(a|b|c|...10000 alternatives...)' RETURN f
```

The compilation time is O(pattern_length) and the compiled automaton consumes memory proportional to the pattern size. This is bounded by the 200-row result cap but the regex compilation happens before any matching.

Similarly, `search_code` compiles user-supplied regex patterns (line 60), but the same Go `regexp` guarantee applies.

**Mitigating factors**: (a) Go regexp is ReDoS-safe by design. (b) The tool is local -- the attacker is the MCP client on the same machine. (c) Pattern compilation is bounded in time.

**Remediation**: Add a maximum pattern length check (e.g., 4096 characters) before calling `regexp.Compile`. This prevents degenerate pattern sizes without affecting legitimate use.

---

## Architecture-Level Security Observations

### Positive Security Properties

1. **Parameterized SQL throughout**: All SQL queries in the audited files use `?` placeholders. No string interpolation of user data into SQL was found in `community_cache.go`, `store.go`, `executor.go`, or any search/query handler. The Cypher executor builds SQL dynamically but uses `sqlPushableColumns` as an allowlist for column names and parameterizes all values.

2. **No network exposure**: The binary communicates via stdin/stdout MCP protocol. No HTTP server, no socket listeners, no remote attack surface.

3. **Single-user model**: The tool runs as the developer's user. Database files are created with directory permissions `0750`. No privilege escalation vectors.

4. **Bounded query results**: The Cypher executor caps results at 200 rows (`maxResultRows`). Intermediate binding sets are capped at 400 (`maxResultRows*2`). Variable-length BFS is capped at 10 hops.

5. **Read-only separation**: `OpenReadOnly` uses SQLite `mode=ro` which provides VFS-level write prevention.

### Areas for Improvement

1. **Input validation at the boundary**: Project names from tool arguments need sanitization before use in filesystem operations. This is the single most actionable finding (S-1).

2. **Symlink handling**: The watcher and indexer follow symlinks without restriction. For a local-only tool this is low risk, but it means indexing a malicious repository could cause the tool to read files outside the project boundary.

3. **Cache lifecycle management**: The `archCache` lacks eviction. While bounded by the number of projects times aspect permutations, adding a simple LRU or TTL would be good hygiene.

4. **Error message hygiene**: Error messages expose internal paths. Acceptable for a local tool, but worth addressing if the architecture ever changes.

---

## Risk Matrix

| Finding | Likelihood | Impact | Risk | Action |
|---------|-----------|--------|------|--------|
| S-1 Path traversal | Medium | Medium | **Medium** | Fix: validate project names |
| S-13 ReDoS (pattern size) | Low | Low | **Low** | Fix: cap pattern length |
| S-3 Symlink following | Low | Low | **Low** | Fix: skip symlinks in WalkDir |
| S-4 archCache unbounded | Low | Low | **Low** | Fix: sort aspects, add eviction |
| S-6 failedLookups unbounded | Low | Low | **Low** | Accept: per-run lifetime |
| S-12 regexCache unbounded | Low | Low | **Low** | Accept: per-request lifetime |
| S-2 SQL injection | N/A | N/A | **None** | Confirmed safe |
| S-10 fsnotify CVE | N/A | N/A | **None** | No known CVEs |
| S-11 OpenReadOnly bypass | N/A | N/A | **None** | mode=ro is effective |

---

## Recommended Priority

1. **S-1** (Medium) -- Add project name validation in `StoreRouter` methods. Allowlist `[a-zA-Z0-9._-]`, reject empty strings, reject strings containing path separators or `..`.
2. **S-13** (Low) -- Add a pattern length cap before `regexp.Compile` in both the Cypher executor and `search_code`.
3. **S-3** (Low) -- Skip symlinks in `filepath.WalkDir` callback and in `handleFSEvent`.
4. **S-4** (Low) -- Sort aspects before cache key construction; add LRU or max-size cap to `archCache`.
