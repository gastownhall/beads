package dolt

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestConditionalWriteGateError(t *testing.T) {
	var err error = &ConditionalWriteGateError{Reason: "a remote is configured"}
	if !errors.Is(err, storage.ErrConditionalWriteUnsupported) {
		t.Fatal("gate error must unwrap to storage.ErrConditionalWriteUnsupported")
	}
	if !strings.Contains(err.Error(), AllowUnsafeCASEnv) {
		t.Fatalf("gate error should name the escape-hatch env var: %v", err)
	}
}

// TestAllowUnsafeCASEnv runs sequentially (no t.Parallel), so its process-env
// mutation completes before the package's parallel tests resume.
func TestAllowUnsafeCASEnv(t *testing.T) {
	old, had := os.LookupEnv(AllowUnsafeCASEnv)
	t.Cleanup(func() {
		if had {
			os.Setenv(AllowUnsafeCASEnv, old)
		} else {
			os.Unsetenv(AllowUnsafeCASEnv)
		}
	})

	for _, c := range []struct {
		val  string
		want bool
	}{
		{"", false}, {"1", true}, {"true", true}, {"TRUE", true},
		{"0", false}, {"false", false}, {"nonsense", false},
	} {
		if c.val == "" {
			os.Unsetenv(AllowUnsafeCASEnv)
		} else {
			os.Setenv(AllowUnsafeCASEnv, c.val)
		}
		if got := allowUnsafeCAS(); got != c.want {
			t.Errorf("allowUnsafeCAS() with %s=%q = %v, want %v", AllowUnsafeCASEnv, c.val, got, c.want)
		}
	}
}

// TestConditionalWriteGateRefusesWithRemote uses an isolated database (not the
// shared branch-per-test DB) so configuring a repo-level remote does not
// contaminate the other parallel CAS tests.
func TestConditionalWriteGateRefusesWithRemote(t *testing.T) {
	skipIfNoDolt(t)
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx := context.Background()
	makeCASIssue(t, ctx, store, "gate-1")
	r0 := revOf(t, ctx, store, "gate-1")

	// Before a remote is configured, whole-row CAS works.
	if _, err := store.UpdateIssueIfMatch(ctx, "gate-1", map[string]interface{}{"title": "ok"}, &r0, "tester"); err != nil {
		t.Fatalf("CAS should work with no remote: %v", err)
	}
	r1 := revOf(t, ctx, store, "gate-1")

	// Configure a repo-level remote; the gate must now refuse whole-row CAS.
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', 'origin', 'file:///tmp/beads-gate-remote-test')"); err != nil {
		t.Fatalf("configure remote: %v", err)
	}

	_, err := store.UpdateIssueIfMatch(ctx, "gate-1", map[string]interface{}{"title": "x"}, &r1, "tester")
	if !errors.Is(err, storage.ErrConditionalWriteUnsupported) {
		t.Fatalf("with a remote, UpdateIssueIfMatch must be refused as unsupported; got %v", err)
	}
	var ge *ConditionalWriteGateError
	if !errors.As(err, &ge) {
		t.Fatalf("want *ConditionalWriteGateError, got %T (%v)", err, err)
	}
	if err := store.DeleteIssueIfMatch(ctx, "gate-1", &r1); !errors.Is(err, storage.ErrConditionalWriteUnsupported) {
		t.Fatalf("DeleteIssueIfMatch must be gated with a remote; got %v", err)
	}

	// The whole-row gate must NOT touch metadata-key CAS (B0): it is session-local
	// and sound across a merge without a nonce, so it stays available under federation.
	if _, err := store.CompareAndSetMetadataKey(ctx, "gate-1", "gc.epoch", nil, "1", "tester"); err != nil {
		t.Fatalf("metadata CAS must not be gated by the whole-row gate: %v", err)
	}

	// The row must be untouched by the refused writes.
	if cur := revOf(t, ctx, store, "gate-1"); cur == r1 {
		// metadata CAS bumped revision, so it should differ from r1 — but the
		// refused title write must not have applied "x".
		iss, _ := store.GetIssue(ctx, "gate-1")
		if iss != nil && iss.Title == "x" {
			t.Fatal("a gated UpdateIssueIfMatch must not have mutated the row")
		}
	}
}
