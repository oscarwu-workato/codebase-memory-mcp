# Codebase Analysis Playbook

How to use `codebase-memory-mcp` to analyze any project from Claude Code.
Based on validated findings from `REPORT.md` (2026-03-10 dogfood run).

**Prerequisites:**
- MCP server connected (`/mcp` shows `codebase-memory-mcp` as connected)
- Project cloned locally with an absolute path you can provide
- ~60–120s for initial indexing (depending on repo size)

---

## Step 0 — Orient before indexing

Before touching the MCP tools, answer these two questions:

1. **Does the repo have large vendored or generated directories?**
   (e.g. `vendor/`, `node_modules/`, `dist/`, bundled C grammars, protobuf generated code)
   If yes, note those paths — you will exclude them from searches with `file_pattern`.

2. **What is the absolute path to the repo on disk?**
   ```bash
   echo $(pwd)   # run from inside the repo
   ```

---

## Step 1 — Index the project

```
Index the project at <ABSOLUTE_PATH> using the codebase-memory-mcp MCP tool index_repository.
Use mode="fast" for large repos (>50K files), mode="full" for everything else.
Report back: total nodes, total edges, any errors.
```

**What to expect:**
- Full mode: 30–120s depending on repo size
- Fast mode: skips generated code, docs, files >512KB — use for monorepos
- If the repo was already indexed (prior session), this is an incremental update

**Check status after:**
```
Call index_status using the codebase-memory-mcp MCP tool.
Report: status (ready/indexing), node count, edge count, last indexed timestamp.
```

> **Watch out:** If node count seems low (e.g. a 200K-line Go project returns <500 Function
> nodes), the indexer may have hit parsing failures. Try `mode="full"` if you used `fast`.

---

## Step 2 — Get your bearings (5 minutes)

Run these three queries in sequence. They give you a complete orientation with minimal tokens.

### 2a. Schema — what's in the graph

```
Call get_graph_schema using the codebase-memory-mcp MCP tool.
Summarize:
1. Top 5 node labels by count
2. Top 5 edge types by count
3. Any zero-count edge types that seem unexpected (flag CALLS=0 as a red flag)
4. Rough ratio of Function to Method nodes
```

**Red flags to watch for:**
- `CALLS = 0` — call resolution failed entirely; graph is not useful for tracing
- `Function` count wildly high — likely vendored code inflating numbers (see §REPORT.md §5.3)
- `IMPLEMENTS = 0` on a Java/Go/TypeScript repo — interface tracking may be incomplete

### 2b. Language and architecture snapshot

```
Call get_architecture with aspects=["languages", "entry_points", "layers"]
using the codebase-memory-mcp MCP tool.
Report:
- Which languages are present and their file counts
- What are the entry points (main functions, route handlers)?
- What layer structure was detected (entry → internal → core)?
```

### 2c. Hotspots — the most-called functions

```
Call get_architecture with aspects=["hotspots"]
using the codebase-memory-mcp MCP tool.
List the top 10 functions by inbound call count.
Are these the functions you'd expect to be central? Note any surprises.
```

> **Note:** Test setup/teardown functions often dominate hotspot lists because tests call
> them repeatedly. Look for non-test hotspots for true production centrality.

---

## Step 3 — Understand the architecture

### 3a. Package dependencies (use boundaries, not packages)

```
Call get_architecture with aspects=["boundaries"]
using the codebase-memory-mcp MCP tool.
Report: which packages call which other packages, and with what volume?
Identify the top 3 cross-package dependencies by call count.
```

> **Known bug:** `aspects=["packages"]` returns fan_in=0/fan_out=0 for all packages.
> Always use `aspects=["boundaries"]` for cross-package dependency data. (REPORT.md BUG-001)

### 3b. Community clusters

```
Call get_architecture with aspects=["clusters"]
using the codebase-memory-mcp MCP tool.
How many clusters were detected? Do they map to the directory structure,
or do they reveal hidden functional groupings that cut across directories?
```

Clusters that don't match directory structure are interesting — they reveal functional
cohesion the folder layout doesn't show.

### 3c. Full snapshot (optional — verbose)

```
Call get_architecture with aspects=["all"]
using the codebase-memory-mcp MCP tool.
Summarize all aspects. Flag any that returned empty results.
```

---

## Step 4 — Find what you're looking for

### 4a. Find functions by name

```
Call search_graph with name_pattern="<PATTERN>" and limit=20
using the codebase-memory-mcp MCP tool.
List all matches with their file paths and inbound/outbound degree.
```

