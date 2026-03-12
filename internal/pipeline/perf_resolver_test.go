package pipeline

import (
	"sync"
	"testing"
)

// TestFailedLookupsCache verifies that resolving an unknown name populates
// the failedLookups cache so subsequent calls skip the scan.
func TestFailedLookupsCache(t *testing.T) {
	r := NewFunctionRegistry()

	// "unknownFunc" is not registered, so Resolve must fall through to
	// resolveViaNameLookup which caches the miss.
	result := r.Resolve("unknownFunc", "proj.mod", nil)
	if result.QualifiedName != "" {
		t.Errorf("expected empty result for unknown name, got %q", result.QualifiedName)
	}

	r.failedMu.RLock()
	cached := r.failedLookups["unknownFunc"]
	size := len(r.failedLookups)
	r.failedMu.RUnlock()

	if !cached {
		t.Error("expected failedLookups[\"unknownFunc\"] == true after failed resolve")
	}
	if size != 1 {
		t.Errorf("expected failedLookups length 1, got %d", size)
	}
}

// TestFailedLookupsNotCachedOnSuccess verifies that a successful resolution
// does not add the name to the failedLookups cache.
func TestFailedLookupsNotCachedOnSuccess(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("KnownFunc", "proj.pkg.KnownFunc", "Function")

	// Resolve using the unique-name strategy (single candidate project-wide).
	result := r.Resolve("KnownFunc", "proj.other", nil)
	if result.QualifiedName != "proj.pkg.KnownFunc" {
		t.Errorf("expected proj.pkg.KnownFunc, got %q", result.QualifiedName)
	}

	r.failedMu.RLock()
	size := len(r.failedLookups)
	r.failedMu.RUnlock()

	if size != 0 {
		t.Errorf("expected empty failedLookups after successful resolve, got %d entries", size)
	}
}

// TestFailedLookupsConcurrent verifies that concurrent Resolve calls for an
// unknown name are race-free. Run with -race to catch data races.
func TestFailedLookupsConcurrent(t *testing.T) {
	r := NewFunctionRegistry()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			res := r.Resolve("unknownConcurrent", "proj.mod", nil)
			// Result must always be empty (name is never registered).
			if res.QualifiedName != "" {
				t.Errorf("unexpected non-empty result: %q", res.QualifiedName)
			}
		}()
	}
	wg.Wait()

	// After all goroutines finish, the miss must be cached exactly once.
	r.failedMu.RLock()
	cached := r.failedLookups["unknownConcurrent"]
	r.failedMu.RUnlock()

	if !cached {
		t.Error("expected failedLookups[\"unknownConcurrent\"] == true after concurrent resolves")
	}
}

// TestFailedLookupsNotPopulatedByFuzzyResolve verifies that FuzzyResolve does
// not write to failedLookups — it is a separate fallback path with no caching.
func TestFailedLookupsNotPopulatedByFuzzyResolve(t *testing.T) {
	r := NewFunctionRegistry()

	// FuzzyResolve an unknown name — should return false and not cache the miss.
	_, ok := r.FuzzyResolve("totallyUnknown", "proj.mod", nil)
	if ok {
		t.Error("expected FuzzyResolve to return false for unknown name")
	}

	r.failedMu.RLock()
	size := len(r.failedLookups)
	r.failedMu.RUnlock()

	if size != 0 {
		t.Errorf("expected failedLookups to remain empty after FuzzyResolve, got %d entries", size)
	}
}
