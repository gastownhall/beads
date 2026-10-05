//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedOpen_EmptyDatabaseRejected verifies that embeddeddolt.Open fails
// with a clear error when called with an empty database name, rather than
// deferring to a confusing "no database selected" SQL error.
// Belt-and-suspenders defense for be-sy8 / GH#2988.
func TestEmbeddedOpen_EmptyDatabaseRejected(t *testing.T) {
	_, err := embeddeddolt.Open(t.Context(), t.TempDir(), "", "main")
	if err == nil {
		t.Fatal("expected error for empty database name")
	}
	if !strings.Contains(err.Error(), "database name must not be empty") {
		t.Errorf("unexpected error: %v", err)
	}
}
