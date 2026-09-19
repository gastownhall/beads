//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// newProvenanceTestIssue creates an issue the provenance events can hang off
// (the FK requires a real issue row).
func newProvenanceTestIssue(t *testing.T, ctx context.Context, s interface {
	CreateIssue(context.Context, *types.Issue, string) error
}) string {
	t.Helper()
	issue := &types.Issue{
		Title:     "Provenance subject",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return issue.ID
}

func TestProvenanceRecordLogByRefRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	sha := "0123456789abcdef0123456789abcdef01234567"
	refKind := "git-sha"
	actor := "git-hook"
	ev := types.ProvenanceEvent{
		IssueID: issueID,
		Kind:    types.ProvCommit,
		Source:  "git-hook",
		Actor:   &actor,
		Ref:     &sha,
		RefKind: &refKind,
	}
	id, inserted, err := s.RecordProvenanceEvent(ctx, ev)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !inserted {
		t.Fatal("first record should report inserted=true")
	}
	if id == "" {
		t.Fatal("record returned empty id")
	}

	logged, err := s.GetProvenanceEvents(ctx, issueID, "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("log returned %d events, want 1", len(logged))
	}
	got := logged[0]
	if got.Kind != types.ProvCommit || got.Ref == nil || *got.Ref != sha || got.Actor == nil || *got.Actor != actor {
		t.Fatalf("logged event mismatch: %+v", got)
	}

	byRef, err := s.GetProvenanceByRef(ctx, sha)
	if err != nil {
		t.Fatalf("by-ref: %v", err)
	}
	if len(byRef) != 1 || byRef[0].ID != id {
		t.Fatalf("by-ref returned %d events (ids mismatch)", len(byRef))
	}
}

func TestProvenanceRecordIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	sha := "abcabcabcabcabcabcabcabcabcabcabcabcabca"
	refKind := "git-sha"
	ev := types.ProvenanceEvent{
		IssueID: issueID,
		Kind:    types.ProvLand,
		Source:  "orchestrator",
		Ref:     &sha,
		RefKind: &refKind,
	}

	id1, inserted1, err := s.RecordProvenanceEvent(ctx, ev)
	if err != nil {
		t.Fatalf("first record: %v", err)
	}
	id2, inserted2, err := s.RecordProvenanceEvent(ctx, ev)
	if err != nil {
		t.Fatalf("second record: %v", err)
	}
	if !inserted1 {
		t.Fatal("first record should be inserted")
	}
	if inserted2 {
		t.Fatal("second record should report inserted=false (idempotent no-op)")
	}
	if id1 != id2 {
		t.Fatalf("deterministic id changed: %s vs %s", id1, id2)
	}

	logged, err := s.GetProvenanceEvents(ctx, issueID, "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("idempotent re-record left %d rows, want 1", len(logged))
	}
}

func TestProvenanceRejectsUnknownKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	_, _, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID,
		Kind:    types.ProvKind("teleport"),
		Source:  "orchestrator",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("expected unknown-kind rejection, got %v", err)
	}
}

func TestProvenanceRejectsReservedSourceCaseInsensitive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	at := time.Now().UTC()
	for _, src := range []string{"ingest-backfill", "Ingest-Backfill", "INGEST-BACKFILL"} {
		_, _, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
			IssueID:    issueID,
			Kind:       types.ProvClaim,
			Source:     src,
			OccurredAt: &at,
		})
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("source %q: expected reserved-source rejection, got %v", src, err)
		}
	}
}

func TestProvenanceRejectsNon40HexGitSHA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	badRef := "not-a-sha"
	refKind := "git-sha"
	_, _, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID,
		Kind:    types.ProvCommit,
		Source:  "git-hook",
		Ref:     &badRef,
		RefKind: &refKind,
	})
	if err == nil || !strings.Contains(err.Error(), "git-sha") {
		t.Fatalf("expected git-sha shape rejection, got %v", err)
	}
}

// TestProvenanceRefLessKindIsDeterministicWithOccurredAt verifies the property
// the CLI's --at requirement protects: a ref-less kind keyed by occurred_at gets
// distinct ids for distinct event-times and a stable id for the same one.
func TestProvenanceRefLessKindIsDeterministicWithOccurredAt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	t1 := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 19, 13, 0, 0, 0, time.UTC)

	id1a, ins1a, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID, Kind: types.ProvClaim, Source: "orchestrator", OccurredAt: &t1,
	})
	if err != nil {
		t.Fatalf("record t1: %v", err)
	}
	id1b, ins1b, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID, Kind: types.ProvClaim, Source: "orchestrator", OccurredAt: &t1,
	})
	if err != nil {
		t.Fatalf("re-record t1: %v", err)
	}
	id2, ins2, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID, Kind: types.ProvClaim, Source: "orchestrator", OccurredAt: &t2,
	})
	if err != nil {
		t.Fatalf("record t2: %v", err)
	}

	if !ins1a || ins1b || !ins2 {
		t.Fatalf("inserted flags wrong: %v %v %v", ins1a, ins1b, ins2)
	}
	if id1a != id1b {
		t.Fatal("same event-time should yield same id")
	}
	if id1a == id2 {
		t.Fatal("different event-times should yield different ids")
	}

	// Kind filter narrows results.
	claims, err := s.GetProvenanceEvents(ctx, issueID, string(types.ProvClaim))
	if err != nil {
		t.Fatalf("filtered log: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("kind-filtered log returned %d, want 2", len(claims))
	}
}

