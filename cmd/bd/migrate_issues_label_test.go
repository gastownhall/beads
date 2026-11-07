package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// newMigrateIssuesFlagsCommand clones migrateIssuesCmd's flag definitions onto
// a fresh command and parses args against it, for the reason
// newReadyFlagsCommand exists: the canonical `bd migrate issues` command and
// its hidden root-level alias are one shallow struct copy sharing one
// FlagSet, so setting a flag on either would leak into every later test in
// this package. Integer defaults are cloned through strconv.Atoi rather than
// pinned to 0, so --priority keeps its -1 "any priority" sentinel.
func newMigrateIssuesFlagsCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "issues"}
	migrateIssuesCmd.Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Value.Type() {
		case "bool":
			cmd.Flags().Bool(f.Name, f.DefValue == "true", f.Usage)
		case "int":
			n, err := strconv.Atoi(f.DefValue)
			if err != nil {
				t.Fatalf("--%s has non-integer default %q: %v", f.Name, f.DefValue, err)
			}
			cmd.Flags().Int(f.Name, n, f.Usage)
		case "string":
			cmd.Flags().String(f.Name, f.DefValue, f.Usage)
		case "stringSlice":
			cmd.Flags().StringSlice(f.Name, nil, f.Usage)
		default:
			t.Fatalf("--%s has unhandled flag type %q", f.Name, f.Value.Type())
		}
	})
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// migrateIssuesAliasCommand returns the hidden root-level `bd migrate-issues`
// compatibility alias. It is a shallow copy of migrateIssuesCmd registered on
// rootCmd in init(), sharing that command's RunE and FlagSet; driving it
// separately pins that the alias cannot dodge a fix written into the shared
// RunE.
func migrateIssuesAliasCommand(t *testing.T) *cobra.Command {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == "migrate-issues" {
			return c
		}
	}
	t.Fatal("root-level migrate-issues alias is not registered on rootCmd")
	return nil
}

// recordingMigrationStore is a DoltStorage test double for the migrate-issues
// input-gate tests. It records what a migration run reached — every
// SearchIssues filter and every transaction message — and answers the base
// reads safely rather than panicking: SearchIssues always returns one seeded
// issue (so the candidate set is non-empty and the pre-fix flow runs all the
// way to a plan, a confirmation bypass and a transaction), the dependency
// reads report no edges, and RunInTransaction records the message and
// succeeds without running the write closure. Embedding the interface (rather
// than a real store) keeps it usable from the pure-Go test lane; any method
// not overridden panics on the nil embed, which is exactly the loud failure a
// test that reached a new store call wants.
type recordingMigrationStore struct {
	storage.DoltStorage

	mu           sync.Mutex
	searches     []types.IssueFilter
	transactions []string
}

func (s *recordingMigrationStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.mu.Lock()
	s.searches = append(s.searches, filter)
	s.mu.Unlock()
	return []*types.Issue{{
		ID:        "bd-migseed1",
		Title:     "migrate-issues label gate seed",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		SourceRepo: "repo-from",
	}}, nil
}

func (s *recordingMigrationStore) RunInTransaction(_ context.Context, commitMsg string, _ func(tx storage.Transaction) error) error {
	s.mu.Lock()
	s.transactions = append(s.transactions, commitMsg)
	s.mu.Unlock()
	return nil
}

func (s *recordingMigrationStore) GetDependencyRecords(_ context.Context, _ string) ([]*types.Dependency, error) {
	return nil, nil
}

func (s *recordingMigrationStore) GetDependencyRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}

func (s *recordingMigrationStore) GetAllDependencyRecords(_ context.Context) (map[string][]*types.Dependency, error) {
	return nil, nil
}

func (s *recordingMigrationStore) GetDependents(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, nil
}

func (s *recordingMigrationStore) searchCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.searches)
}

func (s *recordingMigrationStore) transactionCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transactions)
}

// candidateFilter returns the SearchIssues filter of the candidate scan: the
// one query that pages the source repo with no row limit, as opposed to the
// two repo-existence probes (Limit == 1) and the orphan-existence check
// (no SourceRepo). Nil when no such query ran.
func (s *recordingMigrationStore) candidateFilter() *types.IssueFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.searches {
		f := s.searches[i]
		if f.Limit == 1 || f.SourceRepo == nil {
			continue
		}
		return &s.searches[i]
	}
	return nil
}

// setupMigrateIssuesTestStore installs the recording stub as the package's
// store and isolates the globals a direct RunE call touches: BEADS_DIR points
// at an empty temp directory so the migration-freeze lookup finds no marker
// (CheckReadonly exits the process otherwise), readonlyMode is pinned off,
// rootCtx gets a live context, and commandDidExplicitDoltCommit is reset.
func setupMigrateIssuesTestStore(t *testing.T) *recordingMigrationStore {
	t.Helper()
	saveAndRestoreGlobals(t)
	stub := &recordingMigrationStore{}
	store = stub

	t.Setenv("BEADS_DIR", t.TempDir())

	oldReadonly := readonlyMode
	readonlyMode = false
	t.Cleanup(func() { readonlyMode = oldReadonly })

	oldRootCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldRootCtx })

	oldCommit := commandDidExplicitDoltCommit
	commandDidExplicitDoltCommit = false
	t.Cleanup(func() { commandDidExplicitDoltCommit = oldCommit })

	return stub
}

