package dolt

import (
	"errors"
	"strings"
	"testing"
)

func TestIsLockError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", errors.New("some error"), false},
		{"database is locked", errors.New("database is locked"), true},
		{"lock file", errors.New("cannot open lock file"), true},
		{"noms lock", errors.New("noms lock contention"), true},
		{"locked by another", errors.New("locked by another dolt process"), true},
		{"case insensitive", errors.New("DATABASE IS LOCKED"), true},
		{"connection refused", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLockError(tt.err); got != tt.want {
				t.Errorf("isLockError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestWrapLockError(t *testing.T) {
	t.Parallel()

	t.Run("nil passes through", func(t *testing.T) {
		if err := wrapLockError(nil); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("non-lock error passes through", func(t *testing.T) {
		orig := errors.New("some other error")
		got := wrapLockError(orig)
		if got != orig {
			t.Fatalf("expected original error, got %v", got)
		}
	})

	t.Run("lock error gets wrapped with guidance", func(t *testing.T) {
		orig := errors.New("database is locked")
		got := wrapLockError(orig)
		if got == orig {
			t.Fatal("expected wrapped error, got original")
		}
		if !strings.Contains(got.Error(), "bd doctor --fix") {
			t.Fatalf("expected actionable guidance in error, got: %v", got)
		}
		if !errors.Is(got, orig) {
			t.Fatal("wrapped error should unwrap to original")
		}
	})
}
