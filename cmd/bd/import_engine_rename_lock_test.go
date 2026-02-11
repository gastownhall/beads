package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

type flakyBusyStore struct {
	storage.Storage

	mu                 sync.Mutex
	deleteBusyFailures int
	createBusyFailures int
	deleteCalls        int
	createCalls        int
}

func (s *flakyBusyStore) DeleteIssue(ctx context.Context, id string) error {
	s.mu.Lock()
	s.deleteCalls++
	if s.deleteBusyFailures > 0 {
		s.deleteBusyFailures--
		s.mu.Unlock()
		return errors.New("database is locked")
	}
	s.mu.Unlock()
	return s.Storage.DeleteIssue(ctx, id)
}

func (s *flakyBusyStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	s.mu.Lock()
	s.createCalls++
	if s.createBusyFailures > 0 {
		s.createBusyFailures--
		s.mu.Unlock()
		return errors.New("SQLITE_BUSY: database is locked")
	}
	s.mu.Unlock()
	return s.Storage.CreateIssue(ctx, issue, actor)
}

func TestImportEngineRename_NoDatabaseLockUnderConcurrentLoad(t *testing.T) {
	ctx := context.Background()

	base := memory.New("")
	if err := base.SetConfig(ctx, "issue_prefix", "tt"); err != nil {
		t.Fatalf("failed to set issue prefix: %v", err)
	}

	baseTime := time.Now().Add(-time.Hour).UTC()
	oldIssue := &types.Issue{
		ID:          "tt-55r",
		Title:       "Rename target",
		Description: "same content",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   baseTime,
		UpdatedAt:   baseTime,
	}
	if err := base.CreateIssue(ctx, oldIssue, "test"); err != nil {
		t.Fatalf("failed to create original issue: %v", err)
	}

	store := &flakyBusyStore{
		Storage:            base,
		deleteBusyFailures: 6,
		createBusyFailures: 6,
	}

	buildRenamed := func() *types.Issue {
		return &types.Issue{
			ID:          "tt-trd.4",
			Title:       "Rename target",
			Description: "same content",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   baseTime,
			UpdatedAt:   baseTime.Add(time.Second),
		}
	}

	const workers = 12
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, importErr := importIssuesEngine(ctx, "", store, []*types.Issue{buildRenamed()}, ImportOptions{})
			if importErr != nil {
				errCh <- importErr
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)

	for importErr := range errCh {
		if strings.Contains(strings.ToLower(importErr.Error()), "database is locked") {
			t.Fatalf("rename import should not fail with database lock: %v", importErr)
		}
		t.Fatalf("unexpected import error: %v", importErr)
	}

	renamed, err := store.GetIssue(ctx, "tt-trd.4")
	if err != nil {
		t.Fatalf("failed to load renamed issue: %v", err)
	}
	if renamed == nil {
		t.Fatal("expected renamed issue to exist")
	}

	store.mu.Lock()
	deletes := store.deleteCalls
	creates := store.createCalls
	remainingDeleteFailures := store.deleteBusyFailures
	remainingCreateFailures := store.createBusyFailures
	store.mu.Unlock()

	if remainingDeleteFailures != 0 || remainingCreateFailures != 0 {
		t.Fatalf("expected retry path to consume busy failures, remaining delete=%d create=%d", remainingDeleteFailures, remainingCreateFailures)
	}
	if deletes <= 6 || creates <= 6 {
		t.Fatalf("expected retries to perform additional calls, deleteCalls=%d createCalls=%d", deletes, creates)
	}
}
