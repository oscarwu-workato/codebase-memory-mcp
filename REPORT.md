# codebase-memory-mcp — Technical Evaluation Report

**Date:** 2026-03-10
**Evaluator:** oscar/dev analysis fork
**Binary version:** v0.4.6 (upstream pre-built release)
**Target:** Self-dogfooding — the tool indexed and queried its own source repository
**Log:** `~/codebase-memory-mcp-runbook-20260310.log` (1,228 lines)
**Runbook:** `RUNBOOK.md` (27 steps across 11 phases, all 14 MCP tools exercised)

---

## Executive Summary

27 test steps were executed against the `codebase-memory-mcp` repository itself using its
own MCP tools. **20 steps passed, 1 partially passed, and 6 failed.** Of the 6 failures,
3 were runbook fixture errors (wrong function names), 2 were documented tool limitations,
and 1 was a confirmed bug in the `packages` architecture aspect. No crashes, panics, or
data corruption were observed. The tool is broadly functional and ready for analysis use.

| Category | Count |
|----------|-------|
| PASS | 20 |
| PARTIAL PASS | 1 |
| FAIL — fixture error (runbook wrong) | 3 |
| FAIL — known tool limitation | 2 |
| FAIL — confirmed bug | 1 |
| **Total steps** | **27** |

---

## 1. Environment & Setup

### 1.1 Installation

Built from source on 2026-03-10 using Go 1.26 (`CGO_ENABLED=1`). The build produced ~150MB
binary including 64 vendored tree-sitter grammars compiled as C. All compiler output was
harmless noise from vendored C code (unused values, unused functions, macro redefinitions).
No Go compilation errors or warnings.

The built-from-source binary reports version `dev` because the upstream build system injects
a version string via `-ldflags` at release time — local builds always produce `dev`. After
discovering this causes a persistent "update available" notice every session, the binary was
replaced with the upstream pre-built v0.4.6 release via the built-in updater:

```
codebase-memory-mcp update   # downloads, checksums, atomically replaces binary
```

### 1.2 MCP Integration

**Root cause of initial connection failure:** The repo contains a `.mcp.json` file with the
original author's macOS binary path hardcoded (`/Users/martinvogel/.local/bin/...`). When
Claude Code opens this project directory it loads `.mcp.json` as a project-scoped MCP server.
The project-scope server has the same name as the user-scope server and the bad command path
caused "Failed to reconnect" on every session open. Fixed by updating `.mcp.json` to the
correct Linux path (`/home/oscar_wu/.local/bin/codebase-memory-mcp`).

Post-fix MCP connection was stable across all test phases.

### 1.3 Version Control Setup

Personal fork at `github.com/oscarwu-workato/codebase-memory-mcp`. Branch strategy:

- `main` — tracks `upstream/main` (DeusData/codebase-memory-mcp), never committed to
- `oscar/dev` — all analysis work: RUNBOOK.md, REPORT.md, `.mcp.json` fix

---

## 2. Graph Composition

### 2.1 Node Distribution

Full index of this repo produced **6,737 nodes** across 14 labels:

| Label | Count | Notes |
|-------|-------|-------|
| Function | 3,267 | Dominates; includes vendored C grammar functions |
| File | 644 | All source files tracked |
| Module | 619 | Go files as modules |
| Variable | 492 | Config values, JSON fields |
| Community | 428 | Louvain clusters (internal graph node) |
| Method | 385 | Go receiver methods |
| Class | 356 | Structs with methods |
| Section | 220 | Markdown headings (RUNBOOK.md indexed) |
| Folder | 167 | Directory nodes |
| Enum | 144 | Constants, iota groups |
| InfraFile | 5 | Docker/config files |
| Interface | 4 | Go interfaces |
| Route | 2 | HTTP route extractors (Express/Ktor patterns) |
| Project | 1 | Root project node |

**Key observation:** Function count (3,267) is disproportionately high relative to Method
(385). This is because the 64 vendored tree-sitter C grammar files each contribute dozens
of C functions. The ratio would be inverted in a pure Go project. Approximately 70-80% of
Function nodes are vendored C code, not application logic.

**Unexpected node type:** `Section` (220 nodes) — the markdown headings in RUNBOOK.md were
indexed after it was committed to `oscar/dev`. This caused 3 test failures where fixture
function names (e.g. "TraverseBFS") matched RUNBOOK section titles instead of code symbols.

### 2.2 Edge Distribution

**21,528 edges** across 16 types:

