package main

import (
	"context"
	"errors"
	"testing"
)

type recordingInitCommitter struct {
	message string
	err     error
}

func (s *recordingInitCommitter) CommitWithConfig(_ context.Context, message string) error {
	s.message = message
	return s.err
}

func TestCommitInitStateIncludesIntentionalConfig(t *testing.T) {
	store := &recordingInitCommitter{}
	if err := commitInitState(context.Background(), store); err != nil {
		t.Fatalf("commitInitState: %v", err)
	}
	if store.message != "bd init" {
		t.Fatalf("CommitWithConfig message = %q, want %q", store.message, "bd init")
	}
}

func TestCommitInitStatePreservesCommitError(t *testing.T) {
	want := errors.New("commit failed")
	store := &recordingInitCommitter{err: want}
	if got := commitInitState(context.Background(), store); !errors.Is(got, want) {
		t.Fatalf("commitInitState error = %v, want %v", got, want)
	}
}
