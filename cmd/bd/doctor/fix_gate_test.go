package doctor

import (
	"strings"
	"testing"
)

func TestSanitizeFixRecommendation_SafePassesThrough(t *testing.T) {
	gate := FixGate{Safe: true}
	in := "Run 'bd doctor --fix' to untrack runtime files"
	if got := SanitizeFixRecommendation(in, gate); got != in {
		t.Fatalf("safe gate should pass through, got %q", got)
	}
}

func TestSanitizeFixRecommendation_AheadBlocksTip(t *testing.T) {
	gate := FixGate{
		Safe:          false,
		Ahead:         true,
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
		Safe:          false,
		Pending:       true,
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
	gate := FixGate{Safe: false, Ahead: true, Reason: "skew"}
	in := "Run 'bd bootstrap' as recovery"
	if got := SanitizeFixRecommendation(in, gate); got != in {
		t.Fatalf("non --fix tip should pass through, got %q", got)
	}
}

func TestFixGateRefuseDoctorFix(t *testing.T) {
	if (FixGate{Safe: false, Ahead: true}).RefuseDoctorFix() != true {
		t.Fatal("ahead should refuse")
	}
	if (FixGate{Safe: false, Pending: true}).RefuseDoctorFix() != false {
		t.Fatal("pending alone should not hard-refuse apply (filesystem fixes ok)")
	}
	if (FixGate{Safe: true}).RefuseDoctorFix() != false {
		t.Fatal("safe should not refuse")
	}
}