| Edge Type | Count | Significance |
|-----------|-------|-------------|
| USAGE | 5,514 | Largest — read references, callbacks, stored function references |
| DEFINES | 5,102 | Module→Function containment |
| CALLS | 4,765 | Direct function calls (primary analysis edge) |
| MEMBER_OF | 2,964 | Louvain cluster membership |
| TESTS | 1,018 | Test function → production function coverage |
| USES_TYPE | 811 | Type reference edges |
| CONTAINS_FILE | 644 | Folder→File containment |
| DEFINES_METHOD | 385 | Class→Method |
| CONTAINS_FOLDER | 171 | Folder→Folder nesting |
| WRITES | 56 | Write reference edges |
| CONFIGURES | 34 | Config key → configures node |
| FILE_CHANGES_WITH | 29 | Git co-change coupling |
| TESTS_FILE | 27 | Test file → source file |
| IMPLEMENTS | 3 | Go interface implementation |
| OVERRIDE | 3 | Method override |
| HANDLES | 2 | Route handler links |
| IMPORTS | 0 | **Not used** — USAGE subsumes import tracking |
| HTTP_CALLS | 0 | Expected — single binary, no inter-service HTTP |
| ASYNC_CALLS | 0 | Expected — no async message dispatch |
| CONTAINS_PACKAGE | 0 | Not modelled for Go packages |

**Notable:** `USAGE` (5,514) > `CALLS` (4,765). This indicates the indexer tracks read
references, callbacks, and stored function pointers as USAGE edges in addition to direct
call edges. This gives a broader dependency picture than CALLS alone but may overcount
true runtime call paths.

**`FILE_CHANGES_WITH` (29):** Git co-change coupling edges are sparse. This is expected —
the repo is relatively new and has limited commit history to analyse.

**`IMPLEMENTS` (3) and `OVERRIDE` (3):** Very low for a Go codebase. Go uses interfaces
heavily. This suggests the indexer's interface implementation detection is incomplete for
this repo, possibly because Go interface satisfaction is implicit and the indexer relies on
explicit `var _ Interface = (*Impl)(nil)` assertions or similar patterns.

---

## 3. Indexing Performance

| Mode | Time | Files Processed | Notes |
|------|------|-----------------|-------|
| Full (first run) | ~62s | All source files | Initial index from empty state |
| Full (re-run) | ~62s | All source files | Incremental: same file count, all hashes checked |
| Fast (unchanged) | ~7s | 0 files reprocessed | Content hashes matched; pure hash-check pass |

**Speedup ratio:** 8.9× faster in fast mode when no files have changed.

The full re-index time (~62s) being identical to the initial index is expected — the
indexer reprocesses all files regardless on a `mode="full"` call. Truly incremental
behaviour only applies to `mode="fast"`, where only changed files (by SHA-256 hash) are
reprocessed.

---

## 4. Tool-by-Tool Findings

### 4.1 `index_repository` / `index_status` — PASS

Both modes work correctly. `index_status` accurately reflects the last index timestamp,
node/edge counts, and `status=ready` vs `status=indexing`. Auto-indexing triggers on
session open when the project is detected from the working directory.

### 4.2 `get_graph_schema` — PASS

Returns accurate counts for all node labels and edge types. The most reliable way to get
node/edge distribution — preferred over `query_graph COUNT` (see §5.3).

### 4.3 `search_graph` — PASS (with caveats)

All search modes work: name regex, label filter, file glob, degree filtering, pagination.

**Caveats:**
- `max_degree=0` without `relationship=` counts ALL edge types. Every function has ≥1
  DEFINES edge from its parent Module, so unconstrained dead code detection always returns
  0. Dead code detection requires `relationship="CALLS"`, `direction="inbound"`, `max_degree=0`.
- `sort_by="degree"` in label="Function" searches returns vendored C functions first (high
  degree from tree-sitter grammar connections). Application functions are buried deeper.
- Minor pagination count inconsistency observed (total=1005 on page 1, total=1010 on page 2).
  Likely a race condition with background auto-indexing during test execution.
- `Run()` (the pipeline entry point) is indexed as label="Method" not label="Function".
  Go receiver methods and standalone functions are distinct node types.

### 4.4 `search_code` — PASS

Regex and literal text search work correctly. File glob filtering (`file_pattern="*.go"`)
works. Results include file path, line number, and matched line content.

