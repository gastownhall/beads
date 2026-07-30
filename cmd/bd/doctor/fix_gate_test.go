package doctor

import (
	"strings"
	"testing"
)

func safeGate() FixGate {
	return FixGate{
		Determined:   true,
		DBReachable:  true,
		RecommendFix: true,
		AllowDBFix:   true,
		AllowFSFix:   true,
	}
}

func TestSanitizeFixRecommendation_SafePassesThrough(t *testing.T) {
	in := "Run 'bd doctor --fix' to untrack runtime files"
	if got := SanitizeFixRecommendation(in, safeGate()); got != in {
		t.Fatalf("safe gate should pass through, got %q", got)
	}
}

func TestSanitizeFixRecommendation_AheadBlocksTip(t *testing.T) {
	gate := FixGate{
		Determined:    true,
		DBReachable:   true,
		Ahead:         true,
		AllowFSFix:    true,
		DBVersion:     52,
		BinaryVersion: 49,
		Reason:        "database schema is at v52, this binary knows up to v49 (3 migrations ahead)",
	}
	in := "Run 'bd doctor --fix' to untrack, or manually: git rm --cached .beads/redirect"
	got := SanitizeFixRecommendation(in, gate)
	if got == in {
		t.Fatal("expected rewrite when schema is ahead")
	}
	for _, part := range []string{"Do NOT run", "doctor --fix", "Original tip"} {
		if !strings.Contains(got, part) {
			t.Fatalf("rewrite missing %q: %q", part, got)
		}
	}
}

func TestSanitizeFixRecommendation_PendingWarns(t *testing.T) {
	gate := FixGate{
		Determined:    true,
		DBReachable:   true,
		Pending:       true,
		AllowFSFix:    true,
		DBVersion:     48,
		BinaryVersion: 52,
		Reason:        "pending migrations",
	}
	in := "Run: bd doctor --fix"
	got := SanitizeFixRecommendation(in, gate)
	if got == in {
		t.Fatal("expected rewrite when migrations pending")
	}
	for _, part := range []string{"Avoid", "pending", "Original tip"} {
		if !strings.Contains(got, part) {
			t.Fatalf("rewrite missing %q: %q", part, got)
		}
	}
}

func TestSanitizeFixRecommendation_NoDoctorFixUnchanged(t *testing.T) {
	gate := FixGate{Determined: true, DBReachable: true, Ahead: true, Reason: "skew"}
	in := "Run 'bd bootstrap' as recovery"
	if got := SanitizeFixRecommendation(in, gate); got != in {
		t.Fatalf("non --fix tip should pass through, got %q", got)
	}
}

// TestSanitizeFixRecommendation_UndeterminedFailsClosed pins the property that
// "we could not determine the schema version" is not the same as "safe".
func TestSanitizeFixRecommendation_UndeterminedFailsClosed(t *testing.T) {
	gate := FixGate{
		DBReachable: true,
		Determined:  false,
		AllowFSFix:  true,
		Reason:      "database schema version could not be determined",
	}
	in := "Run 'bd doctor --fix' to repair"
	got := SanitizeFixRecommendation(in, gate)
	if got == in {
		t.Fatal("an undetermined schema version must not pass --fix advice through unchanged")
	}
	if !strings.Contains(got, "Do NOT run") {
		t.Fatalf("undetermined gate should refuse, got %q", got)
	}
}

// TestMentionsFixAdvice_ToleratesFlagOrderAndSpacing covers the wordings the
// previous strings.Contains("doctor --fix") sniff silently missed.
func TestMentionsFixAdvice_ToleratesFlagOrderAndSpacing(t *testing.T) {
	shouldMatch := []string{
		"Run 'bd doctor --fix'",
		"bd doctor  --fix",
		"bd doctor --yes --fix",
		"try `bd doctor --fix --verbose` next",
		"Run BD DOCTOR --FIX",
	}
	for _, s := range shouldMatch {
		if !MentionsFixAdvice(s) {
			t.Errorf("expected %q to be recognised as --fix advice", s)
		}
	}

	shouldNotMatch := []string{
		"Run 'bd bootstrap'",
		"see the doctor documentation",
		"pass --fixture to the test runner",
	}
	for _, s := range shouldNotMatch {
		if MentionsFixAdvice(s) {
			t.Errorf("did not expect %q to be treated as --fix advice", s)
		}
	}
}

// TestIsFilesystemOnlyFix_DefaultsToGuarded pins the fail-closed default: a fix
// nobody has classified is treated as database-touching, so a check added later
// is guarded rather than silently escaping the gate.
func TestIsFilesystemOnlyFix_DefaultsToGuarded(t *testing.T) {
	for _, name := range []string{"Gitignore", "Permissions", "Lock Files", "Git Hooks"} {
		if !IsFilesystemOnlyFix(name) {
			t.Errorf("%q should be classified filesystem-only", name)
		}
	}
	for _, name := range []string{
		"Database", "Schema Compatibility", "Blocked State",
		"Some Check Added Next Year", "",
	} {
		if IsFilesystemOnlyFix(name) {
			t.Errorf("%q must default to database-touching (guarded)", name)
		}
	}
}
