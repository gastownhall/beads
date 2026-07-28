package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// TestBuildUpdateSpecForIssue_RoutesMergeOpsAsOperations locks in the
// lost-update fix on the proxied-server route: metadata edits and note
// appends must flow into spec.Fields as merge OPERATION keys, never as values
// pre-merged from `current` (a read from this unit of work's MVCC snapshot —
// merging there erased keys a concurrent writer committed after the snapshot).
func TestBuildUpdateSpecForIssue_RoutesMergeOpsAsOperations(t *testing.T) {
	current := &types.Issue{
		ID:       "bd-spec-1",
		Status:   types.StatusOpen,
		Notes:    "existing notes",
		Metadata: json.RawMessage(`{"existing":"yes"}`),
	}

	t.Run("append_notes", func(t *testing.T) {
		in := &updateInput{
			fields:         map[string]any{},
			hasAppendNotes: true,
			appendNotes:    "appended",
		}
		spec := buildUpdateSpecForIssue(current, in)
		if got, ok := spec.Fields[issueops.OpAppendNotes]; !ok || got != "appended" {
			t.Errorf("Fields[%s] = %v (ok=%v), want the raw append text", issueops.OpAppendNotes, got, ok)
		}
		if merged, ok := spec.Fields["notes"]; ok {
			t.Errorf("Fields[notes] = %v: notes must NOT be pre-merged from the snapshot", merged)
		}
	})

	t.Run("merge_metadata", func(t *testing.T) {
		in := &updateInput{
			fields:          map[string]any{},
			mergeMetadataIn: json.RawMessage(`{"new":"key"}`),
		}
		spec := buildUpdateSpecForIssue(current, in)
		raw, ok := spec.Fields[issueops.OpMergeMetadata].(json.RawMessage)
		if !ok || string(raw) != `{"new":"key"}` {
			t.Errorf("Fields[%s] = %v, want the raw incoming JSON", issueops.OpMergeMetadata, spec.Fields[issueops.OpMergeMetadata])
		}
		if merged, ok := spec.Fields["metadata"]; ok {
			t.Errorf("Fields[metadata] = %v: metadata must NOT be pre-merged from the snapshot", merged)
		}
	})

	t.Run("set_and_unset_metadata", func(t *testing.T) {
		in := &updateInput{
			fields:        map[string]any{},
			setMetadata:   []string{"tier=gold"},
			unsetMetadata: []string{"existing"},
		}
		spec := buildUpdateSpecForIssue(current, in)
		set, _ := spec.Fields[issueops.OpSetMetadata].([]string)
		unset, _ := spec.Fields[issueops.OpUnsetMetadata].([]string)
		if len(set) != 1 || set[0] != "tier=gold" {
			t.Errorf("Fields[%s] = %v, want [tier=gold]", issueops.OpSetMetadata, spec.Fields[issueops.OpSetMetadata])
		}
		if len(unset) != 1 || unset[0] != "existing" {
			t.Errorf("Fields[%s] = %v, want [existing]", issueops.OpUnsetMetadata, spec.Fields[issueops.OpUnsetMetadata])
		}
		if merged, ok := spec.Fields["metadata"]; ok {
			t.Errorf("Fields[metadata] = %v: metadata must NOT be pre-merged from the snapshot", merged)
		}
	})

	t.Run("clear_defer_status_still_resolved_from_current", func(t *testing.T) {
		deferred := &types.Issue{ID: "bd-spec-2", Status: types.StatusDeferred}
		in := &updateInput{fields: map[string]any{}, clearDeferStatus: true}
		spec := buildUpdateSpecForIssue(deferred, in)
		if got := spec.Fields["status"]; got != string(types.StatusOpen) {
			t.Errorf("Fields[status] = %v, want open (clearDeferStatus on a deferred issue)", got)
		}
	})
}

// --- fakes for the whole-attempt retry tests ---

type fakeUpdateIssueUC struct {
	domain.IssueUseCase // unimplemented methods panic; the attempt path must not call them
	issue               *types.Issue
}

func (f *fakeUpdateIssueUC) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return f.issue, nil
}

func (f *fakeUpdateIssueUC) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	return f.issue, nil
}

type fakeUOW struct {
	issueUC domain.IssueUseCase
	commit  func() error
}

func (f *fakeUOW) Close(ctx context.Context)                                 {}
func (f *fakeUOW) Commit(ctx context.Context, message string) error          { return f.commit() }
func (f *fakeUOW) SwitchDatabase(ctx context.Context, database string) error { return nil }
func (f *fakeUOW) ConfigUseCase() domain.ConfigUseCase                       { return nil }
func (f *fakeUOW) DoltRemoteUseCase() domain.DoltRemoteUseCase               { return nil }
func (f *fakeUOW) BootstrapUseCase() domain.BootstrapUseCase                 { return nil }
func (f *fakeUOW) IssueUseCase() domain.IssueUseCase                         { return f.issueUC }
func (f *fakeUOW) DependencyUseCase() domain.DependencyUseCase               { return nil }
func (f *fakeUOW) LabelUseCase() domain.LabelUseCase                         { return nil }
func (f *fakeUOW) CommentUseCase() domain.CommentUseCase                     { return nil }
func (f *fakeUOW) RawSQLUseCase() domain.RawSQLUseCase                       { return nil }

type fakeUOWProvider struct {
	uows   atomic.Int64
	commit func(attempt int64) error
	issue  *types.Issue
}

func (p *fakeUOWProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	n := p.uows.Add(1)
	return &fakeUOW{
		issueUC: &fakeUpdateIssueUC{issue: p.issue},
		commit:  func() error { return p.commit(n) },
	}, nil
}

