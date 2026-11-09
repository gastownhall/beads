//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/domain"
	internalissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// outputReadSpyStore is a real store that records the reads a command's text
// output may skip: the label, blocking-annotation, dependency-record and issue
// reads, and every update with whether an issue read followed it.
// beforeUpdate, when set, runs once before the first update reaches the store,
// standing in for another process writing between the command's read and its
// write.
type outputReadSpyStore struct {
	storage.DoltStorage
	labelReads         int
	blockingAnnotators int
	depRecordReads     int
	issueReads         int
	issueReadsAfter    int
	wrote              bool
	updates            []map[string]interface{}
	beforeUpdate       func(ctx context.Context, id string)
	// afterGetIssue, when set, runs once after the first issue read returns,
	// standing in for another process writing between that read and the
	// command's render.
	afterGetIssue func(ctx context.Context, id string)
}

func (s *outputReadSpyStore) GetLabels(ctx context.Context, id string) ([]string, error) {
	s.labelReads++
	return s.DoltStorage.GetLabels(ctx, id)
}

func (s *outputReadSpyStore) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	s.blockingAnnotators++
	return s.DoltStorage.BlockingAnnotator()
}

func (s *outputReadSpyStore) GetDependencyRecordsForIssues(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	s.depRecordReads++
	return s.DoltStorage.GetDependencyRecordsForIssues(ctx, ids)
}

func (s *outputReadSpyStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	s.issueReads++
	if s.wrote {
		s.issueReadsAfter++
	}
	issue, err := s.DoltStorage.GetIssue(ctx, id)
	if hook := s.afterGetIssue; hook != nil {
		s.afterGetIssue = nil
		hook(ctx, id)
	}
	return issue, err
}

func (s *outputReadSpyStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if hook := s.beforeUpdate; hook != nil {
		s.beforeUpdate = nil
		hook(ctx, id)
	}
	s.updates = append(s.updates, updates)
	s.wrote = true
	return s.DoltStorage.UpdateIssue(ctx, id, updates, actor)
}

func (s *outputReadSpyStore) reset() {
	*s = outputReadSpyStore{DoltStorage: s.DoltStorage}
}

// useOutputReadSpy opens a Dolt server store, installs a spy over it as the
// command store, and restores every global the commands under test touch.
func useOutputReadSpy(t *testing.T, prefix string) (*dolt.DoltStore, *outputReadSpyStore) {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	inner := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), ".beads", "beads.db"), prefix)
	// The migration-freeze probe and the last-touched record both walk from
	// BEADS_DIR; keep them in an empty directory.
	t.Setenv("BEADS_DIR", t.TempDir())

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("output.title-length", defaultTitleLength)
	t.Cleanup(config.ResetForTesting)

	oldCtx, oldJSON, oldQuiet, oldProxied, oldActor := rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() {
		rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = oldCtx, oldJSON, oldQuiet, oldProxied, oldActor
		commandDidWrite.Store(oldDidWrite)
	})

	spy := &outputReadSpyStore{DoltStorage: inner}
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = spy, t.Context(), false, false, false, "test"
	return inner, spy
}

// commandWithFlags clones src's flag definitions onto a fresh command and
// parses args against them, so a flag set here cannot leak into src.
func commandWithFlags(t *testing.T, src *cobra.Command, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: src.Use}
	src.Flags().VisitAll(func(f *pflag.Flag) {
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
		case "stringArray":
			cmd.Flags().StringArray(f.Name, nil, f.Usage)
		default:
			t.Fatalf("--%s has unhandled flag type %q", f.Name, f.Value.Type())
		}
	})
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

func createOutputReadIssue(t *testing.T, s *dolt.DoltStore, issue *types.Issue, labels ...string) {
	t.Helper()
	ctx := t.Context()
	if issue.Status == "" {
		issue.Status = types.StatusOpen
	}
	if issue.IssueType == "" {
		issue.IssueType = types.TypeTask
	}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
	}
	for _, label := range labels {
		if err := s.AddLabel(ctx, issue.ID, label, "tester"); err != nil {
			t.Fatalf("AddLabel(%s, %s): %v", issue.ID, label, err)
		}
	}
}

