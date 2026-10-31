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

func TestJournalWriterLockNameUsesRawNameAtTheLimit(t *testing.T) {
	dbName := strings.Repeat("a", journalWriterLockNameMaxLength-len(journalWriterLockPrefix))
	want := journalWriterLockPrefix + dbName
	if len(want) != journalWriterLockNameMaxLength {
		t.Fatalf("raw lock name length = %d, want exactly %d", len(want), journalWriterLockNameMaxLength)
	}
	if got := JournalWriterLockName(dbName); got != want {
		t.Fatalf("JournalWriterLockName() = %q, want the raw name %q at exactly %d bytes", got, want, journalWriterLockNameMaxLength)
	}
}

func TestJournalWriterLockNameHashesOneByteOverTheLimit(t *testing.T) {
	dbName := strings.Repeat("a", journalWriterLockNameMaxLength-len(journalWriterLockPrefix)+1)
	raw := journalWriterLockPrefix + dbName
	if len(raw) != journalWriterLockNameMaxLength+1 {
		t.Fatalf("raw lock name length = %d, want exactly %d", len(raw), journalWriterLockNameMaxLength+1)
	}
	got := JournalWriterLockName(dbName)
	if got == raw {
		t.Fatalf("JournalWriterLockName() used the %d-byte raw name %q", len(raw), got)
	}
	if len(got) > journalWriterLockNameMaxLength || !strings.HasPrefix(got, journalWriterLockPrefix) {
		t.Fatalf("JournalWriterLockName() = %q, want a hashed name with prefix %q within %d bytes", got, journalWriterLockPrefix, journalWriterLockNameMaxLength)
	}
}