func (p *fakeUOWProvider) Close(ctx context.Context) error { return nil }

func withFakeProxiedUpdateEnv(t *testing.T, p *fakeUOWProvider) {
	t.Helper()
	oldProvider := uowProvider
	oldHookRunner := hookRunner
	uowProvider = p
	hookRunner = hooks.NewRunner(t.TempDir()) // no hooks installed: RunSync no-ops
	t.Cleanup(func() {
		uowProvider = oldProvider
		hookRunner = oldHookRunner
	})
}

func captureStderrDuring(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	defer func() {
		os.Stderr = old
		_ = r.Close()
	}()
	fn()
	_ = w.Close()
	return <-done
}

func serializationFailure() error {
	return &mysql.MySQLError{Number: 1213, Message: "serialization failure, transaction rolled back"}
}

// TestApplyUpdateProxiedOne_RetriesWholeAttemptOnConflict proves a Dolt
// serialization failure at commit time redoes the WHOLE read-merge-write in a
// fresh unit of work — not just the commit. Re-committing the rolled-back
// session was the old behavior; the server had already discarded the writes,
// so the retry could only report "nothing to commit" and the update vanished.
func TestApplyUpdateProxiedOne_RetriesWholeAttemptOnConflict(t *testing.T) {
	issue := &types.Issue{ID: "bd-retry-1", Status: types.StatusOpen}
	provider := &fakeUOWProvider{
		issue: issue,
		commit: func(attempt int64) error {
			if attempt <= 2 {
				return serializationFailure()
			}
			return nil
		},
	}
	withFakeProxiedUpdateEnv(t, provider)

	in := &updateInput{fields: map[string]any{"title": "renamed"}}
	got, fail, err := applyUpdateProxiedOne(context.Background(), "bd-retry-1", in)
	if err != nil {
		t.Fatalf("applyUpdateProxiedOne: %v", err)
	}
	if fail != nil {
		t.Fatalf("expected update to succeed after conflict retries, got failure: %s", fail.Error)
	}
	if got == nil || got.ID != "bd-retry-1" {
		t.Fatalf("updated issue = %v, want bd-retry-1", got)
	}
	if n := provider.uows.Load(); n != 3 {
		t.Errorf("unit-of-work attempts = %d, want 3 (each conflict must redo the whole read-merge-write)", n)
	}
}

// TestApplyUpdateProxiedOne_ExhaustedConflictsFailLoudly proves a write that
// never lands cannot exit as a success: when every attempt loses Dolt's
// commit-time merge, the command reports the failure instead of printing
// "✓ Updated" (a non-empty failReason suppresses the success line and drives
// the non-zero exit via reportUpdateFailures in runUpdateProxiedServer).
func TestApplyUpdateProxiedOne_ExhaustedConflictsFailLoudly(t *testing.T) {
	oldMax := proxiedUpdateRetryMaxElapsed
	proxiedUpdateRetryMaxElapsed = 150 * time.Millisecond
	t.Cleanup(func() { proxiedUpdateRetryMaxElapsed = oldMax })

	issue := &types.Issue{ID: "bd-retry-2", Status: types.StatusOpen}
	provider := &fakeUOWProvider{
		issue:  issue,
		commit: func(int64) error { return serializationFailure() },
	}
	withFakeProxiedUpdateEnv(t, provider)

	var got *types.Issue
	var fail *updateIDFailure
	var err error
	stderr := captureStderrDuring(t, func() {
		in := &updateInput{fields: map[string]any{"title": "never lands"}}
		got, fail, err = applyUpdateProxiedOne(context.Background(), "bd-retry-2", in)
	})
	if err != nil {
		t.Fatalf("applyUpdateProxiedOne returned hard error: %v", err)
	}
	if fail == nil || got != nil {
		t.Fatalf("fail=%v issue=%v: a write that never landed must be reported as a failure", fail, got)
	}
	if fail.GuardMismatch {
		t.Errorf("exhausted conflicts must not masquerade as a guard mismatch (that would exit 13 and tell scripts not to retry)")
	}
	if !strings.Contains(stderr, "retries exhausted") {
		t.Errorf("stderr = %q, want a loud retries-exhausted failure", stderr)
	}
	if n := provider.uows.Load(); n < 2 {
		t.Errorf("unit-of-work attempts = %d, want at least 2 before giving up", n)
	}
}

// TestApplyUpdateProxiedOne_NothingToCommitWithoutConflictSucceeds preserves
// the one legitimate nothing-to-commit case: wisp-only updates write to
// dolt_ignored tables, so a successful ApplyUpdate can leave the Dolt commit
// layer with nothing to do. That is a success, not a lost write — no
// serialization failure preceded it.
func TestApplyUpdateProxiedOne_NothingToCommitWithoutConflictSucceeds(t *testing.T) {
	issue := &types.Issue{ID: "bd-retry-3", Status: types.StatusOpen}
	provider := &fakeUOWProvider{
		issue:  issue,
		commit: func(int64) error { return errors.New("nothing to commit") },
	}
	withFakeProxiedUpdateEnv(t, provider)

	in := &updateInput{fields: map[string]any{"title": "wisp-shaped"}}
	got, fail, err := applyUpdateProxiedOne(context.Background(), "bd-retry-3", in)
	if err != nil {
		t.Fatalf("applyUpdateProxiedOne: %v", err)
	}
	if fail != nil || got == nil {
		t.Fatalf("fail=%v issue=%v: empty-working-set commit on an unconflicted attempt is a success", fail, got)
	}
	if n := provider.uows.Load(); n != 1 {
		t.Errorf("unit-of-work attempts = %d, want 1 (nothing-to-commit must not trigger retries)", n)
	}
}
