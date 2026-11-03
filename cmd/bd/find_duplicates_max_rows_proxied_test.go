package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/types"
)

// runFindDuplicatesCaptured runs findDuplicatesCmd.RunE with the flags the
// command reads, capturing both streams: the report prints to os.Stdout via
// fmt (reportFindDuplicates), and the cap diagnosis prints to os.Stderr, so
// `got`/`stderr` are everything the invocation rendered on either side.
func runFindDuplicatesCaptured(t *testing.T, args ...string) (error, string, string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	oldRootCtx := rootCtx
	rootCtx = context.Background()
	defer func() { rootCtx = oldRootCtx }()

	cmd := &cobra.Command{Use: "find-duplicates"}
	cmd.Flags().String("method", "mechanical", "detection method")
	cmd.Flags().Float64("threshold", 0.5, "similarity threshold")
	cmd.Flags().StringP("status", "s", "", "status filter")
	cmd.Flags().IntP("limit", "n", 50, "pair limit")
	cmd.Flags().String("model", "", "AI model")
	cmd.Flags().Int("max-rows", 0, "defensive row cap")
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse find-duplicates args %v: %v", args, err)
	}

	oldStdout := os.Stdout
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStderr := os.Stderr
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = wOut
	os.Stderr = wErr
	var outBuf, errBuf bytes.Buffer
	outDone := make(chan struct{})
	go func() { _, _ = io.Copy(&outBuf, rOut); close(outDone) }()
	errDone := make(chan struct{})
	go func() { _, _ = io.Copy(&errBuf, rErr); close(errDone) }()
	runErr := findDuplicatesCmd.RunE(cmd, cmd.Flags().Args())
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	<-outDone
	<-errDone
	_ = rOut.Close()
	_ = rErr.Close()
	return runErr, outBuf.String(), errBuf.String()
}

// stubFindDuplicatesIssue builds an issue whose title and description both
// carry text, so the mechanical method has tokens to compare.
func stubFindDuplicatesIssue(id string, status types.Status, title, description string) *types.Issue {
	return &types.Issue{ID: id, Title: title, Description: description, Status: status}
}