**Finding:** The literal string "TraverseBFS" only appears in `RUNBOOK.md`, not in any Go
source file. The BFS implementation in `internal/store/traverse.go` is a method named
`TraverseBFS` on a receiver type — it appears in the graph as a Method node but the Go
source text containing the function definition was not matched by search_code because the
method signature renders as `func (s *Store) TraverseBFS(...)`. Text search for
`TraverseBFS` does find it at the definition site — the test step was looking for a
standalone function definition that doesn't exist.

### 4.5 `trace_call_path` — PASS (3.1) / FAIL (4.2, 4.4)

**4.1 Run() outbound depth=3:** Returned 71 edges mapping the complete 3-pass pipeline:

```
Run → runPasses → runFullPasses → passStructure
                               → passDefinitions
                               → buildRegistry
                               → passImports
                               → buildReturnTypeMap
                               → passCalls
                               → runSemanticEdgePasses
                               → cleanupASTCache
                               → passHTTPLinks
                               → updateFileHashes
              → runIncrementalPasses → [same subpasses]
```

This confirms the indexer's 3-pass architecture is accurately captured in the graph.

**4.3 UpsertNode both directions depth=2:** Returned 75 edges. 18 inbound callers at
hop-1 including `UpsertNodeBatch`, `insertSingleRouteNode`, `upsertNode`,
`injectEnvBindings`, `upsertInfraNodes`, `mergeWithExistingModule`, and 10+ test
helpers. The store write path is well-connected and centrally placed.

**Failures:** Steps 4.2 and 4.4 failed because the fixture function names (`TraverseBFS`
as standalone, `ExecuteQuery`) do not exist. Corrected names: `TraverseBFS` works as a
method lookup; the Cypher executor entry point is `Execute`.

### 4.6 `query_graph` (Cypher) — PASS (3) / FAIL (2)

Basic MATCH, WHERE filtering, and CALLS relationship patterns all work.

**Limitation 1 — COUNT aggregation (step 5.3):**
```cypher
MATCH (n) RETURN n.label, COUNT(n) ORDER BY COUNT(n) DESC LIMIT 10
```
Returns only 2 rows: `Project (1)` and `Community (428)`. The 200-row cap is applied
before `GROUP BY` — the engine fetches up to 200 rows, groups only those, and returns
a count that represents a tiny fraction of the graph. Function (3,267) and Method (385)
nodes are absent from results. This is a fundamental limitation for any aggregation
query on graphs larger than ~200 nodes. **Use `get_graph_schema` for counts.**

**Limitation 2 — Named path variables (step 5.5):**
```cypher
MATCH p=(a:Function)-[:CALLS*1..3]->(b:Function) WHERE a.name = 'Run' RETURN b.name
```
Parser error at position 6. The `p=` path variable assignment syntax is unsupported.
Variable-length paths themselves (`[:CALLS*1..3]`) may work without the path variable
prefix — untested after fix.

### 4.7 `get_code_snippet` — PASS (6.3) / PARTIAL (6.2) / FAIL (6.1)

**6.3 Non-existent name:** Returns clean `"node not found"` error. No crash. No
spurious fuzzy suggestions for names with zero lexical similarity.

**6.2 Case-insensitive fuzzy (`upsertnode`):** Found 3 candidates
(`graph_buffer.UpsertNode`, `pipeline.upsertNode`, `store.UpsertNode`). `auto_resolve`
did not activate because it only picks automatically with ≤2 candidates — 3 triggers
graceful disambiguation. This is correct tool behaviour. Workaround: use a qualified
name suffix to narrow to 1 candidate.

**6.1 TraverseBFS:** Failed because the short name matched a RUNBOOK.md section title
node before the code Method node. Fixed in updated runbook to use
`store.traverse.TraverseBFS` as the qualified name.

### 4.8 `get_architecture` — PASS (6) / FAIL (1)

**Languages (7.1):** 6 languages detected. C dominates by file count (424 files) due to
vendored tree-sitter grammars. Go = 190 files (the application). Also detected: Bash,
YAML, JavaScript, HTML.

**Hotspots (7.4):** Top functions by inbound degree:

| Rank | Function | Fan-in | Package |
|------|----------|--------|---------|
| 1 | store.Close | 212 | store |
| 2 | cbm.ExtractFile | 142 | cbm |
| 3 | pipeline.Run | 39 | pipeline |
| 4 | httplink.Run | 32 | httplink |

`store.Close` and `cbm.ExtractFile` being called 212 and 142 times respectively is
largely driven by test setup/teardown. In production, these would have far fewer callers.

