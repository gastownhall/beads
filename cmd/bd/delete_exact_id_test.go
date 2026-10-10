package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// deleteExactStubStore is the store `bd delete` resolves and deletes through,
// reduced to the calls its direct route makes: the resolver's exact-id probe
// (SearchIssues with IDs) and its substring search (SearchIssueIDs), the
// prefix config, GetIssue, the preview's dependency reads, and Deleter. The
// embedded nil DoltStorage makes any other call panic, so a test that wanders
// off this path fails loudly instead of passing on a default. No cgo, no
// Dolt server: the same shape as bulkDepStubStore in dep_add_target_test.go.
type deleteExactStubStore struct {
	storage.DoltStorage
	ids     []string
	deleter *deleteRecordingDeleter
}

func (s *deleteExactStubStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, id := range s.ids {
		if len(filter.IDs) > 0 && !slices.Contains(filter.IDs, id) {
			continue
		}
		out = append(out, &types.Issue{ID: id, Title: "issue " + id})
	}
	return out, nil
}

// SearchIssueIDs models the id half of the real `id LIKE '%q%'` search. None
// of these issues is ephemeral, so the resolver's wisp-only fallback finds
// nothing, as the real wisps-then-issues search would.
func (s *deleteExactStubStore) SearchIssueIDs(_ context.Context, query string, filter types.IssueFilter) ([]string, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral {
		return nil, nil
	}
	var out []string
	for _, id := range s.ids {
		if strings.Contains(id, query) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *deleteExactStubStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "fx", nil
	}
	return "", nil
}

func (s *deleteExactStubStore) GetAllConfig(_ context.Context) (map[string]string, error) {
	return map[string]string{"issue_prefix": "fx"}, nil
}

func (s *deleteExactStubStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if slices.Contains(s.ids, id) {
		return &types.Issue{ID: id, Title: "issue " + id}, nil
	}
	return nil, storage.ErrNotFound
}

func (s *deleteExactStubStore) GetDependencies(context.Context, string) ([]*types.Issue, error) {
	return nil, nil
}

func (s *deleteExactStubStore) GetDependents(context.Context, string) ([]*types.Issue, error) {
	return nil, nil
}

func (s *deleteExactStubStore) GetDependencyRecords(context.Context, string) ([]*types.Dependency, error) {
	return nil, nil
}

func (s *deleteExactStubStore) Deleter() (issueops.Deleter, error) {
	return s.deleter, nil
}

// deleteRecordingDeleter records every request the front door hands the role,
// previews (DryRun) included: a preview that names an issue the caller never
// named prints the `bd delete <that id> --force` line the caller is told to run.
type deleteRecordingDeleter struct {
	requests []issueops.DeleteRequest
}

func (d *deleteRecordingDeleter) Delete(_ context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	d.requests = append(d.requests, req)
	return issueops.DeleteResult{DryRun: req.DryRun, Deleted: len(req.IDs)}, nil
}

// useDeleteExactStub installs a stub holding ids as the store `bd delete`
// reads, and restores every global the command touches.
func useDeleteExactStub(t *testing.T, ids ...string) *deleteExactStubStore {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	// The migration-freeze probe in CheckReadonly walks from BEADS_DIR; keep it
	// in an empty directory rather than whatever workspace the test runs in.
	t.Setenv("BEADS_DIR", t.TempDir())

	oldCtx, oldJSON, oldQuiet, oldProxied, oldActor := rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() {
		rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = oldCtx, oldJSON, oldQuiet, oldProxied, oldActor
		commandDidWrite.Store(oldDidWrite)
		commandDeletedIssueIDs.reset()
	})

	s := &deleteExactStubStore{ids: ids, deleter: &deleteRecordingDeleter{}}
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = s, context.Background(), false, false, false, "test"
	return s
}

// setDeleteFlag sets one of deleteCmd's flags for the length of the test.
func setDeleteFlag(t *testing.T, name, value string) {
	t.Helper()
	if err := deleteCmd.Flags().Set(name, value); err != nil {
		t.Fatalf("setting --%s: %v", name, err)
	}
	t.Cleanup(func() {
		flag := deleteCmd.Flags().Lookup(name)
		if err := flag.Value.Set(flag.DefValue); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
		flag.Changed = false
	})
}

// runDeleteCmd runs `bd delete` in-process against the installed stub and
// returns its stderr and error.
func runDeleteCmd(t *testing.T, args []string) (string, error) {
	t.Helper()
	var err error
	stderr := captureStderr(t, func() {
		err = deleteCmd.RunE(deleteCmd, args)
	})
	return stderr, err
}

