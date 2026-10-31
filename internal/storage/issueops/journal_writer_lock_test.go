package issueops

import (
	"strings"
	"testing"
)

func TestJournalWriterLockNameUsesRawNameWhenBounded(t *testing.T) {
	got := JournalWriterLockName("proj_a")
	want := journalWriterLockPrefix + "proj_a"
	if got != want {
		t.Fatalf("JournalWriterLockName() = %q, want %q", got, want)
	}
}

func TestJournalWriterLockNameHashesLongNames(t *testing.T) {
	dbName := strings.Repeat("a", 64)
	got := JournalWriterLockName(dbName)
	if len(got) > journalWriterLockNameMaxLength {
		t.Fatalf("JournalWriterLockName() length = %d, want <= %d", len(got), journalWriterLockNameMaxLength)
	}
	if got == journalWriterLockPrefix+dbName {
		t.Fatalf("JournalWriterLockName() used over-limit raw name %q", got)
	}
	if !strings.HasPrefix(got, journalWriterLockPrefix) {
		t.Fatalf("JournalWriterLockName() = %q, want prefix %q", got, journalWriterLockPrefix)
	}
	if got != JournalWriterLockName(dbName) {
		t.Fatal("JournalWriterLockName() is not deterministic")
	}
	if got == JournalWriterLockName(strings.Repeat("b", 64)) {
		t.Fatal("JournalWriterLockName() maps two long databases to one lock")
	}
}
