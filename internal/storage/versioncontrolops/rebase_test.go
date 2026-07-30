package versioncontrolops

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Mirrors the sqlmock convention in fastforward_test.go: exercise the guard
// logic without a live embedded Dolt fixture. The real merge/renumber behaviour
// is covered by the embedded integration tests in internal/storage/embeddeddolt.

func TestRebaseChildCollisionsRejectsInvalidRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// No query expectations: an invalid ref must be rejected before any SQL runs.
	_, err = RebaseChildCollisions(context.Background(), db, "bad ref; drop table issues", false)
	if err == nil {
		t.Fatal("expected error for invalid remote ref, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHasCollidingAncestor(t *testing.T) {
	set := map[string]bool{
		"bd-a.71":   true,
		"bd-a.72":   true,
		"bd-b.5.71": true,
	}
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"root collider has no colliding ancestor", "bd-a.71", false},
		{"child of a colliding root is covered by its subtree", "bd-a.71.3", true},
		{"grandchild of a colliding root", "bd-a.71.3.9", true},
		{"deep collider whose parent does not collide is a root", "bd-b.5.71", false},
		{"child of a deep collider", "bd-b.5.71.1", true},
		{"unrelated id is not covered", "bd-c.1", false},
		{"sibling of a collider is not covered", "bd-a.99", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasCollidingAncestor(tt.id, set); got != tt.want {
				t.Errorf("hasCollidingAncestor(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