Tips:
- Use regex alternatives: `"auth|authenticate|login"` instead of three separate searches
- Add `label="Function"` or `label="Method"` to narrow results
- Add `file_pattern="**/services/**"` to scope to a directory
- Add `sort_by="degree"` to surface the most-connected matches first

> **Remember:** Go receiver methods are label="Method", not "Function". If you expect a
> result and don't find it, try omitting the label filter.

### 4b. Find functions in a specific file or directory

```
Call search_graph with file_pattern="**/payments/**" and label="Function" and limit=30
using the codebase-memory-mcp MCP tool.
List all functions. How many are there? Which have the highest degree?
```

### 4c. Text search (for strings, error messages, config values)

```
Call search_code with pattern="<LITERAL_OR_REGEX>" and max_results=15
using the codebase-memory-mcp MCP tool.
Report matching lines, file paths, and line numbers.
```

Use `regex=true` for patterns like `"func.*Handler"` or `"TODO|FIXME"`.
Use `file_pattern="*.go"` to scope to a language.

---

## Step 5 — Trace dependencies

This is the highest-value workflow. Use it when you want to understand how a function
fits into the system.

### 5a. Who calls this function? (inbound trace)

```
Call trace_call_path with function_name="<FUNCTION>" and direction="inbound"
and depth=2 and risk_labels=true
using the codebase-memory-mcp MCP tool.
Report: all hop-1 callers (CRITICAL risk), hop-2 callers (HIGH risk).
Are the callers in expected packages or crossing surprising boundaries?
```

### 5b. What does this function call? (outbound trace)

```
Call trace_call_path with function_name="<FUNCTION>" and direction="outbound"
and depth=3
using the codebase-memory-mcp MCP tool.
Report: the call tree. Do you see any unexpected external calls or deeply nested chains?
```

### 5c. Full neighborhood (both directions)

```
Call trace_call_path with function_name="<FUNCTION>" and direction="both"
and depth=2
using the codebase-memory-mcp MCP tool.
Report: total edges, inbound callers, outbound callees.
```

> **If the function isn't found:** The tool returns suggestions. Check the suggestion list —
> the function may be a Method not a Function, or have a slightly different name. Use
> `search_graph(name_pattern="<NAME>")` to find the exact name first.

> **Confidence note:** Edges with confidence <0.45 are speculative fuzzy matches. Use
> `min_confidence=0.7` to see only high-confidence direct calls when you want precision.

---

## Step 6 — Read the source

Once you've found a symbol via search or tracing:

```
Call get_code_snippet with qualified_name="<QUALIFIED_NAME>" and include_neighbors=true
using the codebase-memory-mcp MCP tool.
Report: source code, signature, return type, complexity score, caller/callee counts.
```

Tips:
- Use the `qualified_name` from search_graph results for precision (avoids ambiguity)
- If you only have the short name and get multiple candidates, add the package path as
  a suffix: e.g. `"store.UpsertNode"` instead of just `"UpsertNode"`
- `auto_resolve=true` picks the best match when ≤2 candidates exist

---

## Step 7 — Detect dead code

```
Call search_graph with label="Function" and relationship="CALLS"
and direction="inbound" and max_degree=0 and exclude_entry_points=true
and limit=30
using the codebase-memory-mcp MCP tool.
List any functions with zero inbound calls. These are dead code candidates.
```

> **Critical:** You MUST set `relationship="CALLS"` and `direction="inbound"`.
> Without these, `max_degree=0` counts all edge types including DEFINES (containment),
> and every function has at least one DEFINES edge — so you get 0 results. (REPORT.md §4.3)

Follow up on each candidate:
```
Call get_code_snippet with qualified_name="<CANDIDATE>" using the codebase-memory-mcp MCP tool.
Is this actually unreachable, or is it called via reflection/interface/config?
```

---

## Step 8 — Assess change impact

Before or after making edits, run this to understand blast radius:

### 8a. Unstaged changes (work in progress)

```
Call detect_changes with scope="unstaged"
using the codebase-memory-mcp MCP tool.
What symbols changed? What is the blast radius (CRITICAL/HIGH/MEDIUM/LOW)?
```

### 8b. Branch vs main (before a PR)

```
Call detect_changes with scope="branch" and base_branch="main" and depth=2
using the codebase-memory-mcp MCP tool.
Report: changed files, changed symbols, and all impacted symbols with risk levels.
Are any CRITICAL-risk callers in unexpected packages?
```

