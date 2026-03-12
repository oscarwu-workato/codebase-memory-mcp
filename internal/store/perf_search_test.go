package store

import (
	"fmt"
	"strings"
	"testing"
)

// --- extractLikeHints tests ---

func TestExtractLikeHintsDotStarPattern(t *testing.T) {
	got := extractLikeHints(".*Submit.*")
	want := []string{"Submit"}
	if len(got) != len(want) || (len(want) > 0 && got[0] != want[0]) {
		t.Errorf("extractLikeHints(%q) = %v, want %v", ".*Submit.*", got, want)
	}
}

func TestExtractLikeHintsPlainLiteral(t *testing.T) {
	got := extractLikeHints("handler")
	want := []string{"handler"}
	if len(got) != len(want) || (len(want) > 0 && got[0] != want[0]) {
		t.Errorf("extractLikeHints(%q) = %v, want %v", "handler", got, want)
	}
}

func TestExtractLikeHintsTooShort(t *testing.T) {
	// "ab" is only 2 chars — below the 3-char threshold.
	got := extractLikeHints(".*ab.*")
	if len(got) != 0 {
		t.Errorf("extractLikeHints(%q) = %v, want nil", ".*ab.*", got)
	}
}

func TestExtractLikeHintsAlternation(t *testing.T) {
	// Alternation → bail out.
	got := extractLikeHints("foo|bar")
	if len(got) != 0 {
		t.Errorf("extractLikeHints(%q) = %v, want nil (alternation)", "foo|bar", got)
	}
}

func TestExtractLikeHintsMetacharInMiddle(t *testing.T) {
	// ".*foo.*bar.*" — the fast path strips leading and trailing .* leaving
	// "foo.*bar" which contains a metachar, so the fast path does not apply.
	// The character walk should extract both "foo" and "bar" (each >= 3 chars).
	got := extractLikeHints(".*foo.*bar.*")
	if len(got) == 0 {
		t.Errorf("extractLikeHints(%q) = nil, expected at least one hint (foo or bar)", ".*foo.*bar.*")
	}
	// Verify that "foo" and "bar" are in the result.
	hasHint := func(hints []string, target string) bool {
		for _, h := range hints {
			if strings.Contains(h, target) || h == target {
				return true
			}
		}
		return false
	}
	if !hasHint(got, "foo") && !hasHint(got, "bar") {
		t.Errorf("extractLikeHints(%q) = %v, expected foo or bar in hints", ".*foo.*bar.*", got)
	}
}

// --- computeSQLLimit tests ---

func TestComputeSQLLimitWithLikeHints(t *testing.T) {
	params := &SearchParams{
		MinDegree: -1,
		MaxDegree: -1,
		Offset:    0,
		Limit:     10,
	}
	// nameHasLikeHints=true, qnHasLikeHints=false.
	// Expected: offset(0) + limit(10) + 5000 = 5010.
	got := computeSQLLimit(params, true, false)
	if got != 5010 {
		t.Errorf("computeSQLLimit = %d, want 5010", got)
	}
}

func TestComputeSQLLimitWithLikeHintsCapped(t *testing.T) {
	params := &SearchParams{
		MinDegree: -1,
		MaxDegree: -1,
		Offset:    44999,
		Limit:     10,
	}
	// offset(44999) + limit(10) + 5000 = 50009 → capped at 50000.
	got := computeSQLLimit(params, true, false)
	if got != 50000 {
		t.Errorf("computeSQLLimit (capped) = %d, want 50000", got)
	}
}

func TestComputeSQLLimitQNOnlyHint(t *testing.T) {
	// qnHasLikeHints=true but nameHasLikeHints=false.
	// The name pattern has no LIKE hints → needsNameScan=true (if QNPattern is set
	// with no hints, that triggers the full scan path too). Actually: the logic is
	// needsNameScan = (NamePattern != "" && !nameHasLikeHints) || (QNPattern != "" && !qnHasLikeHints).
	// Here QNPattern="" so no scan needed from QN side; NamePattern="" no scan needed.
	// nameHasLikeHints=false and no NamePattern → needsNameScan=false.
	// qnHasLikeHints=true means QN column has hints — but the limit path depends on
	// nameHasLikeHints only (qnHasLikeHints doesn't trigger the LIKE cap path).
	// So we fall through to the +1000 buffer path.
	params := &SearchParams{
		MinDegree: -1,
		MaxDegree: -1,
		Offset:    0,
		Limit:     10,
	}
	// nameHasLikeHints=false, qnHasLikeHints=true.
	// needsNameScan = false (no NamePattern), hasDegreeFilter=false.
	// Falls to sqlLimit = offset(0) + limit(10) + 1000 = 1010.
	got := computeSQLLimit(params, false, true)
	if got != 1010 {
		t.Errorf("computeSQLLimit (qn hint only) = %d, want 1010", got)
	}
}

func TestComputeSQLLimitDegreeFilter(t *testing.T) {
	// hasDegreeFilter via MinDegree=5, nameHasLikeHints=true.
	// Even though we have LIKE hints, the degree filter takes precedence → 200000.
	params := &SearchParams{
		MinDegree: 5,
		MaxDegree: -1,
		Offset:    0,
		Limit:     10,
	}
	got := computeSQLLimit(params, true, false)
	if got != 200000 {
		t.Errorf("computeSQLLimit (degree filter) = %d, want 200000", got)
	}
}

// --- Search end-to-end LIKE hint pre-filter test ---

func TestSearchLikeHintPreFilter(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	// Seed 100 nodes named "foo_N".
	for i := 0; i < 100; i++ {
		_, err := s.UpsertNode(&Node{
			Project:       "test",
			Label:         "Function",
			Name:          fmt.Sprintf("foo_%d", i),
			QualifiedName: fmt.Sprintf("test.pkg.foo_%d", i),
			FilePath:      "pkg.go",
		})
		if err != nil {
			t.Fatalf("UpsertNode foo_%d: %v", i, err)
		}
	}

	// Seed 1 node named "Submit_handler".
	_, err = s.UpsertNode(&Node{
		Project:       "test",
		Label:         "Function",
		Name:          "Submit_handler",
		QualifiedName: "test.pkg.Submit_handler",
		FilePath:      "handler.go",
	})
	if err != nil {
		t.Fatalf("UpsertNode Submit_handler: %v", err)
	}

	// Search with NamePattern that should only match "Submit_handler".
	output, err := s.Search(&SearchParams{
		Project:     "test",
		NamePattern: ".*Submit.*",
		MinDegree:   -1,
		MaxDegree:   -1,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(output.Results) != 1 {
		t.Fatalf("expected exactly 1 result, got %d", len(output.Results))
	}
	if !strings.Contains(output.Results[0].Node.Name, "Submit") {
		t.Errorf("result name %q does not contain 'Submit'", output.Results[0].Node.Name)
	}
}