// TestProvenanceRejectsRefLessWithoutOccurredAt covers the store-boundary guard:
// an event with neither a ref nor an occurred_at has no stable-id discriminator,
// so distinct events would collapse to one id. This must be rejected for any
// kind (handoff here — the CLI's old ref-less kind list missed cut/handoff/used).
func TestProvenanceRejectsRefLessWithoutOccurredAt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	_, _, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID,
		Kind:    types.ProvHandoff,
		Source:  "orchestrator",
	})
	if err == nil || !strings.Contains(err.Error(), "occurred_at") {
		t.Fatalf("expected ref-less-without-occurred_at rejection, got %v", err)
	}
}

// TestProvenanceRejectsRefKindWithoutRef covers the low-severity guard: a
// ref_kind set with no ref is meaningless and must be rejected.
func TestProvenanceRejectsRefKindWithoutRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	refKind := "git-sha"
	_, _, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID,
		Kind:    types.ProvCut,
		Source:  "orchestrator",
		RefKind: &refKind,
	})
	if err == nil || !strings.Contains(err.Error(), "requires a ref") {
		t.Fatalf("expected ref-kind-without-ref rejection, got %v", err)
	}
}

// TestProvenanceSubSecondOccurredAtIsIdempotent verifies that recording the same
// ref-less event twice with a sub-second --at is idempotent: occurred_at is
// stored at second precision (bare DATETIME), and the id basis truncates to whole
// seconds to match, so the second record is a no-op and exactly one row exists.
func TestProvenanceSubSecondOccurredAtIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	// 123456789ns fractional seconds — must not affect the id.
	at := time.Date(2026, 6, 20, 12, 0, 0, 123456789, time.UTC)
	ev := types.ProvenanceEvent{
		IssueID:    issueID,
		Kind:       types.ProvClaim,
		Source:     "orchestrator",
		OccurredAt: &at,
	}

	id1, ins1, err := s.RecordProvenanceEvent(ctx, ev)
	if err != nil {
		t.Fatalf("first record: %v", err)
	}
	id2, ins2, err := s.RecordProvenanceEvent(ctx, ev)
	if err != nil {
		t.Fatalf("second record: %v", err)
	}
	if !ins1 || ins2 {
		t.Fatalf("sub-second --at not idempotent: inserted flags %v %v", ins1, ins2)
	}
	if id1 != id2 {
		t.Fatalf("id changed across truncated re-record: %s vs %s", id1, id2)
	}

	logged, err := s.GetProvenanceEvents(ctx, issueID, "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("sub-second re-record left %d rows, want 1", len(logged))
	}
	// Stored occurred_at is truncated to whole seconds (matches the id basis).
	if logged[0].OccurredAt == nil || logged[0].OccurredAt.Nanosecond() != 0 {
		t.Fatalf("stored occurred_at not truncated to whole seconds: %v", logged[0].OccurredAt)
	}
}

// TestProvenanceOpaqueFieldsRoundTripExactly verifies the NullStringPtr swap did
// not regress opaque-field handling: a non-empty actor round-trips byte-for-byte,
// and an absent (nil) actor reads back as nil (NULL).
func TestProvenanceOpaqueFieldsRoundTripExactly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	sha := "0123456789abcdef0123456789abcdef01234567"
	refKind := "git-sha"
	actor := "agent/worker-7 (session=abc)"
	if _, _, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID, Kind: types.ProvCommit, Source: "git-hook",
		Actor: &actor, Ref: &sha, RefKind: &refKind,
	}); err != nil {
		t.Fatalf("record with actor: %v", err)
	}

	// Absent actor must read back as nil (NULL), not empty string.
	sha2 := "fedcba9876543210fedcba9876543210fedcba98"
	if _, _, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID, Kind: types.ProvLand, Source: "git-hook",
		Ref: &sha2, RefKind: &refKind,
	}); err != nil {
		t.Fatalf("record without actor: %v", err)
	}

	logged, err := s.GetProvenanceEvents(ctx, issueID, "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	var withActor, withoutActor *types.ProvenanceEvent
	for i := range logged {
		switch logged[i].Kind {
		case types.ProvCommit:
			withActor = &logged[i]
		case types.ProvLand:
			withoutActor = &logged[i]
		}
	}
	if withActor == nil || withActor.Actor == nil || *withActor.Actor != actor {
		t.Fatalf("actor did not round-trip exactly: %+v", withActor)
	}
	if withoutActor == nil || withoutActor.Actor != nil {
		t.Fatalf("absent actor should read back nil (NULL), got %+v", withoutActor)
	}
}

// TestProvenanceNonGitShaRefKindWithRefWorks verifies a non-git-sha ref-kind
// with a ref is accepted (the ref-kind/ref guards do not over-reject).
func TestProvenanceNonGitShaRefKindWithRefWorks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	issueID := newProvenanceTestIssue(t, ctx, s)

	ref := "https://github.com/example/repo/pull/42"
	refKind := "pr"
	_, inserted, err := s.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: issueID,
		Kind:    types.ProvLand,
		Source:  "orchestrator",
		Ref:     &ref,
		RefKind: &refKind,
	})
	if err != nil {
		t.Fatalf("record pr ref: %v", err)
	}
	if !inserted {
		t.Fatal("first record should report inserted=true")
	}
}