// TestShowTextTakesLabelsFromTheIssueRead pins that `bd show`'s text output
// prints the labels its issue read already returned, with no label read of its
// own, and that for an ID present in both issues and wisps those are the labels
// of the durable row whose body the command prints. The labels are as of the
// issue read: a label change another process commits after it is not shown.
func TestShowTextTakesLabelsFromTheIssueRead(t *testing.T) {
	inner, spy := useOutputReadSpy(t, "sk")
	createOutputReadIssue(t, inner, &types.Issue{ID: "sk-lab", Title: "Labeled", Priority: 2}, "alpha-label", "beta-label")

	t.Run("no label read", func(t *testing.T) {
		spy.reset()
		out := captureStdout(t, func() error { return showCmd.RunE(commandWithFlags(t, showCmd), []string{"sk-lab"}) })
		if spy.labelReads != 0 {
			t.Errorf("bd show read the labels %d more times after its issue read", spy.labelReads)
		}
		for _, want := range []string{"alpha-label", "beta-label"} {
			if !strings.Contains(out, want) {
				t.Errorf("bd show output lacks label %q:\n%s", want, out)
			}
		}
	})

	t.Run("labels as of the issue read", func(t *testing.T) {
		createOutputReadIssue(t, inner, &types.Issue{ID: "sk-race", Title: "Raced", Priority: 2}, "before-label")
		spy.reset()
		spy.afterGetIssue = func(ctx context.Context, id string) {
			if err := inner.AddLabel(ctx, id, "after-label", "other"); err != nil {
				t.Errorf("concurrent label add: %v", err)
			}
		}
		out := captureStdout(t, func() error { return showCmd.RunE(commandWithFlags(t, showCmd), []string{"sk-race"}) })
		if spy.afterGetIssue != nil {
			t.Fatal("bd show made no issue read for the concurrent label add to follow")
		}
		if spy.labelReads != 0 {
			t.Errorf("bd show read the labels %d more times after its issue read", spy.labelReads)
		}
		if !strings.Contains(out, "before-label") {
			t.Errorf("bd show lacks the label its issue read returned:\n%s", out)
		}
		if strings.Contains(out, "after-label") {
			t.Errorf("bd show printed a label added after its issue read:\n%s", out)
		}
	})

	t.Run("overlapping id shows the durable labels", func(t *testing.T) {
		createOutputReadIssue(t, inner, &types.Issue{ID: "sk-ovl", Title: "Durable body", Priority: 2}, "durable-label")
		db := inner.UnderlyingDB()
		if _, err := db.ExecContext(t.Context(),
			"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) "+
				"VALUES (?, 'Shadow wisp', '', '', '', '', 'open', 2, 'task', 1)", "sk-ovl"); err != nil {
			t.Fatalf("seed shadow wisp: %v", err)
		}
		if _, err := db.ExecContext(t.Context(),
			"INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'wisp-label')", "sk-ovl"); err != nil {
			t.Fatalf("seed shadow wisp label: %v", err)
		}

		spy.reset()
		out := captureStdout(t, func() error { return showCmd.RunE(commandWithFlags(t, showCmd), []string{"sk-ovl"}) })
		if !strings.Contains(out, "Durable body") {
			t.Fatalf("bd show printed another body than the durable row's:\n%s", out)
		}
		if !strings.Contains(out, "durable-label") {
			t.Errorf("bd show lacks the durable row's label:\n%s", out)
		}
		if strings.Contains(out, "wisp-label") {
			t.Errorf("bd show printed the shadow wisp's label beside the durable body:\n%s", out)
		}
	})
}