// TestFindDuplicatesHonorsMaxRowsUnderProxiedServer is the find-duplicates half
// of bd-7fnvhp: a proxied search-matching set honors the cap rather than
// refusing it. The cap binds the search itself (searchWindowForFilter), so cap
// 2 over three similar open issues exits 2 naming its source with nothing
// rendered, cap 3 reports exactly the uncapped invocation's pairs, and — the
// direct route's documented ordering — a closed row the search matched counts
// against the cap even though the default closed-row removal runs afterward.
func TestFindDuplicatesHonorsMaxRowsUnderProxiedServer(t *testing.T) {
	similar := []*types.Issue{
		stubFindDuplicatesIssue("bd-fd1", types.StatusOpen,
			"Fix login page timeout error", "The login page throws a timeout error after 30 seconds"),
		stubFindDuplicatesIssue("bd-fd2", types.StatusOpen,
			"Fix login page timeout error now", "The login page throws a timeout error after 30 seconds idle"),
		stubFindDuplicatesIssue("bd-fd3", types.StatusOpen,
			"Fix login page timeout error soon", "The login page throws a timeout error after 30 seconds of idling"),
	}

	for _, tc := range []struct {
		name       string
		args       []string
		env        string
		wantSource string
	}{
		{name: "cap_over_flag", args: []string{"--max-rows", "2"}, wantSource: "--max-rows"},
		{name: "cap_over_env", env: "2", wantSource: maxRowsEnvVar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			stub := withStubbedProxiedGraph(t, similar, nil)

			err, got, stderr := runFindDuplicatesCaptured(t, tc.args...)
			if code, ok := exitCodeFromError(err); !ok || code != 2 {
				t.Fatalf("exit = %v (code=%d typed=%v), want 2 (stderr=%q)", err, code, ok, stderr)
			}
			if got != "" {
				t.Errorf("stdout = %q, want nothing rendered under a refused cap", got)
			}
			wantFirst := "Error: too many rows: 3 found, " + tc.wantSource + "=2 exceeded."
			if first := strings.SplitN(stderr, "\n", 2)[0]; first != wantFirst {
				t.Errorf("stderr first line = %q, want %q", first, wantFirst)
			}
			if strings.Contains(stderr, "not supported in proxied-server mode") {
				t.Errorf("the stale unsupported-cap refusal still fired: %q", stderr)
			}
			if len(stub.filters) != 1 || stub.filters[0].MaxRows != 2 || stub.filters[0].MaxRowsSource != tc.wantSource {
				t.Errorf("search filters = %+v, want one search carrying cap 2 from %s",
					stub.filters, tc.wantSource)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "cap_fits_flag", args: []string{"--max-rows", "3"}},
		{name: "cap_fits_env", env: "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			withStubbedProxiedGraph(t, similar, nil)

			// The uncapped invocation first: its report is the byte-identical
			// target, and it proves the set really produces pairs.
			baseErr, want, baseStderr := runFindDuplicatesCaptured(t)
			if baseErr != nil {
				t.Fatalf("uncapped find-duplicates failed: %v (stderr=%q)", baseErr, baseStderr)
			}
			if !strings.Contains(want, "3 potential duplicate pair") {
				t.Fatalf("test setup: expected the similar set to produce 3 pairs, got:\n%s", want)
			}
			err, got, stderr := runFindDuplicatesCaptured(t, tc.args...)
			if err != nil {
				t.Fatalf("find-duplicates under a cap the set fits inside failed: %v (stderr=%q)", err, stderr)
			}
			if got != want {
				t.Errorf("capped report differs from the uncapped one:\ncapped:\n%s\nuncapped:\n%s", got, want)
			}
		})
	}

	// Two open issues plus one the default search also matches but the
	// closed-row removal drops afterward. The cap counts the closed row —
	// the search matched three — so cap 2 refuses while cap 3 succeeds and
	// reports the two open issues' pair without the closed one.
	openClosed := []*types.Issue{
		stubFindDuplicatesIssue("bd-fo1", types.StatusOpen,
			"Fix login page timeout error", "The login page throws a timeout error"),
		stubFindDuplicatesIssue("bd-fo2", types.StatusOpen,
			"Fix login page timeout error again", "The login page throws a timeout error"),
		stubFindDuplicatesIssue("bd-fc1", types.StatusClosed,
			"Fix login page timeout error old", "The login page throws a timeout error"),
	}

	t.Run("closed_row_counts_before_removal", func(t *testing.T) {
		withStubbedProxiedGraph(t, openClosed, nil)
		err, got, stderr := runFindDuplicatesCaptured(t, "--max-rows", "2")
		if code, ok := exitCodeFromError(err); !ok || code != 2 {
			t.Fatalf("exit = %v (code=%d typed=%v), want 2: closed rows must count against the cap (stderr=%q stdout=%q)",
				err, code, ok, stderr, got)
		}
		if got != "" {
			t.Errorf("stdout = %q, want nothing rendered under a refused cap", got)
		}
		wantFirst := "Error: too many rows: 3 found, --max-rows=2 exceeded."
		if first := strings.SplitN(stderr, "\n", 2)[0]; first != wantFirst {
			t.Errorf("stderr first line = %q, want %q", first, wantFirst)
		}
	})

	t.Run("cap_over_closed_set_fits_after_removal", func(t *testing.T) {
		withStubbedProxiedGraph(t, openClosed, nil)
		err, got, stderr := runFindDuplicatesCaptured(t, "--max-rows", "3")
		if err != nil {
			t.Fatalf("cap 3 over the two-open-plus-closed set failed: %v (stderr=%q)", err, stderr)
		}
		if !strings.Contains(got, "1 potential duplicate pair") {
			t.Errorf("expected the two open issues to be reported as one pair, got:\n%s", got)
		}
		if strings.Contains(got, "bd-fc1") || strings.Contains(got, "(old)") {
			t.Errorf("the closed row survived the default closed-row removal:\n%s", got)
		}
	})

	// The negative flag is a usage error raised in RunE, ahead of any search:
	// a passing validation would open a UOW, and the nil provider means no
	// store can be reached to prove it.
	t.Run("negative_cap_precedes_search", func(t *testing.T) {
		pinProxiedServerMode(t)
		if uowProvider != nil {
			t.Fatal("precondition: uowProvider must be nil so the run cannot reach a search")
		}
		err, got, stderr := runFindDuplicatesCaptured(t, "--max-rows", "-1")
		assertExitCode(t, err, 1)
		if !strings.Contains(stderr, "--max-rows must be non-negative") {
			t.Fatalf("stderr = %q, want the negative-cap usage error", stderr)
		}
		if got != "" {
			t.Errorf("stdout = %q, want nothing rendered under a refused flag", got)
		}
	})
}