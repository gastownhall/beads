package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The AC-5 mechanics of openInitStore (gastownhall/beads#6089): on an
// ELIGIBLE unversioned refusal the helper runs exactly strict / lenient /
// strict, commits exactly once with the fixed message, closes the lenient
// handle before the next open, never mutates the caller's strict intent,
// and stops at the first failure with the dual-wrapped error. The
// eligibility surface itself is pinned in init_store_open_test.go.

// TestOpenInitStoreReconcilesUnversionedRefusal is the AC-5 core: the
// strict/lenient/strict order, the lenient config being an unmutating copy
// of the original, one CommitAll with the fixed message, the lenient handle
// closed before the final open, the final open getting the ORIGINAL strict
// config pointer, and the note describing the refusal's table list.
func TestOpenInitStoreReconcilesUnversionedRefusal(t *testing.T) {
	var events []string
	lenient := &fakeInitStore{events: &events, committed: true}
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: eligibleUnversionedRefusal()},
		{store: lenient},
		{},
	}}
	var notes []string
	original := serverInitConfig()
	original.BeadsDir = "/workspace/.beads"

	store, err := openInitStore(context.Background(), original, opener.open, func(line string) { notes = append(notes, line) })
	if err != nil {
		t.Fatalf("openInitStore: %v", err)
	}
	if store == nil {
		t.Fatal("openInitStore returned a nil store after a successful adoption")
	}
	if want := []string{
		"open:lenient=false",
		"open:lenient=true",
		"commit:" + initUnversionedRecoveryCommitMessage,
		"close",
		"open:lenient=false",
	}; strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if got := initUnversionedRecoveryCommitMessage; got !=
		"bd init: commit pre-existing working set before schema migration (gastownhall/beads#6089)" {
		t.Fatalf("recovery commit message = %q", got)
	}
	if len(opener.configs) != 3 {
		t.Fatalf("opens = %d, want exactly 3", len(opener.configs))
	}
	if opener.configs[0] != original || opener.configs[2] != original {
		t.Fatal("the strict opens must receive the original config pointer")
	}
	if opener.configs[1] == original {
		t.Fatal("the lenient open must receive a copy, not the original config")
	}
	if !opener.configs[1].LenientOpen || opener.configs[1].Database != original.Database ||
		opener.configs[1].BeadsDir != original.BeadsDir || !opener.configs[1].ServerMode {
		t.Fatalf("lenient config = %+v, want the original with only LenientOpen changed", *opener.configs[1])
	}
	if original.LenientOpen {
		t.Fatal("openInitStore mutated the caller's strict intent")
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want exactly one", notes)
	}
	wantNote := "Note: database \"legacy_residue\" had uncommitted changes in tables the schema migrations alter " +
		"(config, interactions, routes); bd init committed the working set in its own commit, then applied the " +
		"migrations; no further action is needed (gastownhall/beads#6089)"
	if notes[0] != wantNote {
		t.Fatalf("note = %q, want %q", notes[0], wantNote)
	}
}

// TestOpenInitStoreCommitFailureWrapsRefusalAndRecovery pins the dual error
// contract: a failed CommitAll returns an error carrying BOTH the original
// refusal and the recovery error in the fixed text, after closing the
// lenient handle, with no third open.
func TestOpenInitStoreCommitFailureWrapsRefusalAndRecovery(t *testing.T) {
	refusal := eligibleUnversionedRefusal()
	commitErr := fmt.Errorf("CALL DOLT_COMMIT failed: deadlock")
	var events []string
	lenient := &fakeInitStore{events: &events, commitErr: commitErr}
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: refusal},
		{store: lenient},
	}}
	var notes []string
	store, err := openInitStore(context.Background(), serverInitConfig(), opener.open, func(line string) { notes = append(notes, line) })
	if store != nil {
		t.Fatal("openInitStore returned a store alongside an error")
	}
	wantText := unversionedRefusalFrom(refusal).Error() +
		"; bd init could not commit the pre-existing working set: " + commitErr.Error()
	if err == nil || err.Error() != wantText {
		t.Fatalf("openInitStore error = %v, want %q", err, wantText)
	}
	if unversionedRefusalFrom(err) == nil {
		t.Fatal("the wrapped error must still expose the DirtyTablesError chain")
	}
	if !errors.Is(err, commitErr) {
		t.Fatal("the wrapped error must expose the recovery error chain")
	}
	if want := []string{
		"open:lenient=false",
		"open:lenient=true",
		"commit:" + initUnversionedRecoveryCommitMessage,
		"close",
	}; strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v (closed, no third open)", events, want)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none after a failed recovery", notes)
	}
}

