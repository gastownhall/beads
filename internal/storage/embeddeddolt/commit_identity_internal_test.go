//go:build cgo

package embeddeddolt

import "testing"

// TestSetCommitIdentity pins GH#7628's resolution contract directly against
// the package-level state buildDSN and commitAuthor read, without needing a
// real embedded database: SetCommitIdentity overrides both, an empty name or
// email is a no-op (never adopting a half-resolved identity), and
// commitAuthor composes the current pair fresh on every call.
func TestSetCommitIdentity(t *testing.T) {
	t.Cleanup(func() { SetCommitIdentity("beads", "beads@local") })

	if got := commitAuthor(); got != "beads <beads@local>" {
		t.Fatalf("commitAuthor() before any override = %q, want the historical beads <beads@local>", got)
	}

	SetCommitIdentity("Alice Author", "alice@example.com")
	if got := commitAuthor(); got != "Alice Author <alice@example.com>" {
		t.Fatalf("commitAuthor() after SetCommitIdentity = %q, want %q", got, "Alice Author <alice@example.com>")
	}
	if defaultAuthorName != "Alice Author" || defaultAuthorEmail != "alice@example.com" {
		t.Fatalf("defaultAuthorName/Email = %q/%q, want the overridden pair", defaultAuthorName, defaultAuthorEmail)
	}

	SetCommitIdentity("", "bob@example.com")
	if got := commitAuthor(); got != "Alice Author <alice@example.com>" {
		t.Fatalf("commitAuthor() after an empty-name call = %q, want the prior override unchanged (empty name must no-op)", got)
	}

	SetCommitIdentity("Bob Author", "")
	if got := commitAuthor(); got != "Alice Author <alice@example.com>" {
		t.Fatalf("commitAuthor() after an empty-email call = %q, want the prior override unchanged (empty email must no-op)", got)
	}

	SetCommitIdentity("Carol Author", "carol@example.com")
	if got := commitAuthor(); got != "Carol Author <carol@example.com>" {
		t.Fatalf("commitAuthor() after a second full override = %q, want %q", got, "Carol Author <carol@example.com>")
	}
}
