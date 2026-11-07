package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// migrateLabelStubStore is the store `bd migrate issues` reads and writes
// through, reduced to the calls a --include none migration makes: the repo
// existence probes and the candidate scan (SearchIssues), the orphan check's
// dependency read, and the migration transaction. It records every search
// filter and transaction, and the transaction never runs its body, so a test
// can see how far a run got without a Dolt server. The embedded nil
// DoltStorage makes any other call panic.
type migrateLabelStubStore struct {
	storage.DoltStorage
	searches     []types.IssueFilter
	transactions int
}

func (s *migrateLabelStubStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.searches = append(s.searches, filter)
	if filter.SourceRepo == nil {
		return nil, nil
	}
	return []*types.Issue{{ID: "mig-1", Title: "migration candidate"}}, nil
}

func (s *migrateLabelStubStore) GetAllDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	return map[string][]*types.Dependency{}, nil
}

func (s *migrateLabelStubStore) RunInTransaction(context.Context, string, func(storage.Transaction) error) error {
	s.transactions++
	return nil
}

// candidateSearches returns the candidate scans: the SourceRepo searches with
// no Limit, as opposed to validateRepos' Limit 1 existence probes.
func (s *migrateLabelStubStore) candidateSearches() []types.IssueFilter {
	var out []types.IssueFilter
	for _, f := range s.searches {
		if f.SourceRepo != nil && f.Limit == 0 {
			out = append(out, f)
		}
	}
	return out
}

// newMigrateIssuesFlagsCommand clones migrateIssuesCmd's flag definitions
// onto a fresh command and parses args against them, for
// newReadyFlagsCommand's reason. The hidden migrate-issues alias is a value
// copy of migrateIssuesCmd and shares its FlagSet, so a test that parsed into
// either real command would leak state into both.
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
			if f.DefValue != "[]" {
				t.Fatalf("--%s has a non-empty slice default %q, which this clone does not reproduce", f.Name, f.DefValue)
			}
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

// migrateIssuesRunEs is both spellings of the command: `bd migrate issues` and
// the hidden root-level `bd migrate-issues` alias, which shares its RunE.
func migrateIssuesRunEs(t *testing.T) map[string]func(*cobra.Command, []string) error {
	t.Helper()
	runEs := map[string]func(*cobra.Command, []string) error{"migrate issues": migrateIssuesCmd.RunE}
	for _, c := range rootCmd.Commands() {
		if c.Name() == "migrate-issues" {
			runEs["migrate-issues"] = c.RunE
		}
	}
	if runEs["migrate-issues"] == nil {
		t.Fatal("hidden migrate-issues alias is not registered on rootCmd")
	}
	return runEs
}

// setupMigrateLabelStub installs a fresh recording store and pins every global
// the migration RunE reads or writes, restoring them afterwards. BEADS_DIR is
// an empty workspace so CheckReadonly's freeze lookup finds no marker.
func setupMigrateLabelStub(t *testing.T, jsonMode bool) *migrateLabelStubStore {
	t.Helper()

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	stub := &migrateLabelStubStore{}
	oldStore, oldCtx, oldReadonly, oldCommitted := store, rootCtx, readonlyMode, commandDidExplicitDoltCommit
	store, rootCtx, readonlyMode = stub, context.Background(), false
	t.Cleanup(func() {
		store, rootCtx, readonlyMode, commandDidExplicitDoltCommit = oldStore, oldCtx, oldReadonly, oldCommitted
	})
	pinJSONOutput(t, jsonMode)
	return stub
}

// runMigrateIssues runs one RunE against a cloned flag set and returns its
// streams and its error.
func runMigrateIssues(t *testing.T, runE func(*cobra.Command, []string) error, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newMigrateIssuesFlagsCommand(t, args...)
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	stdout, stderr = captureStdio(t, func() { err = runE(cmd, nil) })
	return stdout, stderr, err
}

// TestMigrateIssuesRefusesEmptyLabelFilter pins GH#6636 for migration: pflag
// parses --label "" to an empty slice, which findCandidateIssues read as "no
// label filter", so the candidate set became every issue in --from matching
// the other filters - and --yes or --json skip the confirmation that would
// have shown it. A supplied --label that normalizes to no labels must be
// refused before the store is touched, on both command spellings.
func TestMigrateIssuesRefusesEmptyLabelFilter(t *testing.T) {
	modes := []struct {
		name  string
		json  bool
		extra []string
	}{
		{"dry-run", false, []string{"--dry-run"}},
		{"text-yes", false, []string{"--yes"}},
		{"json-no-yes", true, nil},
	}
	for spelling, runE := range migrateIssuesRunEs(t) {
		for _, label := range []string{"", "  ", ",,", " , ,\t"} {
			for _, m := range modes {
				name := spelling + "/" + strconv.Quote(label) + "/" + m.name
				t.Run(name, func(t *testing.T) {
					stub := setupMigrateLabelStub(t, m.json)
					args := append([]string{"--from", "repo-a", "--to", "repo-b", "--label=" + label}, m.extra...)
					stdout, stderr, err := runMigrateIssues(t, runE, args...)

					var ee *exitError
					if !errors.As(err, &ee) || ee.Code != 1 {
						t.Fatalf("err = %v, want exit code 1 (stdout %q, stderr %q)", err, stdout, stderr)
					}
					const want = "--label was supplied but contains no usable label"
					shown := stdout + stderr
					if strings.Count(shown, want) != 1 {
						t.Errorf("output = %q, want the %q error exactly once", shown, want)
					}
					if m.json && stderr != "" {
						t.Errorf("stderr = %q, want the JSON error on stdout only", stderr)
					}
					if !m.json && stdout != "" {
						t.Errorf("stdout = %q, want the text error on stderr only", stdout)
					}
					if len(stub.searches) != 0 || stub.transactions != 0 {
						t.Errorf("store saw %d searches and %d transactions, want none: the refusal must precede any migration read or write",
							len(stub.searches), stub.transactions)
					}
					if strings.Contains(shown, "Migration Plan") || strings.Contains(shown, `"plan"`) {
						t.Errorf("output = %q, want no migration plan", shown)
					}
				})
			}
		}
	}
}
