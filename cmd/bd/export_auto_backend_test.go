package main

import (
	"context"
	"testing"
	"time"

	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
)

// fakeSeamBackend is the autoExportBackend stand-in for the seam tests: a
// fixed state hash, and a read that records it ran (the tests here assert the
// cycle's ordering decisions — change detection and throttle — which happen
// before any reader is needed, so read never invokes its fn).
type fakeSeamBackend struct {
	hash string

	hashCalls   int
	commitCalls int
	readCalls   int
}

func (f *fakeSeamBackend) stateHash(context.Context) (string, error) {
	f.hashCalls++
	return f.hash, nil
}

func (f *fakeSeamBackend) currentCommit(context.Context) (string, error) {
	f.commitCalls++
	return "anchor-commit", nil
}

func (f *fakeSeamBackend) read(ctx context.Context, fn func(context.Context, autoExportReader) error) error {
	f.readCalls++
	return nil
}

// seamTestForTest wires maybeAutoExport to the fake backend for one test.
func seamBackendForTest(t *testing.T, fake *fakeSeamBackend) {
	t.Helper()
	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)

	old := autoExportBackendForCommand
	t.Cleanup(func() { autoExportBackendForCommand = old })
	autoExportBackendForCommand = func() autoExportBackend { return fake }
}

// TestMaybeAutoExportBackendUnchangedHashSkipsRead pins the cheapest exit on
// the backend seam: a state hash equal to the last export's means no read
// cycle at all. This is the leg that keeps proxied-server workspaces (where
// the hash is working-set aware) from hammering the server after read-only
// commands that do not write.
func TestMaybeAutoExportBackendUnchangedHashSkipsRead(t *testing.T) {
	fake := &fakeSeamBackend{hash: "hash-now"}
	seamBackendForTest(t, fake)

	dir := autoExportTestDir(t)
	saveExportAutoState(filepath.Join(dir, ".beads"), &exportAutoState{
		LastDoltCommit: "hash-now",
		Timestamp:      time.Time{}, // throttle window open
	})

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("maybeAutoExport: %v", err)
	}
	if fake.readCalls != 0 {
		t.Fatalf("read calls = %d, want 0 (unchanged hash must skip the read cycle)", fake.readCalls)
	}
}

// TestMaybeAutoExportBackendThrottleSkipsRead pins the second exit on the
// seam: a moved hash inside the export.interval window skips the read cycle
// (the throttle is a write-rate bound, and the read cycle is the write).
func TestMaybeAutoExportBackendThrottleSkipsRead(t *testing.T) {
	fake := &fakeSeamBackend{hash: "hash-moved"}
	seamBackendForTest(t, fake)
	config.Set("export.interval", time.Hour)

	dir := autoExportTestDir(t)
	saveExportAutoState(filepath.Join(dir, ".beads"), &exportAutoState{
		LastDoltCommit: "hash-before",
		Timestamp:      time.Now(),
	})

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("maybeAutoExport: %v", err)
	}
	if fake.readCalls != 0 {
		t.Fatalf("read calls = %d, want 0 (throttle window must skip the read cycle)", fake.readCalls)
	}
}

// TestMaybeAutoExportBackendReadsWhenHashMovesAndWindowOpen is the positive
// control for the two above: a moved hash with the window open reaches the
// backend's read — the leg every real export (proxied and direct alike)
// rides on.
func TestMaybeAutoExportBackendReadsWhenHashMovesAndWindowOpen(t *testing.T) {
	fake := &fakeSeamBackend{hash: "hash-moved"}
	seamBackendForTest(t, fake)
	config.Set("export.interval", time.Hour)

	dir := autoExportTestDir(t)
	saveExportAutoState(filepath.Join(dir, ".beads"), &exportAutoState{
		LastDoltCommit: "hash-before",
		Timestamp:      time.Time{}, // window open
	})

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("maybeAutoExport: %v", err)
	}
	if fake.readCalls != 1 {
		t.Fatalf("read calls = %d, want 1 (moved hash with the window open must reach the read cycle)", fake.readCalls)
	}
}
