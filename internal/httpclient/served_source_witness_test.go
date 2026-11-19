//go:build cgo

package httpclient

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// servedLockedBuffer is an in-process server's stderr: the server writes it
// from its own goroutines while the case reads it.
type servedLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *servedLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *servedLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestServedTierServesFromTheSourceItNames proves, on the server's own log,
// that newServedEnv served the case from the database source
// BEADS_HTTP_SERVED_SOURCE NAMES (servedSourceWitness). It parses the source
// from the environment rather than asking the harness, so a harness that
// ignored the switch would be caught rather than agreed with. It runs in both
// served lanes: roles in httpclient_served_test, provider in
// httpclient_served_provider_test.
func TestServedTierServesFromTheSourceItNames(t *testing.T) {
	log := &servedLockedBuffer{}
	env := newServedEnv(t, "hsrc", func(cfg *httpapi.Config) { cfg.Stderr = log })
	named, err := parseServedSource(os.Getenv(servedSourceEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	const id = "hsrc-witness"
	if _, err := lifecycle.Create(ctx, issueops.CreateRequest{Actor: "witness", Issue: &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}}); err != nil {
		t.Fatalf("create %s through the served client: %v", id, err)
	}
	// The request line is written as the handler returns, which can trail the
	// client's read of the response.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(log.String(), "op="+httpapi.OpCreateIssue) {
			break
		}
	}
	served := log.String()
	violations := servedSourceWitness(named, served)
	for _, v := range violations {
		t.Error(v)
	}
	var rows int
	if err := env.queryScalar(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{id}, &rows); err != nil {
		t.Errorf("read %s from the database the hooks read: %v", id, err)
	} else if rows != 1 {
		t.Errorf("%s is in the database the hooks read %d times, want once: the server wrote somewhere else", id, rows)
	}
	if t.Failed() {
		t.Logf("source %s, server log:\n%s", named, served)
	}
}