// captureMigrateIssuesRunE calls a migrate-issues RunE with both streams
// captured, the way runGatherListInputSplit does for the gatherers: the
// refusal under test is printed by HandleErrorRespectJSON (stderr as text,
// stdout as a JSON object), and a pre-fix run prints a whole migration plan,
// so both streams have to be watchable separately.
func captureMigrateIssuesRunE(t *testing.T, runE func(*cobra.Command, []string) error, cmd *cobra.Command) (error, string, string) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = wOut, wErr

	drain := func(r *os.File) <-chan string {
		done := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r)
			done <- buf.String()
		}()
		return done
	}
	outDone, errDone := drain(rOut), drain(rErr)

	runErr := runE(cmd, nil)

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	stdout, stderr := <-outDone, <-errDone
	_ = rOut.Close()
	_ = rErr.Close()
	return runErr, stdout, stderr
}

// assertMigrateIssuesRefusal is the shared shape of every refusal row: exit
// code 1, the --label message on the stream the text/JSON convention says
// carries it, and NOTHING reached the store — no search, no transaction, no
// plan. Assertions are reported rather than fatal so a pre-fix run shows
// exactly how far the command got.
func assertMigrateIssuesRefusal(t *testing.T, err error, stdout, stderr string, stub *recordingMigrationStore, jsonOut bool) {
	t.Helper()
	const want = "--label was supplied but contains no usable label"

	if err == nil {
		t.Error("migrate-issues RunE(--label <blank>) = nil, want the --label refusal")
	} else {
		var ee *exitError
		if !errors.As(err, &ee) || ee.Code != 1 {
			t.Errorf("error = %v, want an exitError with code 1", err)
		}
	}

	if jsonOut {
		if stderr != "" {
			t.Errorf("under --json the refusal must not reach stderr, got:\n%s", stderr)
		}
		if err != nil {
			if msg := jsonErrorMessage(t, stdout); !strings.Contains(msg, want) {
				t.Errorf("stdout error = %q, want it to contain %q", msg, want)
			}
		}
	} else {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}

	if n := stub.searchCalls(); n != 0 {
		t.Errorf("SearchIssues ran %d time(s), want 0: the refusal must precede every migration store query", n)
	}
	if n := stub.transactionCalls(); n != 0 {
		t.Errorf("%d transaction(s) ran, want 0: the refusal must precede planning, confirmation and writes", n)
	}
	if out := stdout + stderr; strings.Contains(out, "Migration Plan") {
		t.Errorf("a migration plan was displayed despite the refusal:\n%s", out)
	}
}

// TestMigrateIssuesRefusesEmptyLabelFilter pins GH#6636 for `bd migrate
// issues` and its hidden root-level alias: pflag parses `--label ""` to an
// empty slice, which the candidate query read as "no label filter" —
// widening the migration candidate set to every issue in --from matching the
// other filters, behind the confirmation bypasses a script uses (--yes, or
// --json). Every blank spelling must be refused before any store query.
func TestMigrateIssuesRefusesEmptyLabelFilter(t *testing.T) {
	const (
		from = "repo-from"
		to   = "repo-to"
	)

	cases := []struct {
		name  string
		label string
	}{
		{"empty", ""},
		{"whitespace", "  "},
		{"comma only", ",,"},
		{"mixed blank csv", " , , "},
	}
	modes := []struct {
		name string
		args []string // extra args; the label is appended below
		json bool
	}{
		// The three arrival shapes a script uses. dry-run is the safe one;
		// text --yes and JSON without --yes are the two distinct
		// confirmation bypasses the broadened candidate set used to slip
		// through.
		{"dry run", []string{"--dry-run"}, false},
		{"text yes", []string{"--yes"}, false},
		{"json without yes", nil, true},
	}

	runCase := func(t *testing.T, runE func(*cobra.Command, []string) error, mode struct {
		name string
		args []string
		json bool
	}, label string) {
		t.Helper()
		pinJSONOutput(t, mode.json)
		stub := setupMigrateIssuesTestStore(t)

		args := []string{"--from", from, "--to", to, "--label", label}
		args = append(args, mode.args...)
		cmd := newMigrateIssuesFlagsCommand(t, args...)

		err, stdout, stderr := captureMigrateIssuesRunE(t, runE, cmd)
		assertMigrateIssuesRefusal(t, err, stdout, stderr, stub, mode.json)
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					runCase(t, migrateIssuesCmd.RunE, mode, tc.label)
				})
			}
		})
	}

	// The shallow-copy alias shares the canonical RunE today; driving it
	// keeps a future fork of that RunE from dodging the refusal.
	t.Run("root alias", func(t *testing.T) {
		alias := migrateIssuesAliasCommand(t)
		for _, mode := range modes {
			t.Run(mode.name, func(t *testing.T) {
				runCase(t, alias.RunE, mode, "")
			})
		}
	})
}
