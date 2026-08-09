package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// readerProvider is lookupOnlyProvider plus the capability accessor, which is
// the door `bd show --json` now reaches the detail view through on this route.
// The reader it hands back is the REAL one, over the same failing lookup, so
// what these tests observe is the role's own error vocabulary reaching the
// command — not a stub's imitation of it.
type readerProvider struct{ lookupOnlyProvider }

func (p readerProvider) IssueReader() (issueops.Reader, error) {
	return uow.NewIssueReader(p)
}

var _ uow.IssueReaderSource = readerProvider{}

func withStubbedProxiedReader(t *testing.T, hardErr error) {
	t.Helper()
	oldProvider, oldJSON := uowProvider, jsonOutput
	uowProvider = readerProvider{lookupOnlyProvider{issues: stubLookupIssueUC{hardErr: hardErr}}}
	jsonOutput = true
	t.Cleanup(func() {
		uowProvider = oldProvider
		jsonOutput = oldJSON
	})
}

// TestShowProxiedJSONMissingIDKeepsItsContract is the regression guard for
// moving the proxied detail view onto issueops.Reader. The corpus pins a PAIR
// of outputs for a missing id under --json — the human line on stderr and the
// error envelope on stdout, exit 1 — and the role reports the same miss as a
// storage.ErrNotFound rather than as a resolve failure the command reported
// itself. If that mapping is ever lost, this fails instead of the corpus.
func TestShowProxiedJSONMissingIDKeepsItsContract(t *testing.T) {
	withStubbedProxiedReader(t, nil)

	var err error
	stderr := captureStderrDuring(t, func() {
		err = runShowProxiedServer(&cobra.Command{}, context.Background(), []string{stubMissingID})
	})

	if err == nil {
		t.Error("a batch that found nothing exited zero")
	}
	if got, want := strings.TrimSpace(stderr), "Issue "+stubMissingID+" not found"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if strings.Contains(stderr, stubRawNoRows) {
		t.Errorf("stderr leaks the raw driver sentinel: %q", stderr)
	}
}

// TestShowProxiedJSONBackendFailureAborts pins the one behavior this move
// changed on purpose. Resolution and assembly used to be two calls, and a
// backend failure was reported per id and skipped past when it surfaced in the
// first, aborted on when it surfaced in the second. They are one call now, so
// there is one answer, and abort is the one worth keeping: a JSON array
// missing the rows a database error swallowed is indistinguishable from a
// complete one.
func TestShowProxiedJSONBackendFailureAborts(t *testing.T) {
	withStubbedProxiedReader(t, errors.New(stubBackendError))

	var err error
	_ = captureStderrDuring(t, func() {
		err = runShowProxiedServer(&cobra.Command{}, context.Background(), []string{stubMissingID})
	})

	if err == nil {
		t.Fatal("a backend failure exited zero")
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Errorf("backend failure reported as a missing issue: %v", err)
	}
}

// TestShowProxiedTextRouteDoesNotOpenAReader pins the other half of the split:
// the terminal rendering needs the raw issue and the wisp flag, which the
// detail contract does not carry, so it stays on the CLI's own resolution and
// must not pay for a reader it cannot use.
func TestShowProxiedTextRouteDoesNotOpenAReader(t *testing.T) {
	withStubbedProxiedLookup(t, nil) // a provider with NO capability accessor

	var err error
	stderr := captureStderrDuring(t, func() {
		err = runShowProxiedServer(&cobra.Command{}, context.Background(), []string{stubMissingID})
	})

	if err == nil {
		t.Error("a batch that found nothing exited zero")
	}
	if got, want := strings.TrimSpace(stderr), "Issue "+stubMissingID+" not found"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}