// TestListLongSkipsBlockingAnnotation pins that the flat `bd list --long`
// rendering, which prints no blocking decoration, does not read it, while the
// compact and agent renderings that print it still do.
func TestListLongSkipsBlockingAnnotation(t *testing.T) {
	inner, spy := useOutputReadSpy(t, "ll")
	createOutputReadIssue(t, inner, &types.Issue{ID: "ll-a", Title: "Blocker", Priority: 1})
	createOutputReadIssue(t, inner, &types.Issue{ID: "ll-b", Title: "Blocked", Priority: 2})
	if err := inner.AddDependency(t.Context(), &types.Dependency{IssueID: "ll-b", DependsOnID: "ll-a", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	for _, tc := range []struct {
		name      string
		agent     bool
		args      []string
		wantReads int
	}{
		{"flat long", false, []string{"--flat", "--long", "--no-pager"}, 0},
		{"flat compact", false, []string{"--flat", "--no-pager"}, 1},
		{"agent long", true, []string{"--flat", "--long", "--no-pager"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.agent {
				t.Setenv("BD_AGENT_MODE", "1")
			} else {
				t.Setenv("BD_AGENT_MODE", "")
				t.Setenv("CLAUDE_CODE", "")
			}
			spy.reset()
			out := captureStdout(t, func() error { return runListCore(newListFlagsCommand(t, tc.args...), nil) })
			if spy.blockingAnnotators != tc.wantReads {
				t.Errorf("bd list %v read the blocking annotation %d times, want %d", tc.args, spy.blockingAnnotators, tc.wantReads)
			}
			for _, id := range []string{"ll-a", "ll-b"} {
				if !strings.Contains(out, id) {
					t.Errorf("bd list %v output lacks %s:\n%s", tc.args, id, out)
				}
			}
		})
	}
}

// TestReadyPlainSkipsParentEpicReads pins that `bd ready --plain`, which
// prints no parent epic, does not read the parent edges or the parents, while
// the pretty rendering that prints the epic title still does.
func TestReadyPlainSkipsParentEpicReads(t *testing.T) {
	inner, spy := useOutputReadSpy(t, "rp")
	createOutputReadIssue(t, inner, &types.Issue{ID: "rp-epic", Title: "Parent epic title", Priority: 1, IssueType: types.TypeEpic})
	createOutputReadIssue(t, inner, &types.Issue{ID: "rp-child", Title: "Child task", Priority: 2})
	if err := inner.AddDependency(t.Context(), &types.Dependency{IssueID: "rp-child", DependsOnID: "rp-epic", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	t.Run("plain", func(t *testing.T) {
		spy.reset()
		out := captureStdout(t, func() error { return readyCmd.RunE(newReadyFlagsCommand(t, "--plain"), nil) })
		if spy.depRecordReads != 0 || spy.issueReads != 0 {
			t.Errorf("bd ready --plain read %d dependency-record sets and %d parent issues, want none", spy.depRecordReads, spy.issueReads)
		}
		if !strings.Contains(out, "rp-child") {
			t.Errorf("bd ready --plain output lacks rp-child:\n%s", out)
		}
	})

	t.Run("pretty", func(t *testing.T) {
		spy.reset()
		out := captureStdout(t, func() error { return readyCmd.RunE(newReadyFlagsCommand(t), nil) })
		if spy.depRecordReads != 1 {
			t.Errorf("bd ready read %d dependency-record sets, want 1", spy.depRecordReads)
		}
		if !strings.Contains(out, "Parent epic title") {
			t.Errorf("bd ready output lacks the parent epic's title:\n%s", out)
		}
	})
}

// proxiedEpicReadSpy is a unit of work serving one ready child of one epic and
// counting the reads the parent-epic column costs.
type proxiedEpicReadSpy struct {
	uow.UnitOfWork
	depReads   int
	issueReads int
}

func (s *proxiedEpicReadSpy) IssueUseCase() domain.IssueUseCase {
	return proxiedEpicIssues{spy: s}
}

func (s *proxiedEpicReadSpy) DependencyUseCase() domain.DependencyUseCase {
	return proxiedEpicDeps{spy: s}
}

type proxiedEpicIssues struct {
	domain.IssueUseCase
	spy *proxiedEpicReadSpy
}

func (u proxiedEpicIssues) GetReadyWork(context.Context, types.WorkFilter) (domain.SearchPage, error) {
	return domain.SearchPage{Items: []*types.Issue{{ID: "rp-child", Title: "Child task", Priority: 2, IssueType: types.TypeTask}}}, nil
}

func (u proxiedEpicIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	u.spy.issueReads++
	return &types.Issue{ID: id, Title: "Parent epic title", IssueType: types.TypeEpic}, nil
}

type proxiedEpicDeps struct {
	domain.DependencyUseCase
	spy *proxiedEpicReadSpy
}

func (u proxiedEpicDeps) GetForIssueIDs(context.Context, []string) (map[string][]*types.Dependency, error) {
	u.spy.depReads++
	return map[string][]*types.Dependency{
		"rp-child": {{IssueID: "rp-child", DependsOnID: "rp-epic", Type: types.DepParentChild}},
	}, nil
}

// TestProxiedReadyPlainSkipsParentEpicReads is
// TestReadyPlainSkipsParentEpicReads for the proxied route, which builds the
// same column through the unit of work.
func TestProxiedReadyPlainSkipsParentEpicReads(t *testing.T) {
	for _, tt := range []struct {
		name           string
		in             readyInput
		wantDepReads   int
		wantIssueReads int
		wantInOutput   string
	}{
		{name: "plain", in: readyInput{plainFormat: true}, wantInOutput: "rp-child"},
		{name: "pretty turned off", in: readyInput{}, wantInOutput: "rp-child"},
		{name: "pretty", in: readyInput{prettyFormat: true}, wantDepReads: 1, wantIssueReads: 1, wantInOutput: "Parent epic title"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spy := &proxiedEpicReadSpy{}
			out := captureStdout(t, func() error { return runReadyProxiedList(t.Context(), spy, tt.in) })
			if spy.depReads != tt.wantDepReads || spy.issueReads != tt.wantIssueReads {
				t.Errorf("read %d dependency sets and %d parents, want %d and %d", spy.depReads, spy.issueReads, tt.wantDepReads, tt.wantIssueReads)
			}
			if !strings.Contains(out, tt.wantInOutput) {
				t.Errorf("output lacks %q:\n%s", tt.wantInOutput, out)
			}
		})
	}
}

// TestPriorityAndNoteTextTakeTitleFromTheResolvedIssue pins that the text
// confirmation of `bd priority` and `bd note` prints the title of the issue the
// command resolved before its write, with no read after the write. The title is
// as of that read: a rename another process commits between the read and the
// write shows the old title. --json still reads the issue after the write and
// shows the rename.
func TestPriorityAndNoteTextTakeTitleFromTheResolvedIssue(t *testing.T) {
	inner, spy := useOutputReadSpy(t, "pn")

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args func(id string) []string
	}{
		{"priority", priorityCmd, func(id string) []string { return []string{id, "1"} }},
		{"note", noteCmd, func(id string) []string { return []string{id, "a note"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "pn-" + tc.name
			createOutputReadIssue(t, inner, &types.Issue{ID: id, Title: "Original title", Priority: 2})
			rename := func(ctx context.Context, id string) {
				if err := inner.UpdateIssue(ctx, id, map[string]interface{}{"title": "Renamed concurrently"}, "other"); err != nil {
					t.Errorf("concurrent rename: %v", err)
				}
			}

			spy.reset()
			spy.beforeUpdate = rename
			jsonOutput = false
			out := captureStdout(t, func() error { return tc.cmd.RunE(commandWithFlags(t, tc.cmd), tc.args(id)) })
			if spy.issueReadsAfter != 0 {
				t.Errorf("bd %s read the issue %d times after its write", tc.name, spy.issueReadsAfter)
			}
			if !strings.Contains(out, id+" — Original title") {
				t.Errorf("bd %s text output does not name the title as read before the write:\n%s", tc.name, out)
			}

			createOutputReadIssue(t, inner, &types.Issue{ID: id + "-json", Title: "Original title", Priority: 2})
			spy.reset()
			spy.beforeUpdate = rename
			jsonOutput = true
			out = captureStdout(t, func() error { return tc.cmd.RunE(commandWithFlags(t, tc.cmd), tc.args(id+"-json")) })
			jsonOutput = false
			if spy.issueReadsAfter != 1 {
				t.Errorf("bd %s --json read the issue %d times after its write, want 1", tc.name, spy.issueReadsAfter)
			}
			if !strings.Contains(out, "Renamed concurrently") {
				t.Errorf("bd %s --json does not show the issue as read after the write:\n%s", tc.name, out)
			}
		})
	}
}

// TestDeferReasonAppendsInsideTheUpdate pins that `bd defer --reason` hands the
// reason to the update as a notes append, resolved against the row the update
// transaction reads, with no notes read of its own. A note another process
// commits between the command's resolution and its write survives, and the
// reason is joined to existing notes with one newline.
func TestDeferReasonAppendsInsideTheUpdate(t *testing.T) {
	inner, spy := useOutputReadSpy(t, "dr")

	for _, tc := range []struct {
		name       string
		notes      string
		concurrent bool
		want       string
	}{
		{"no existing notes", "", false, "waiting on API access"},
		{"existing notes", "existing context", false, "existing context\nwaiting on API access"},
		{"concurrent note survives", "existing context", true, "existing context\nconcurrent note\nwaiting on API access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "dr-" + strings.ReplaceAll(tc.name, " ", "-")
			createOutputReadIssue(t, inner, &types.Issue{ID: id, Title: "Defer target", Priority: 2, Notes: tc.notes})

			spy.reset()
			if tc.concurrent {
				spy.beforeUpdate = func(ctx context.Context, id string) {
					if err := inner.UpdateIssue(ctx, id, map[string]interface{}{internalissueops.OpAppendNotes: "concurrent note"}, "other"); err != nil {
						t.Errorf("concurrent note: %v", err)
					}
				}
			}
			cmd := commandWithFlags(t, deferCmd, "--reason", "waiting on API access")
			captureStdout(t, func() error { return deferCmd.RunE(cmd, []string{id}) })

			if spy.issueReads != 0 {
				t.Errorf("bd defer --reason read the issue %d times", spy.issueReads)
			}
			if len(spy.updates) != 1 {
				t.Fatalf("bd defer made %d updates, want 1", len(spy.updates))
			}
			if _, replaced := spy.updates[0]["notes"]; replaced {
				t.Errorf("bd defer replaced the notes column: %v", spy.updates[0])
			}
			if got := spy.updates[0][internalissueops.OpAppendNotes]; got != "waiting on API access" {
				t.Errorf("bd defer append = %v, want the reason", got)
			}

			got, err := inner.GetIssue(t.Context(), id)
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if got.Status != types.StatusDeferred {
				t.Errorf("status = %s, want deferred", got.Status)
			}
			if got.Notes != tc.want {
				t.Errorf("notes = %q, want %q", got.Notes, tc.want)
			}
		})
	}
}
