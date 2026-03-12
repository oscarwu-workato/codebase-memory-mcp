# Review Scope

## Target

`perf/week1-week2` branch of `oscarwu-workato/codebase-memory-mcp` — all commits above the `week1-complete` tag. This is a performance optimization PR for a Go MCP server that builds semantic code graphs from source repositories.

## Week 1 changes (e7b66ad–91d1372)
- `internal/store/bulk.go` — 3 new partial SQL indexes
- `internal/store/store.go` — OpenReadOnly(), community_cache migration, 3 partial indexes in initSchema
- `internal/store/router.go` — ForProjectReadOnly()
- `internal/store/architecture.go` — archLayers(project, cached) signature, avoids double archBoundaries call
- `internal/tools/tools.go` — Server arch cache (map[string]string), invalidateArchCache(), bumpGraphVersion()
- `internal/tools/architecture.go` — cache read/write around handleGetArchitecture
- `internal/store/louvain.go` — louvainWithWarmStart, fraction-based early-exit
- `internal/pipeline/pipeline.go` — changedFilesAffectCommunities, parallel passTests+passHTTPLinks, skip leaf community rebuild
- `internal/store/search.go` — extractLikeHints fast paths, computeSQLLimit cap at 50K
- `internal/pipeline/resolver.go` — FunctionRegistry.failedLookups cache

## Week 2 changes (da0d338–97614c2)
- `internal/watcher/watcher.go` — fsnotify rewrite (inotify/kqueue + 100ms debounce + 30s fallback, project list cache)
- `internal/store/community_cache.go` — SaveCommunityCache/LoadCommunityCache (batched INSERTs, single query)
- `internal/store/louvain.go` — louvainWithWarmStart warm-start init
- `internal/pipeline/communities.go` — communityGraphHash with topology checksum, passCommunities wiring
- `go.mod/go.sum` — github.com/fsnotify/fsnotify v1.9.0

## Test files
- `internal/store/community_cache_test.go` (11 tests)
- `internal/store/perf_louvain_test.go` (+4 tests)
- `internal/store/perf_store_test.go` (+multiple tests)
- `internal/pipeline/communities_test.go` (8 tests)
- `internal/watcher/watcher_test.go` (+8 tests)
- `internal/tools/perf_arch_cache_test.go`
- `internal/pipeline/perf_pipeline_test.go`
- `internal/pipeline/perf_resolver_test.go`

## Flags
- Security Focus: no
- Performance Critical: yes
- Strict Mode: no
- Framework: Go 1.26, SQLite (mattn/go-sqlite3 CGO), github.com/fsnotify/fsnotify
