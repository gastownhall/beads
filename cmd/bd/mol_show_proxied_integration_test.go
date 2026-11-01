//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/uow"
)

func TestProxiedServerMolShow(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("shows_root_children_and_deps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "msh")
		root := bdProxiedCreate(t, bd, p.dir, "Molecule root", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Step one", "--type", "task", "--parent", root.ID)

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "show", root.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol show --json: %v\n%s", err, out)
		}
		var got struct {
			Root struct {
				ID string `json:"id"`
			} `json:"root"`
			Issues []struct {
				ID string `json:"id"`
			} `json:"issues"`
			Dependencies []struct {
				IssueID     string `json:"issue_id"`
				DependsOnID string `json:"depends_on_id"`
			} `json:"dependencies"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.Root.ID != root.ID {
			t.Errorf("root.id = %s, want %s", got.Root.ID, root.ID)
		}
		if len(got.Issues) != 2 {
			t.Fatalf("expected 2 issues (root+child), got %d: %+v", len(got.Issues), got.Issues)
		}
		foundDep := false
		for _, d := range got.Dependencies {
			if d.IssueID == child.ID && d.DependsOnID == root.ID {
				foundDep = true
			}
		}
		if !foundDep {
			t.Errorf("expected parent-child dependency %s -> %s, got %+v", child.ID, root.ID, got.Dependencies)
		}
	})

	t.Run("parallel_marks_ready_and_blocked_steps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mshp")
		root := bdProxiedCreate(t, bd, p.dir, "Parallel root", "--type", "epic")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker step", "--type", "task", "--parent", root.ID)
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked step", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", blocked.ID, blocker.ID); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "show", root.ID, "--parallel", "--json")
		if err != nil {
			t.Fatalf("bd mol show --parallel --json: %v\n%s", err, out)
		}
		var got struct {
			Parallel struct {
				Steps map[string]struct {
					IsReady   bool     `json:"is_ready"`
					BlockedBy []string `json:"blocked_by"`
				} `json:"steps"`
			} `json:"parallel"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if !got.Parallel.Steps[blocker.ID].IsReady {
			t.Errorf("blocker %s should be ready: %+v", blocker.ID, got.Parallel.Steps[blocker.ID])
		}
		if got.Parallel.Steps[blocked.ID].IsReady {
			t.Errorf("blocked step %s should not be ready", blocked.ID)
		}
	})

	t.Run("unknown_id_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mshu")
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "show", "bd-doesnotexist")
		if err == nil {
			t.Fatalf("expected error, got stdout:%s stderr:%s", stdout, stderr)
		}
	})
}

// TestProxiedMolResolveReportsAnUnopenableReadAsItself pins that the proxied
// molecule resolver (proxiedResolvePartialID: mol show, current, progress and
// ready --mol) reports a read unit of work it cannot open as that failure,
// never as a molecule that was not found, while an id the lookup does not find
// still reads as not found (moleculeNotResolved).
func TestProxiedMolResolveReportsAnUnopenableReadAsItself(t *testing.T) {
	oldProvider := uowProvider
	t.Cleanup(func() { uowProvider = oldProvider })
	uowProvider = nil
	ctx := context.Background()
	const unopenable = "proxied-server UOW provider not initialized"

	var resolveErr error
	stderr := captureStderr(t, func() {
		_, resolveErr = proxiedResolvePartialID(ctx, "mol-x")
	})
	var reported reportedResolveError
	if !errors.As(resolveErr, &reported) {
		t.Fatalf("proxiedResolvePartialID error = %v, want a reportedResolveError", resolveErr)
	}
	if !strings.Contains(stderr, unopenable) {
		t.Errorf("proxiedResolvePartialID stderr = %q, want the open failure %q", stderr, unopenable)
	}

	stderr = captureStderr(t, func() {
		if got := moleculeNotResolved("mol-x", resolveErr); !errors.Is(got, reported.err) {
			t.Errorf("moleculeNotResolved = %v, want the already-reported %v", got, reported.err)
		}
	})
	if stderr != "" {
		t.Errorf("moleculeNotResolved reported an already-reported failure again: %q", stderr)
	}

	stderr = captureStderr(t, func() {
		_ = moleculeNotResolved("mol-x", errors.New("no issue found matching \"mol-x\""))
	})
	if !strings.Contains(stderr, "molecule 'mol-x' not found") {
		t.Errorf("an id the lookup did not find reported %q, want molecule 'mol-x' not found", stderr)
	}

	var showErr error
	stderr = captureStderr(t, func() { showErr = runMolShowProxiedServer(ctx, "mol-x") })
	if showErr == nil || !strings.Contains(stderr, unopenable) || strings.Contains(stderr, "not found") {
		t.Errorf("mol show over an unopenable read: err = %v, stderr = %q; want the open failure, not \"not found\"", showErr, stderr)
	}
}

// unopenableUOWProvider fails every NewUOW, as a proxied route's provider does
// when the server cannot be reached.
type unopenableUOWProvider struct{ err error }

func (p unopenableUOWProvider) NewUOW(context.Context) (uow.UnitOfWork, error) { return nil, p.err }
func (unopenableUOWProvider) Close(context.Context) error                      { return nil }

// TestProxiedReadyMolReportsAnUnopenableReadOnce pins `bd ready --mol` on the
// proxied route to the resolver contract above: a read unit of work that will
// not open is reported once, as itself, and is not followed by a "not found"
// for the molecule. Under --json that is one error document on stdout, not two.
func TestProxiedReadyMolReportsAnUnopenableReadOnce(t *testing.T) {
	oldProvider, oldJSON := uowProvider, jsonOutput
	t.Cleanup(func() { uowProvider, jsonOutput = oldProvider, oldJSON })
	const openFailure = "connection refused"
	uowProvider = unopenableUOWProvider{err: errors.New(openFailure)}
	ctx := context.Background()

	t.Run("text", func(t *testing.T) {
		jsonOutput = false
		var readyErr error
		stderr := captureStderr(t, func() {
			readyErr = runMoleculeReady(ctx, nil, proxiedResolvePartialID, "mol-x")
		})
		if readyErr == nil {
			t.Error("runMoleculeReady over an unopenable read returned nil")
		}
		if strings.Count(stderr, "Error:") != 1 || !strings.Contains(stderr, openFailure) || strings.Contains(stderr, "not found") {
			t.Errorf("stderr = %q; want one Error: line reporting the open failure, not \"not found\"", stderr)
		}
	})

	t.Run("json", func(t *testing.T) {
		jsonOutput = true
		var readyErr error
		stdout := captureStdout(t, func() error {
			readyErr = runMoleculeReady(ctx, nil, proxiedResolvePartialID, "mol-x")
			return nil
		})
		if readyErr == nil {
			t.Error("runMoleculeReady over an unopenable read returned nil")
		}
		dec := json.NewDecoder(strings.NewReader(stdout))
		docs := 0
		for {
			var doc map[string]any
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("stdout is not a JSON document stream: %v\n%s", err, stdout)
			}
			docs++
		}
		if docs != 1 || !strings.Contains(stdout, openFailure) || strings.Contains(stdout, "not found") {
			t.Errorf("stdout carries %d JSON documents:\n%s\nwant one, reporting the open failure, not \"not found\"", docs, stdout)
		}
	})
}