// TestOpenInitStoreLenientOpenFailureWrapsRefusalAndRecovery pins the same
// dual error contract when the lenient open itself fails.
func TestOpenInitStoreLenientOpenFailureWrapsRefusalAndRecovery(t *testing.T) {
	refusal := eligibleUnversionedRefusal()
	lenientErr := fmt.Errorf("failed to open Dolt store: server gone away")
	var events []string
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: refusal},
		{err: lenientErr},
	}}
	_, err := openInitStore(context.Background(), serverInitConfig(), opener.open, nil)
	wantText := unversionedRefusalFrom(refusal).Error() +
		"; bd init could not commit the pre-existing working set: " + lenientErr.Error()
	if err == nil || err.Error() != wantText {
		t.Fatalf("openInitStore error = %v, want %q", err, wantText)
	}
	if strings.Contains(strings.Join(events, "|"), "commit:") {
		t.Fatalf("a failed lenient open must not commit: %v", events)
	}
}

// TestOpenInitStoreNoopCommitStillReopens pins the (false, nil) contract: a
// reconciliation that found nothing committable still reopens strictly once,
// returns that store, and emits no success note.
func TestOpenInitStoreNoopCommitStillReopens(t *testing.T) {
	var events []string
	lenient := &fakeInitStore{events: &events} // committed=false, commitErr=nil
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: eligibleUnversionedRefusal()},
		{store: lenient},
		{},
	}}
	var notes []string
	store, err := openInitStore(context.Background(), serverInitConfig(), opener.open, func(line string) { notes = append(notes, line) })
	if err != nil {
		t.Fatalf("openInitStore: %v", err)
	}
	if store == nil {
		t.Fatal("openInitStore returned a nil store after a no-op reconciliation")
	}
	if want := []string{
		"open:lenient=false",
		"open:lenient=true",
		"commit:" + initUnversionedRecoveryCommitMessage,
		"close",
		"open:lenient=false",
	}; strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none for a no-op commit", notes)
	}
}

// TestOpenInitStoreFinalOpenFailurePassesThrough pins that a refusal on the
// final strict open is fatal and unchanged — not re-wrapped, not retried —
// and that the lenient handle was closed before it ran.
func TestOpenInitStoreFinalOpenFailurePassesThrough(t *testing.T) {
	finalErr := fmt.Errorf("failed to initialize schema: schema migration: boom")
	var events []string
	lenient := &fakeInitStore{events: &events, committed: true}
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: eligibleUnversionedRefusal()},
		{store: lenient},
		{err: finalErr},
	}}
	var notes []string
	store, err := openInitStore(context.Background(), serverInitConfig(), opener.open, func(line string) { notes = append(notes, line) })
	if store != nil {
		t.Fatal("openInitStore returned a store alongside an error")
	}
	if err != finalErr {
		t.Fatalf("openInitStore error = %v, want the final open's error unchanged", err)
	}
	if len(opener.configs) != 3 {
		t.Fatalf("opens = %d, want exactly 3 (no retry loop)", len(opener.configs))
	}
	want := []string{
		"open:lenient=false",
		"open:lenient=true",
		"commit:" + initUnversionedRecoveryCommitMessage,
		"close",
		"open:lenient=false",
	}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v (lenient closed before the final open)", events, want)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none when the final open fails", notes)
	}
}

// TestOpenInitStoreNilNoteWriter pins that the notes writer is optional: a
// nil writer (init under --quiet) is safe on every path, including a
// committed recovery.
func TestOpenInitStoreNilNoteWriter(t *testing.T) {
	var events []string
	lenient := &fakeInitStore{events: &events, committed: true}
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: eligibleUnversionedRefusal()},
		{store: lenient},
		{},
	}}
	store, err := openInitStore(context.Background(), serverInitConfig(), opener.open, nil)
	if err != nil {
		t.Fatalf("openInitStore with a nil note writer: %v", err)
	}
	if store == nil {
		t.Fatal("openInitStore returned a nil store")
	}
}

// TestOpenInitStoreCloseErrorDoesNotFailAdoption pins the close convention:
// like every init store close, the lenient handle's close error is advisory
// and ignored — a committed recovery still reopens and succeeds.
func TestOpenInitStoreCloseErrorDoesNotFailAdoption(t *testing.T) {
	var events []string
	lenient := &fakeInitStore{events: &events, committed: true, closeErr: fmt.Errorf("close: pool drained")}
	opener := &fakeInitOpener{events: &events, answers: []initOpenAnswer{
		{err: eligibleUnversionedRefusal()},
		{store: lenient},
		{},
	}}
	store, err := openInitStore(context.Background(), serverInitConfig(), opener.open, nil)
	if err != nil {
		t.Fatalf("openInitStore: %v", err)
	}
	if store == nil {
		t.Fatal("openInitStore returned a nil store")
	}
	if !strings.Contains(strings.Join(events, "|"), "close") {
		t.Fatalf("events = %v, want the lenient handle closed", events)
	}
}
