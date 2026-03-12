# Phase 3-4: Testing, Best Practices & CI/CD Review

## Critical Test Gaps
- **T-SEC-1 (High)**: `validateProjectName` has zero tests — no rejection of traversal payloads
- **T-PERF-1 (Medium)**: `idx_community_cache_lookup` not verified in any test
- **T-CANCEL-1 (Medium)**: `SaveCommunityCache` cancelled context rollback untested
- **T-WARM-1 (Medium)**: `TestPassCommunitiesWarmStart` doesn't assert cache was hit

## Code Bugs
- **BP-2a (Medium)**: Dead `errors.Is(err, sql.ErrNoRows)` after `QueryContext` in `LoadCommunityCache` — `QueryContext` never returns `ErrNoRows`; silently masks real errors
- **BP-3a (Medium)**: `triggerDebounced` `t.Stop()+t.Reset()` race on AfterFunc timer (also H-1)
- **BP-3b (Low)**: `pollInterval` is dead production code (only called by its own test)

## Go/Module Issues
- **BP-5 (Medium)**: `fsnotify` in `go.mod` marked `// indirect` but directly imported — run `go mod tidy`
- **BP-3c (Low)**: `captureSnapshot` ignores caller context, uses `context.Background()`

## CI/CD Issues
- **CI-1 (High)**: No `-race` flag in any CI test run or Makefile `test` target
- **CI-2 (High)**: All GitHub Actions pinned to mutable version tags, not SHA hashes
- **CI-3 (Medium)**: `release.yml` `git push --force` on release tag can corrupt module checksums
- **CI-4 (Medium)**: `contents: write` granted to all jobs, should scope to `release` job only
- **CI-5 (Medium)**: `gosec` exclusions lack justification comments