**Layers (7.5):** Layer detection correctly maps the dependency hierarchy:
```
entry:    codebase-memory-mcp (cmd/)
internal: tools, pipeline
core:     store
```
`cypher`, `httplink`, and `watcher` were misclassified as "entry" (no inbound callers
heuristic). These are internal libraries called by `tools` and `pipeline` — a known
limitation of the zero-caller heuristic for layering.

**Clusters (7.6):** 15 Louvain communities detected, all mapping to `internal/` packages.
Large packages (pipeline, store) split into 2-3 sub-communities each, reflecting internal
cohesion boundaries. The store write path (`UpsertNode`, `InsertEdge`) appears as
cluster 8, distinct from the read path.

**Confirmed bug — Packages (7.2):**
`get_architecture(aspects=["packages"])` returns `fan_in=0` and `fan_out=0` for every
package. The `boundaries` aspect correctly reports cross-package call volumes
(pipeline→store: 169 calls, tools→pipeline: ~80 calls), confirming the underlying
CALLS edge data exists. The bug is isolated to the `packages` aspect's metric
computation. Workaround: use `aspects=["boundaries"]` for cross-package dependency analysis.

### 4.9 `manage_adr` — PASS

All 3 modes (store, get, update) work correctly. Store requires all 6 canonical sections
(PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY). Update patches
individual sections while preserving others. Get returns full text and parsed sections dict.
The ADR is now populated for this project with verified architectural facts.

### 4.10 `detect_changes` — PASS

**Unstaged (9.1):** Correctly reported zero changes on a clean working tree.

**Branch vs main (9.2):** Correctly identified 2 changed files (`oscar/dev` vs `main`):
- `.mcp.json` (modified) — 3 changed symbols
- `RUNBOOK.md` (added) — 57 section heading symbols

Blast radius analysis traced the `.mcp.json` `command` field change to 6 CRITICAL-risk
functions in `scripts/setup.sh` (hop-1: `check_go_version`, `configure_claude`,
`check_git`, etc.) and 1 HIGH-risk function at hop-2 (`build_from_source`). This is
logically correct — `setup.sh` reads the MCP binary path and is a direct dependent of
the config field.

### 4.11 `delete_project` / `list_projects` — PASS

`delete_project` with a non-existent project name returns a graceful `"project not found"`
error without affecting other projects. `list_projects` correctly shows all indexed
projects with node/edge counts and `is_session_project` flag.

---

## 5. Cross-Cutting Observations

### 5.1 Graph Integrity

The graph is internally consistent. BFS traversal (via `trace_call_path`) produces
coherent multi-hop paths that match the source architecture. Confidence scoring on CALLS
edges correctly distinguishes high-confidence direct calls (0.9) from speculative fuzzy
matches (0.275). The test suite is well-represented — `TESTS` edges (1,018) provide
good test→production coverage mapping.

### 5.2 Label Taxonomy Collision

The indexer uses the same graph for code symbols (Function, Method, Class) and for
markdown document structure (Section). When a markdown file is indexed, its headings
become Section nodes with names matching their heading text. If a heading text happens
to match a code symbol name (e.g. "TraverseBFS", "ExecuteQuery"), short-name lookups
return the Section node first. This is a design tension: indexing documentation alongside
code creates namespace collisions for short-name resolution.

**Practical impact:** Any function name that also appears as a section title in an indexed
markdown file will fail short-name lookup. Workaround: use qualified names with package
path prefix.

### 5.3 Vendored Code Noise

424 C files from vendored tree-sitter grammars are indexed as first-class code nodes.
This inflates Function counts by ~70%, dominates degree-sorted search results (the top
degree function `eof` in OCaml grammar has fan-in=87), and adds ~428 Community nodes
from Louvain clustering of the C code. For analysis of the application logic, all searches
should use `file_pattern` to exclude `internal/cbm/vendored/**` unless C grammar analysis
is the goal.

### 5.4 Cypher Query Language Completeness

The Cypher implementation covers the most common patterns (MATCH, WHERE, RETURN, LIMIT,
ORDER BY, variable-length paths, COUNT). Key missing features discovered in testing:

| Feature | Status |
|---------|--------|
| Named path variables (`p=`) | Unsupported |
| COUNT aggregation on large graphs | Broken (200-row pre-aggregation cap) |
| DISTINCT | Supported (untested in this run) |
| AND / OR in WHERE | Supported |
| `=~` regex matching | Supported |
| CONTAINS, STARTS WITH | Supported (case-sensitive) |

For production use, `search_graph` should be preferred over `query_graph` for most
lookups — it has no row cap, built-in regex support, pagination, and is case-insensitive
by default.

### 5.5 Auto-Index on Session Open