---

## Step 9 — Store architectural knowledge (ADR)

Once you've explored the codebase, capture what you've learned so future sessions
(and other agents) can orient quickly:

```
Call manage_adr with mode="store" and content containing all 6 sections:

## PURPOSE
<what this system does in 2-3 sentences>

## STACK
<languages, frameworks, key dependencies, runtime>

## ARCHITECTURE
<structural pattern: layers, services, key flows>

## PATTERNS
<coding conventions, design patterns used, data flow>

## TRADEOFFS
<what decisions were made and why, known limitations>

## PHILOSOPHY
<guiding principles, what to preserve>

Use the codebase-memory-mcp MCP tool.
```

Once stored, any future Claude Code session working in this repo can retrieve the ADR
with `manage_adr(mode="get")` and immediately have architectural context without re-exploring.

---

## Quick reference: which tool for which question

| Question | Tool | Key params |
|----------|------|------------|
| What's in the graph? | `get_graph_schema` | — |
| What languages/layers/hotspots? | `get_architecture` | `aspects=[...]` |
| Which packages depend on which? | `get_architecture` | `aspects=["boundaries"]` ⚠️ not "packages" |
| Find a function by name | `search_graph` | `name_pattern`, `label` |
| Find functions in a directory | `search_graph` | `file_pattern` |
| Find dead code | `search_graph` | `relationship="CALLS"`, `direction="inbound"`, `max_degree=0` |
| Find most-connected functions | `search_graph` | `sort_by="degree"` |
| Search source text | `search_code` | `pattern`, `regex`, `file_pattern` |
| Who calls X? | `trace_call_path` | `direction="inbound"`, `risk_labels=true` |
| What does X call? | `trace_call_path` | `direction="outbound"` |
| Read source code | `get_code_snippet` | `qualified_name`, `include_neighbors=true` |
| What does this change break? | `detect_changes` | `scope="branch"`, `base_branch="main"` |
| Store/retrieve architecture notes | `manage_adr` | `mode="store"/"get"/"update"` |

---

## Known gotchas (from REPORT.md)

**Vendored/generated code noise**
If your repo has vendored dependencies or generated files, Function counts will be inflated
and degree-sorted searches will surface those files first. Add `file_pattern` to every
search that should scope to application code only.

**Method vs Function label**
Go receiver methods, Python class methods, and similar constructs are indexed as
label="Method" not label="Function". If a search with `label="Function"` returns nothing,
try without the label filter or with `label="Method"`.

**Short name collisions with markdown**
If your repo has markdown docs indexed alongside code, section headings can collide with
function names in short-name lookups. Always prefer using the qualified name from
`search_graph` results when calling `get_code_snippet` or `trace_call_path`.

**`query_graph COUNT` is broken for large graphs**
Never use `MATCH (n) RETURN COUNT(n)` style queries for counts. The 200-row pre-aggregation
cap returns wildly wrong numbers. Use `get_graph_schema` for counts.

**`p=` path variable syntax unsupported**
Write `MATCH (a)-[:CALLS]->(b)` not `MATCH p=(a)-[:CALLS]->(b)`.

---

## Single-paste orientation prompt

Paste this into Claude Code when starting analysis on a new project:

```
I want to analyze the codebase at <ABSOLUTE_PATH> using the codebase-memory-mcp MCP server.

Run the following steps in order and summarize findings at each stage:

1. Index the repository: call index_repository with repo_path="<ABSOLUTE_PATH>" and mode="full".
   Report node count, edge count, any errors.

2. Get schema: call get_graph_schema.
   Report top 5 node labels and edge types. Flag CALLS=0 as a red flag.

3. Get architecture overview: call get_architecture with aspects=["languages","entry_points","layers","hotspots"].
   Summarize: languages present, entry points, layer structure, top 5 hotspot functions.

4. Get cross-package dependencies: call get_architecture with aspects=["boundaries"].
   List top 5 cross-package call relationships by volume.

5. Get clusters: call get_architecture with aspects=["clusters"].
   How many clusters? Do they match directory structure?

6. Find the 20 most-connected functions: call search_graph with sort_by="degree" and limit=20.
   List them with file paths.

After all steps, give me:
- A one-paragraph plain-English summary of what this codebase does and how it's structured
- The 3 most important functions/classes to understand to grok the system
- Any red flags (CALLS=0, unexpectedly low node counts, surprising layer violations)
```