// TestDeleteRefusesStaleIDThatPrefixesAnotherIssue pins that `bd delete`
// handed an id that no longer exists does not resolve it, by leading-prefix
// abbreviation, to a DIFFERENT issue whose id it happens to begin.
//
// The shape is ordinary, not engineered. Hash length is adaptive with store
// size (3 to 8 characters), so a store that grew holds older short ids beside
// newer long ones; `--id` makes any shape; and a hierarchical child's id always
// extends its parent's. Deleting the short id and then naming it again — a
// retried script, a re-run `--from-file` chunk — reached the survivor: measured
// on a fixture, `bd delete fx-t00003` after fx-t00003 was gone resolved to
// fx-t000031, and with --force deleted it.
//
// Every route through the direct front door is covered: the single-id path
// (delete.go's inline resolution), the preview (whose output is the
// `bd delete <id> --force` line a caller is told to run next), and the batch
// path that --from-file and more than one id take (deleteBatch). The proxied
// route never resolves (DeleteRequest.IDs are exact), so it is not here.
//
// An abbreviation of a live issue is refused the same way: the resolver cannot
// tell it from a stale full id. The refusal says delete needs the full id and
// names the ids the input begins, so the caller can retype one.
func TestDeleteRefusesStaleIDThatPrefixesAnotherIssue(t *testing.T) {
	cases := []struct {
		name     string
		stored   []string
		args     []string
		fromFile []string
		force    bool
		// wantNamed are the full ids the refusal must name.
		wantNamed []string
	}{
		{
			name:      "single id --force",
			stored:    []string{"fx-t000031"},
			args:      []string{"fx-t00003"},
			force:     true,
			wantNamed: []string{"fx-t000031"},
		},
		{
			name:      "single id preview",
			stored:    []string{"fx-t000031"},
			args:      []string{"fx-t00003"},
			wantNamed: []string{"fx-t000031"},
		},
		{
			name:      "deleted parent's id with an orphaned child --force",
			stored:    []string{"fx-ep1.1"},
			args:      []string{"fx-ep1"},
			force:     true,
			wantNamed: []string{"fx-ep1.1"},
		},
		{
			name:      "re-run of a finished --from-file chunk --force",
			stored:    []string{"fx-t000031", "fx-t000041"},
			fromFile:  []string{"fx-t00003", "fx-t00004"},
			force:     true,
			wantNamed: []string{"fx-t000031"},
		},
		{
			name:      "abbreviation of a live issue --force",
			stored:    []string{"fx-t000031"},
			args:      []string{"t000"},
			force:     true,
			wantNamed: []string{"fx-t000031"},
		},
		{
			name:      "abbreviation of two live issues",
			stored:    []string{"fx-t000031", "fx-t000041"},
			args:      []string{"t000"},
			wantNamed: []string{"fx-t000031", "fx-t000041"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := useDeleteExactStub(t, tc.stored...)
			if tc.force {
				setDeleteFlag(t, "force", "true")
			}
			named := append([]string{}, tc.args...)
			if len(tc.fromFile) > 0 {
				path := filepath.Join(t.TempDir(), "ids.txt")
				if err := os.WriteFile(path, []byte(strings.Join(tc.fromFile, "\n")+"\n"), 0o600); err != nil {
					t.Fatalf("writing --from-file list: %v", err)
				}
				setDeleteFlag(t, "from-file", path)
				named = append(named, tc.fromFile...)
			}

			stderr, err := runDeleteCmd(t, tc.args)

			for _, req := range s.deleter.requests {
				for _, id := range req.IDs {
					if !slices.Contains(named, id) {
						t.Errorf("bd delete %v (force=%v) handed the role %s (DryRun=%v), an issue it never named; stderr:\n%s",
							named, tc.force, id, req.DryRun, stderr)
					}
				}
			}
			if err == nil {
				t.Errorf("bd delete %v (force=%v) succeeded; want a refusal, since no stored issue has that exact id (store holds %v)",
					named, tc.force, tc.stored)
			}
			if !strings.Contains(stderr, "bd delete needs the full issue id") {
				t.Errorf("bd delete %v refusal does not say delete needs the full id; stderr:\n%s", named, stderr)
			}
			for _, id := range tc.wantNamed {
				if !strings.Contains(stderr, id) {
					t.Errorf("bd delete %v refusal does not name the full id %s; stderr:\n%s", named, id, stderr)
				}
			}
		})
	}
}

// TestDeleteExactIDsStillResolve is the over-refusal guard beside the test
// above: an id that exactly names an issue, with or without its prefix, alone
// or in a batch, still reaches the role as that issue and nothing else.
func TestDeleteExactIDsStillResolve(t *testing.T) {
	cases := []struct {
		name   string
		stored []string
		args   []string
		want   []string
	}{
		{name: "full id", stored: []string{"fx-t000031"}, args: []string{"fx-t000031"}, want: []string{"fx-t000031"}},
		{name: "full hash without prefix", stored: []string{"fx-t000031"}, args: []string{"t000031"}, want: []string{"fx-t000031"}},
		{name: "child id", stored: []string{"fx-ep1", "fx-ep1.1"}, args: []string{"fx-ep1.1"}, want: []string{"fx-ep1.1"}},
		{
			name:   "batch of full ids",
			stored: []string{"fx-t000031", "fx-t000041"},
			args:   []string{"fx-t000031", "fx-t000041"},
			want:   []string{"fx-t000031", "fx-t000041"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := useDeleteExactStub(t, tc.stored...)
			setDeleteFlag(t, "force", "true")

			stderr, err := runDeleteCmd(t, tc.args)
			if err != nil {
				t.Fatalf("bd delete %v --force: %v; stderr:\n%s", tc.args, err, stderr)
			}
			if len(s.deleter.requests) != 1 {
				t.Fatalf("bd delete %v --force made %d role requests; want 1", tc.args, len(s.deleter.requests))
			}
			req := s.deleter.requests[0]
			if req.DryRun || !req.Force {
				t.Errorf("request DryRun=%v Force=%v; want a confirmed delete", req.DryRun, req.Force)
			}
			if !slices.Equal(req.IDs, tc.want) {
				t.Errorf("bd delete %v --force deleted %v; want %v", tc.args, req.IDs, tc.want)
			}
		})
	}
}
