# Codebase Memory MCP — Incremental Test & Verification Runbook

This runbook tests all 14 MCP tools against the repo itself (dogfooding).
Every step logs timestamped results to a single session log file for tracing and profiling.

**Log file:** `~/codebase-memory-mcp-runbook-YYYYMMDD.log`
**Target project path:** `/home/oscar_wu/codebase-memory-mcp`

---

## How to use this runbook

**Option A — One step at a time:** Copy each prompt block into a Claude Code session individually.

**Option B — Single session:** Paste the [Master Session Prompt](#master-session-prompt-single-paste) at the bottom into one Claude Code session. It runs all phases sequentially and writes one consolidated log.

---

## Prerequisites

MCP server must be active in your Claude Code session. Verify with:

```
/mcp
```

You should see `codebase-memory-mcp` listed as connected.

---

## Phase 1 — Indexing

### Step 1.1: Check pre-index status

```
LOG=~/codebase-memory-mcp-runbook-$(date +%Y%m%d).log
echo "=== RUNBOOK SESSION START $(date -Iseconds) ===" >> "$LOG"
echo "=== STEP 1.1: index_status (before indexing) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool index_status with no arguments.
Append the full JSON response to $LOG under the header above.
Then tell me: is the project already indexed? If yes, what was the last indexed timestamp?
```

**Expected:** Either "not indexed" or a previous timestamp from the install run.

---

### Step 1.2: Full index

```
echo "=== STEP 1.2: index_repository (full mode) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool index_repository with:
  repo_path: "/home/oscar_wu/codebase-memory-mcp"
  mode: "full"

Append the full response to $LOG.
Report: total nodes created, total edges created, time taken, any errors.
```

**Expected:** Thousands of nodes (functions, classes, modules across 64 language parsers + Go source). No errors. Status "complete".

---

### Step 1.3: Verify post-index status

```
echo "=== STEP 1.3: index_status (after full index) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool index_status.
Append response to $LOG.
Confirm: indexed_at timestamp updated, node/edge counts present.
```

---

### Step 1.4: Fast mode (incremental, no changes)

```
echo "=== STEP 1.4: index_repository (fast mode, no changes) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool index_repository with:
  repo_path: "/home/oscar_wu/codebase-memory-mcp"
  mode: "fast"

Append response to $LOG.
Report: how many files were skipped (unchanged hashes)? How much faster than full mode?
```

**Expected:** Near-instant — all files unchanged, all hashes match, 0 files reprocessed.

---

## Phase 2 — Schema Discovery

### Step 2.1: Node and edge type distribution

```
echo "=== STEP 2.1: get_graph_schema === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_graph_schema.
Append the full response to $LOG.

Report back:
1. All node labels and their counts (expect: Function, Method, Class, Module, Package, File, etc.)
2. All edge types and their counts (expect: CALLS, IMPORTS, IMPLEMENTS, USAGE, etc.)
3. Any edge type with count=0 that you'd expect to see (flag as a gap)
```

**Expected:** Function/Method nodes dominate. CALLS edges > IMPORTS > USAGE. IMPLEMENTS present (Go interfaces). HTTP_CALLS likely 0 (no HTTP server in this codebase).

---

### Step 2.2: List indexed projects

```
echo "=== STEP 2.2: list_projects === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool list_projects.
Append response to $LOG.
Confirm: codebase-memory-mcp project is listed with correct root_path.
```

---

## Phase 3 — Search

### Step 3.1: search_graph — find all Function nodes

```
echo "=== STEP 3.1: search_graph (label=Function, limit=20) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool search_graph with:
  label: "Function"
  limit: 20
  sort_by: "degree"

Append response to $LOG.
Report: top 5 functions by degree (most connected). Are these the expected high-traffic functions?
```

**Expected:** High-degree functions likely from pipeline.go or tools.go (e.g., Run, UpsertNode, InsertEdge).

---

### Step 3.2: search_graph — name regex pattern

```
echo "=== STEP 3.2: search_graph (name_pattern=Upsert) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool search_graph with:
  name_pattern: "Upsert"
  limit: 20

Append response to $LOG.
List all matched functions and their files. Do UpsertNode and UpsertNodeBatch appear?
```

---

### Step 3.3: search_graph — dead code detection

```
echo "=== STEP 3.3: search_graph (dead code: Function, max_degree=0) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool search_graph with:
  label: "Function"
  max_degree: 0
  exclude_entry_points: true
  limit: 30

Append response to $LOG.
List any functions with zero connections (candidates for dead code). Note their file paths.
```

---

### Step 3.4: search_graph — file-scoped search

```
echo "=== STEP 3.4: search_graph (file_pattern=pipeline.go) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool search_graph with:
  file_pattern: "*pipeline.go"
  label: "Function"
  limit: 30

Append response to $LOG.
List all functions in pipeline.go. Does Run() appear? How many functions total?
```

---

### Step 3.5: search_graph — pagination

```
echo "=== STEP 3.5: search_graph (pagination test) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool search_graph with:
  label: "Function"
  limit: 5
  offset: 0

Then call it again with offset: 5.
Append both responses to $LOG.
Confirm: has_more=true on first page, no overlap between pages.
```

---

### Step 3.6: search_code — text search

```
echo "=== STEP 3.6: search_code (pattern=TraverseBFS) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool search_code with:
  pattern: "TraverseBFS"
  max_results: 10

Append response to $LOG.
Report: which files contain TraverseBFS? Both definition (traverse.go) and call sites?
```

---

### Step 3.7: search_code — regex

```
echo "=== STEP 3.7: search_code (regex=func.*Tool.*Handler) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool search_code with:
  pattern: "func.*Tool"
  regex: true
  file_pattern: "*.go"
  max_results: 15

Append response to $LOG.
List matched lines and files.
```

---

## Phase 4 — Call Tracing

### Step 4.1: Outbound call chain from Run

```
echo "=== STEP 4.1: trace_call_path (Run, outbound, depth=3) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool trace_call_path with:
  function_name: "Run"
  direction: "outbound"
  depth: 3

Append response to $LOG.
Report: what does Run() call? Does the 3-pass pipeline structure appear (pass1, pass2, pass3 or similar)?
```

---

### Step 4.2: Inbound callers of TraverseBFS

```
echo "=== STEP 4.2: trace_call_path (TraverseBFS, inbound) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool trace_call_path with:
  function_name: "TraverseBFS"
  direction: "inbound"
  depth: 2

Append response to $LOG.
Report: which functions call TraverseBFS? Are they in tools/ or pipeline/?
Risk labels should appear (CRITICAL for hop-1 callers).
```

---

### Step 4.3: Both directions from UpsertNode

```
echo "=== STEP 4.3: trace_call_path (UpsertNode, both, depth=2) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool trace_call_path with:
  function_name: "UpsertNode"
  direction: "both"
  depth: 2

Append response to $LOG.
Report the full call neighborhood. Is UpsertNodeBatch a sibling? Who are the callers?
```

---

### Step 4.4: Confidence-filtered tracing

```
echo "=== STEP 4.4: trace_call_path (min_confidence=0.7) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool trace_call_path with:
  function_name: "ExecuteQuery"
  direction: "inbound"
  depth: 3
  min_confidence: 0.7

Append response to $LOG.
Report: how many callers pass the 0.7 confidence threshold?
```

---

## Phase 5 — Cypher Graph Queries

### Step 5.1: Basic MATCH

```
echo "=== STEP 5.1: query_graph (MATCH Function) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool query_graph with:
  query: "MATCH (n:Function) RETURN n.name, n.file_path LIMIT 10"

Append response to $LOG.
Confirm: 10 results returned, each with name and file_path.
```

---

### Step 5.2: CALLS relationship pattern

```
echo "=== STEP 5.2: query_graph (CALLS pattern) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool query_graph with:
  query: "MATCH (a:Function)-[:CALLS]->(b:Function) RETURN a.name, b.name LIMIT 20"

Append response to $LOG.
List 5 caller→callee pairs from the results.
```

---

### Step 5.3: COUNT aggregation

```
echo "=== STEP 5.3: query_graph (COUNT by label) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool query_graph with:
  query: "MATCH (n) RETURN n.label, COUNT(n) ORDER BY COUNT(n) DESC LIMIT 10"

Append response to $LOG.
Confirm: counts match the schema report from Step 2.1.
```

---

### Step 5.4: WHERE filtering

```
echo "=== STEP 5.4: query_graph (WHERE file_path CONTAINS pipeline) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool query_graph with:
  query: "MATCH (n:Function) WHERE n.file_path CONTAINS 'pipeline' RETURN n.name, n.file_path LIMIT 20"

Append response to $LOG.
Confirm: only pipeline/ files returned.
```

---

### Step 5.5: Variable-length path

```
echo "=== STEP 5.5: query_graph (variable-length CALLS path) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool query_graph with:
  query: "MATCH p=(a:Function)-[:CALLS*1..3]->(b:Function) WHERE a.name = 'Run' RETURN b.name LIMIT 20"

Append response to $LOG.
Report: which functions are reachable from Run within 3 hops?
```

---

## Phase 6 — Code Intelligence

### Step 6.1: get_code_snippet — exact qualified name

```
echo "=== STEP 6.1: get_code_snippet (TraverseBFS, exact) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_code_snippet with:
  qualified_name: "TraverseBFS"
  include_neighbors: true

Append response to $LOG.
Report: source code returned? Signature correct? Neighbor counts (callers/callees)?
```

---

### Step 6.2: get_code_snippet — fuzzy resolution

```
echo "=== STEP 6.2: get_code_snippet (auto_resolve fuzzy) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_code_snippet with:
  qualified_name: "upsertnode"
  auto_resolve: true

Append response to $LOG.
Confirm: auto_resolve finds UpsertNode despite case mismatch.
```

---

### Step 6.3: get_code_snippet — non-existent symbol

```
echo "=== STEP 6.3: get_code_snippet (non-existent) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_code_snippet with:
  qualified_name: "ThisFunctionDefinitelyDoesNotExist"
  auto_resolve: true

Append response to $LOG.
Confirm: graceful error message, no crash. Does it suggest alternatives?
```

---

## Phase 7 — Architecture Analysis

### Step 7.1: Language distribution

```
echo "=== STEP 7.1: get_architecture (languages) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_architecture with:
  aspects: ["languages"]

Append response to $LOG.
Report: which languages detected? Go should dominate. Any C (vendored grammars)?
```

---

### Step 7.2: Package structure

```
echo "=== STEP 7.2: get_architecture (packages) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_architecture with:
  aspects: ["packages"]

Append response to $LOG.
Report: top 5 packages by size. Does pipeline/ show the highest fan-out?
```

---

### Step 7.3: Entry points

```
echo "=== STEP 7.3: get_architecture (entry_points) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_architecture with:
  aspects: ["entry_points"]

Append response to $LOG.
Confirm: main() appears. Any other entry points (init functions, exported top-level)?
```

---

### Step 7.4: Hotspots

```
echo "=== STEP 7.4: get_architecture (hotspots) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_architecture with:
  aspects: ["hotspots"]

Append response to $LOG.
Report: top 10 hotspot functions by call degree. Do UpsertNode, InsertEdge, Run appear near the top?
```

---

### Step 7.5: Dependency layers

```
echo "=== STEP 7.5: get_architecture (layers) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_architecture with:
  aspects: ["layers"]

Append response to $LOG.
Report: what layers are detected? Expected: cmd → tools → pipeline → store (top-to-bottom).
Any layer violations (lower layer calling higher)?
```

---

### Step 7.6: Cluster communities

```
echo "=== STEP 7.6: get_architecture (clusters) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_architecture with:
  aspects: ["clusters"]

Append response to $LOG.
Report: how many clusters? Do they map to the internal/ packages (store, pipeline, tools, cypher)?
```

---

### Step 7.7: Full architecture snapshot

```
echo "=== STEP 7.7: get_architecture (all aspects) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool get_architecture with:
  aspects: ["all"]

Append the full response to $LOG.
Summarize: total aspects returned, any aspects that returned empty results (gaps to flag).
```

---

## Phase 8 — Architecture Decision Records

### Step 8.1: Store an ADR

```
echo "=== STEP 8.1: manage_adr (store) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool manage_adr with:
  mode: "store"
  content: |
    PURPOSE: Code analysis MCP server that builds a property graph from source code using tree-sitter AST extraction.
    STACK: Go 1.26, SQLite (WAL mode), tree-sitter (64 language grammars), MCP stdio protocol.
    ARCHITECTURE: 3-pass indexing pipeline (definition extraction → call resolution → enrichment). Per-project SQLite databases. 14 MCP tools.
    PATTERNS: Property graph (nodes+edges+JSON properties). BFS traversal for call chains. Cypher-like query language. Content-hash-based incremental indexing.
    TRADEOFFS: Single-writer SQLite limits write concurrency but simplifies deployment. Vendored grammars increase binary size but remove runtime dependencies.
    PHILOSOPHY: Dogfood the tool on itself. Prioritize query speed over write speed. Graph schema reflects language semantics, not file layout.

Append response to $LOG.
Confirm: ADR stored successfully.
```

---

### Step 8.2: Get the ADR

```
echo "=== STEP 8.2: manage_adr (get) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool manage_adr with:
  mode: "get"

Append response to $LOG.
Confirm: the ADR content matches what was stored in Step 8.1. All 6 sections present?
```

---

### Step 8.3: Update a section

```
echo "=== STEP 8.3: manage_adr (update section) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool manage_adr with:
  mode: "update"
  sections: {"TRADEOFFS": "Single-writer SQLite limits write concurrency but simplifies deployment and removes operational overhead. Vendored grammars increase binary size (~50MB) but remove runtime dependencies and ensure reproducible builds. 200-row Cypher cap prevents runaway queries at the cost of full-graph traversal."}

Append response to $LOG.
Then call manage_adr with mode: "get" to verify the update was applied.
Append that response too.
```

---

## Phase 9 — Change Detection

### Step 9.1: Detect unstaged changes (baseline — clean repo)

```
echo "=== STEP 9.1: detect_changes (unstaged, clean repo) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool detect_changes with:
  scope: "unstaged"

Append response to $LOG.
Expected: no changes detected (repo is clean). If changes present, list them.
```

---

### Step 9.2: Detect changes vs main branch

```
echo "=== STEP 9.2: detect_changes (branch scope) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool detect_changes with:
  scope: "branch"
  base_branch: "main"
  depth: 2

Append response to $LOG.
Report: any symbols changed relative to main? What is the blast radius (callers affected)?
```

---

## Phase 10 — Project Management Cleanup

### Step 10.1: Delete test project (safety check)

```
echo "=== STEP 10.1: delete_project (non-existent, safety) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool delete_project with:
  project: "this-project-does-not-exist"

Append response to $LOG.
Confirm: graceful error (not found), real project unaffected.
Do NOT delete the actual codebase-memory-mcp project.
```

---

### Step 10.2: Verify project intact after cleanup test

```
echo "=== STEP 10.2: list_projects (verify intact) === $(date -Iseconds)" >> "$LOG"

Use the codebase-memory-mcp MCP tool list_projects.
Append response to $LOG.
Confirm: codebase-memory-mcp project still present.
```

---

## Phase 11 — Final Session Report

```
echo "=== STEP 11: FINAL REPORT === $(date -Iseconds)" >> "$LOG"

Review all logged steps.
Generate a summary report with:

1. PASS/FAIL for each step (1.1 through 10.2)
2. Any unexpected results or gaps flagged
3. Node counts: total Function, Method, Class, Module nodes
4. Edge counts: total CALLS, IMPORTS, IMPLEMENTS edges
5. Performance observations: which tools were slowest?
6. Gaps: any edge types expected but missing? Any tools that errored?

Append this summary to $LOG under header "=== FINAL SUMMARY ===" with timestamp.
Then print the full path of the log file.
```

---

## Master Session Prompt (Single Paste)

Paste the entire block below into one Claude Code session to run all phases sequentially:

```
You are running the codebase-memory-mcp test runbook. Execute all steps in order.
Log every tool call and its response to: ~/codebase-memory-mcp-runbook-$(date +%Y%m%d).log

Use the codebase-memory-mcp MCP server (must be connected).
Target project: /home/oscar_wu/codebase-memory-mcp

For every step: write a header "=== STEP N.N: description === [timestamp]" to the log,
then append the tool's full JSON response, then append a one-line PASS/FAIL verdict.

Run these steps in order:

STEP 1.1 — index_status (before)
STEP 1.2 — index_repository (repo_path="/home/oscar_wu/codebase-memory-mcp", mode="full")
STEP 1.3 — index_status (after)
STEP 1.4 — index_repository (same path, mode="fast")
STEP 2.1 — get_graph_schema
STEP 2.2 — list_projects
STEP 3.1 — search_graph (label="Function", limit=20, sort_by="degree")
STEP 3.2 — search_graph (name_pattern="Upsert", limit=20)
STEP 3.3 — search_graph (label="Function", max_degree=0, exclude_entry_points=true, limit=30)
STEP 3.4 — search_graph (file_pattern="*pipeline.go", label="Function", limit=30)
STEP 3.5 — search_graph pagination: offset=0 then offset=5, limit=5 each
STEP 3.6 — search_code (pattern="TraverseBFS", max_results=10)
STEP 3.7 — search_code (pattern="func.*Tool", regex=true, file_pattern="*.go", max_results=15)
STEP 4.1 — trace_call_path (function_name="Run", direction="outbound", depth=3)
STEP 4.2 — trace_call_path (function_name="TraverseBFS", direction="inbound", depth=2)
STEP 4.3 — trace_call_path (function_name="UpsertNode", direction="both", depth=2)
STEP 4.4 — trace_call_path (function_name="ExecuteQuery", direction="inbound", depth=3, min_confidence=0.7)
STEP 5.1 — query_graph ("MATCH (n:Function) RETURN n.name, n.file_path LIMIT 10")
STEP 5.2 — query_graph ("MATCH (a:Function)-[:CALLS]->(b:Function) RETURN a.name, b.name LIMIT 20")
STEP 5.3 — query_graph ("MATCH (n) RETURN n.label, COUNT(n) ORDER BY COUNT(n) DESC LIMIT 10")
STEP 5.4 — query_graph ("MATCH (n:Function) WHERE n.file_path CONTAINS 'pipeline' RETURN n.name, n.file_path LIMIT 20")
STEP 5.5 — query_graph ("MATCH p=(a:Function)-[:CALLS*1..3]->(b:Function) WHERE a.name = 'Run' RETURN b.name LIMIT 20")
STEP 6.1 — get_code_snippet (qualified_name="TraverseBFS", include_neighbors=true)
STEP 6.2 — get_code_snippet (qualified_name="upsertnode", auto_resolve=true)
STEP 6.3 — get_code_snippet (qualified_name="ThisFunctionDefinitelyDoesNotExist", auto_resolve=true)
STEP 7.1 — get_architecture (aspects=["languages"])
STEP 7.2 — get_architecture (aspects=["packages"])
STEP 7.3 — get_architecture (aspects=["entry_points"])
STEP 7.4 — get_architecture (aspects=["hotspots"])
STEP 7.5 — get_architecture (aspects=["layers"])
STEP 7.6 — get_architecture (aspects=["clusters"])
STEP 7.7 — get_architecture (aspects=["all"])
STEP 8.1 — manage_adr (mode="store", content with all 6 sections: PURPOSE/STACK/ARCHITECTURE/PATTERNS/TRADEOFFS/PHILOSOPHY describing this codebase)
STEP 8.2 — manage_adr (mode="get")
STEP 8.3 — manage_adr (mode="update", sections={"TRADEOFFS": "Updated tradeoffs: SQLite WAL gives good read concurrency. 200-row Cypher cap prevents runaway queries."})
       then manage_adr (mode="get") to verify
STEP 9.1 — detect_changes (scope="unstaged")
STEP 9.2 — detect_changes (scope="branch", base_branch="main", depth=2)
STEP 10.1 — delete_project (project="this-project-does-not-exist")
STEP 10.2 — list_projects

After all steps, write a final summary to the log with:
- PASS/FAIL table for every step
- Total node and edge counts from step 2.1
- Any errors or unexpected outputs
- Log file path

Print the log file path at the end.
```

---

## Log Format Reference

Each log entry follows this structure:

```
=== STEP N.N: <tool_name> (<params>) === 2026-03-10T19:45:00+00:00
INPUT: { ... }
OUTPUT: { ... }
VERDICT: PASS | FAIL — <one-line reason if FAIL>
---
```

---

## Expected Baseline Metrics

After full indexing of this repo, approximate expected values:

| Metric | Expected Range |
|--------|---------------|
| Function nodes | 1,000 – 3,000 |
| Method nodes | 500 – 1,500 |
| Class/Struct nodes | 100 – 400 |
| Module nodes | 50 – 200 |
| CALLS edges | 2,000 – 8,000 |
| IMPORTS edges | 200 – 800 |
| IMPLEMENTS edges | 50 – 300 |
| USAGE edges | 100 – 500 |
| HTTP_CALLS edges | 0 (no HTTP server in this codebase) |
| Fast mode re-index time | < 5 seconds |
| Full index time | 30 – 120 seconds |

Deviations outside these ranges should be flagged as potential extraction gaps.

---

## Troubleshooting

**MCP server not connected:**
```
claude mcp list
# If missing, restart Claude Code — the server was registered to ~/.claude.json
```

**index_repository returns error:**
```bash
# Test CLI mode directly:
codebase-memory-mcp cli index_repository '{"repo_path":"/home/oscar_wu/codebase-memory-mcp","mode":"fast"}'
```

**query_graph returns 0 rows when expecting results:**
- The 200-row cap applies before aggregation — use LIMIT explicitly
- Qualified names are project-scoped; omit project prefix in WHERE clauses
- Use `search_graph` for simple name lookups instead of Cypher

**Log file location:**
```bash
ls -la ~/codebase-memory-mcp-runbook-*.log
```
