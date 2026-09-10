package migration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsFrozen(t *testing.T) {
	dir := t.TempDir()

	if IsFrozen(dir) {
		t.Fatalf("IsFrozen(%q) = true before sentinel written, want false", dir)
	}

	if err := os.WriteFile(FilePath(dir), []byte("mayor\t2026-08-16T12:00:00Z\tdolt v2 migration\n"), 0644); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	}

	if !IsFrozen(dir) {
		t.Fatalf("IsFrozen(%q) = false after sentinel written, want true", dir)
	}
}

func TestIsFrozenEmptyTownRoot(t *testing.T) {
	if IsFrozen("") {
		t.Fatalf("IsFrozen(\"\") = true, want false (no town root to check)")
	}
}

func TestReadParsesOperatorReasonTimestamp(t *testing.T) {
	dir := t.TempDir()
	ts := "2026-08-16T12:00:00Z"
	if err := os.WriteFile(FilePath(dir), []byte("mayor\t"+ts+"\tdolt v2 migration\n"), 0644); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	}

	info := Read(dir)
	if info == nil {
		t.Fatalf("Read(%q) = nil, want parsed Info", dir)
	}
	if info.Operator != "mayor" {
		t.Errorf("Operator = %q, want %q", info.Operator, "mayor")
	}
	if info.Reason != "dolt v2 migration" {
		t.Errorf("Reason = %q, want %q", info.Reason, "dolt v2 migration")
	}
	wantTS, _ := time.Parse(time.RFC3339, ts)
	if !info.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", info.Timestamp, wantTS)
	}
}

func TestReadNotFrozenReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if info := Read(dir); info != nil {
		t.Fatalf("Read(%q) = %+v, want nil (no sentinel present)", dir, info)
	}
}

func TestReadEmptyContentDoesNotFabricateTimestamp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(FilePath(dir), []byte(""), 0644); err != nil {
		t.Fatalf("writing empty sentinel: %v", err)
	}

	info := Read(dir)
	if info == nil {
		t.Fatalf("Read(%q) = nil for empty-but-present sentinel, want non-nil Info", dir)
	}
	if !info.Timestamp.IsZero() {
		t.Errorf("Timestamp = %v, want zero value (no recorded timestamp to report — must not fabricate one)", info.Timestamp)
	}
	if info.Operator != "" || info.Reason != "" {
		t.Errorf("Operator/Reason = %q/%q, want both empty for an empty sentinel", info.Operator, info.Reason)
	}
}

func TestReadMalformedContentDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(FilePath(dir), []byte("not tab separated at all"), 0644); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	}
	info := Read(dir)
	if info == nil {
		t.Fatalf("Read(%q) = nil for malformed-but-present sentinel, want non-nil Info with best-effort fields", dir)
	}
	if info.Operator != "not tab separated at all" {
		t.Errorf("Operator = %q, want the whole line back (no tabs to split on)", info.Operator)
	}
}

func TestFilePathJoinsTownRoot(t *testing.T) {
	got := FilePath("/some/town/root")
	want := filepath.Join("/some/town/root", "MIGRATION-FREEZE")
	if got != want {
		t.Errorf("FilePath = %q, want %q", got, want)
	}
}