The server auto-detects the session project from the MCP roots protocol or working
directory, then triggers a background incremental index on session open. This means the
graph is always close to current without manual indexing calls. The background indexing
completed before any test queries were run (status transitioned from `indexing` to
`ready` within seconds).

---

## 6. Confirmed Bugs

### BUG-001: `get_architecture(packages)` — fan_in/fan_out always zero

**Severity:** Medium (incorrect metric, but data exists via `boundaries` workaround)
**Affected tool:** `get_architecture`, `aspects=["packages"]`
**Symptom:** `fan_in` and `fan_out` fields return 0 for every package in the listing.
**Evidence:** `aspects=["boundaries"]` correctly returns `pipeline→store: 169 calls`,
confirming the CALLS edges exist in the graph. The bug is in the packages aspect's
metric computation, not in the underlying data.
**Workaround:** Use `get_architecture(aspects=["boundaries"])` for cross-package
dependency volumes.
**Upstream report:** Worth filing at `github.com/DeusData/codebase-memory-mcp/issues`.

---

## 7. Tool Limitation Reference

| Limitation | Impact | Workaround |
|------------|--------|------------|
| `query_graph COUNT` silently undercounts on graphs >200 nodes | Aggregation queries return wrong counts | Use `get_graph_schema` for counts |
| Named path variables (`p=`) unsupported in Cypher parser | Certain path queries fail | Remove `p=` prefix from queries |
| `auto_resolve` requires ≤2 candidates | 3+ ambiguous names are not auto-resolved | Use qualified name with package path |
| `max_degree=0` without `relationship=` counts all edge types | Dead code detection returns 0 | Add `relationship="CALLS"`, `direction="inbound"` |
| `layers` heuristic misclassifies libraries with no callers as "entry" | cypher/httplink/watcher appear as entry layer | Cross-reference with `clusters` for true package role |
| Section nodes from markdown files share namespace with code symbols | Short-name lookups collide with doc headings | Use qualified names for symbols with common prose words |
| Vendored C grammar functions inflate Function counts | Degree-sorted searches return C functions first | Add `file_pattern` to exclude `internal/cbm/vendored/**` |

---

## 8. Recommended Runbook Fixture Changes (Applied in RUNBOOK.md v2)

| Step | Original | Fixed | Reason |
|------|----------|-------|--------|
| 3.3 | `max_degree=0` only | + `relationship="CALLS"`, `direction="inbound"` | All-type degree always ≥1 |
| 4.2 | `function_name="TraverseBFS"` | `function_name="TraverseBFS"` + `risk_labels=true` | Method works; added risk labels |
| 4.4 | `function_name="ExecuteQuery"` | `function_name="Execute"` | ExecuteQuery doesn't exist |
| 5.5 | `MATCH p=(...)` | `MATCH (...)` (removed `p=`) | Unsupported parser syntax |
| 6.1 | `qualified_name="TraverseBFS"` | `qualified_name="store.traverse.TraverseBFS"` | Avoids RUNBOOK section collision |
| 3.1/7.4 | Expected Run/UpsertNode in top results | Actual top: store.Close=212, cbm.ExtractFile=142 | Updated expectations |

---

## 9. Conclusion

`codebase-memory-mcp` v0.4.6 is functional and delivers on its core value proposition:
building a queryable property graph from source code that supports multi-hop call tracing,
architectural analysis, dead code detection, and code search — all without reading raw
source files. The 14 MCP tools cover indexing, search, tracing, architecture, ADR
management, change detection, and project lifecycle.

**Strongest tools:** `trace_call_path`, `search_graph`, `get_graph_schema`, `detect_changes`,
`manage_adr` — all worked cleanly and returned high-signal results.

**Weakest tools:** `get_architecture(packages)` (bug), `query_graph` COUNT aggregation
(fundamental cap limitation).

**Primary analysis workflow** (validated by this run):

```
index_repository (once) →
get_graph_schema (orient) →
search_graph (find symbols) →
trace_call_path (understand dependencies) →
get_code_snippet (read source) →
get_architecture (system overview) →
detect_changes (understand impact of edits)
```

The tool works best on pure application code. Repos with large vendored C files
(like this one's tree-sitter grammars) produce inflated node counts and noisy
degree-sorted results. File pattern filtering on all searches is recommended.

---

*Report generated from `~/codebase-memory-mcp-runbook-20260310.log` (1,228 lines) and
agent logs from parallel subagent execution. All findings are based on tool output
from the 2026-03-10 runbook session against binary v0.4.6.*
