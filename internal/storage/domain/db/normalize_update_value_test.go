package db

import (
	"testing"
	"time"
)

// TestNormalizeUpdateValueWaiters pins the waiters serialization: the column
// is TEXT holding a JSON array, and the embedded path (issueops.updateIssueInTx)
// marshals the value unconditionally. Before bd-v50ru this repository passed
// the raw []string to the SQL driver, which refused it — so every waiters
// update through the proxied backend (e.g. `bd gate add-waiter`) failed.
func TestNormalizeUpdateValueWaiters(t *testing.T) {
	got := normalizeUpdateValue("waiters", []string{"a", "b"})
	if got != `["a","b"]` {
		t.Errorf("waiters []string: got %#v, want %q", got, `["a","b"]`)
	}
	if got := normalizeUpdateValue("waiters", []string{}); got != `[]` {
		t.Errorf("waiters empty slice: got %#v, want %q", got, `[]`)
	}
	if got := normalizeUpdateValue("waiters", nil); got != `null` {
		t.Errorf("waiters nil: got %#v, want %q", got, `null`)
	}

	// Untouched keys pass through unchanged.
	if got := normalizeUpdateValue("title", "hello"); got != "hello" {
		t.Errorf("title passthrough: got %#v", got)
	}
	now := time.Now()
	if got := normalizeUpdateValue("due_at", now); got != now.UTC() {
		t.Errorf("due_at normalization: got %#v", got)
	}
}
