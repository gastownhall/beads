package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestTokenizeBatchLine(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{
			name: "simple",
			in:   "close bd-1 done",
			want: []string{"close", "bd-1", "done"},
		},
		{
			name: "tabs and spaces",
			in:   "close\tbd-1  done",
			want: []string{"close", "bd-1", "done"},
		},
		{
			name: "quoted with spaces",
			in:   `update bd-2 title="hello world"`,
			want: []string{"update", "bd-2", "title=hello world"},
		},
		{
			name: "escaped quote",
			in:   `create bug 1 "say \"hi\""`,
			want: []string{"create", "bug", "1", `say "hi"`},
		},
		{
			name: "escaped backslash",
			in:   `create bug 1 "back\\slash"`,
			want: []string{"create", "bug", "1", `back\slash`},
		},
		{
			name:    "unterminated quote",
			in:      `close bd-1 "oops`,
			wantErr: true,
		},
		{
			name: "empty string",
			in:   "",
			want: nil,
		},
		{
			name: "empty quoted",
			in:   `update bd-1 title=""`,
			want: []string{"update", "bd-1", "title="},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenizeBatchLine(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("tokenizeBatchLine(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tokenizeBatchLine(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseBatchScript(t *testing.T) {
	script := `# leading comment
# another
close bd-1 stale

update bd-2 status=in_progress priority=1
create task 2 "triage the backlog"
dep add bd-3 bd-4
dep add bd-3 bd-5 related
dep remove bd-6 bd-7
dep rm bd-8 bd-9
`
	ops, err := parseBatchScript(strings.NewReader(script))
	if err != nil {
		t.Fatalf("parseBatchScript: %v", err)
	}
	wantCmds := []string{
		"close",
		"update",
		"create",
		"dep.add",
		"dep.add",
		"dep.remove",
		"dep.remove",
	}
	if len(ops) != len(wantCmds) {
		t.Fatalf("got %d ops, want %d: %+v", len(ops), len(wantCmds), ops)
	}
	for i, op := range ops {
		if op.cmd != wantCmds[i] {
			t.Errorf("op %d: cmd = %q, want %q", i, op.cmd, wantCmds[i])
		}
		if op.line == 0 {
			t.Errorf("op %d: line number not set", i)
		}
	}

	// Spot check: create title is one token via quoting
	create := ops[2]
	if create.cmd != "create" {
		t.Fatalf("expected create, got %q", create.cmd)
	}
	if len(create.args) != 3 {
		t.Fatalf("create args = %v, want 3 tokens", create.args)
	}
	if create.args[2] != "triage the backlog" {
		t.Errorf("create title = %q, want %q", create.args[2], "triage the backlog")
	}

	// dep.add with type
	depWithType := ops[4]
	if depWithType.cmd != "dep.add" || len(depWithType.args) != 3 || depWithType.args[2] != "related" {
		t.Errorf("dep.add with type mismatch: %+v", depWithType)
	}
}

func TestParseBatchScript_Empty(t *testing.T) {
	ops, err := parseBatchScript(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseBatchScript empty: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("expected 0 ops, got %d", len(ops))
	}
}

func TestParseBatchScript_OnlyCommentsAndBlank(t *testing.T) {
	ops, err := parseBatchScript(strings.NewReader("# comment\n\n  \n# another\n"))
	if err != nil {
		t.Fatalf("parseBatchScript: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("expected 0 ops, got %d", len(ops))
	}
}

func TestParseBatchScript_UnsupportedCommand(t *testing.T) {
	_, err := parseBatchScript(strings.NewReader("show bd-1\n"))
	if err == nil {
		t.Fatal("expected error for unsupported command")
	}
	if !strings.Contains(err.Error(), "unsupported batch command") {
		t.Errorf("error should mention unsupported command, got: %v", err)
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error should include line number, got: %v", err)
	}
}

func TestParseBatchScript_UnsupportedCommandOnLaterLine(t *testing.T) {
	script := "close bd-1\nshow bd-2\n"
	_, err := parseBatchScript(strings.NewReader(script))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error should include line 2, got: %v", err)
	}
}

func TestParseBatchScript_DepRequiresSubcommand(t *testing.T) {
	_, err := parseBatchScript(strings.NewReader("dep\n"))
	if err == nil {
		t.Fatal("expected error for bare 'dep'")
	}
	if !strings.Contains(err.Error(), "subcommand") {
		t.Errorf("error should mention subcommand, got: %v", err)
	}
}

func TestParseBatchScript_DepUnknownSubcommand(t *testing.T) {
	_, err := parseBatchScript(strings.NewReader("dep list bd-1\n"))
	if err == nil {
		t.Fatal("expected error for unknown dep subcommand")
	}
	if !strings.Contains(err.Error(), "dep subcommand") {
		t.Errorf("error should mention dep subcommand, got: %v", err)
	}
}

func TestParseBatchScript_UnterminatedQuote(t *testing.T) {
	_, err := parseBatchScript(strings.NewReader(`create task 1 "oops`))
	if err == nil {
		t.Fatal("expected error for unterminated quote")
	}
}

func TestParseUpdateKVs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name: "status and priority",
			in:   []string{"status=in_progress", "priority=1"},
			want: map[string]interface{}{"status": "in_progress", "priority": 1},
		},
		{
			name: "title",
			in:   []string{"title=new title"},
			want: map[string]interface{}{"title": "new title"},
		},
		{
			name: "assignee blank allowed (unassign)",
			in:   []string{"assignee="},
			want: map[string]interface{}{"assignee": ""},
		},
		{
			name:    "unsupported key",
			in:      []string{"description=foo"},
			wantErr: true,
		},
		{
			name:    "missing equals",
			in:      []string{"status"},
			wantErr: true,
		},
		{
			name:    "invalid priority",
			in:      []string{"priority=high"},
			wantErr: true,
		},
		{
			name:    "empty status",
			in:      []string{"status="},
			wantErr: true,
		},
		{
			name:    "empty title",
			in:      []string{"title="},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUpdateKVs(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseUpdateKVs err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseUpdateKVs = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBatchDepAddEdge pins the edge a `dep add` line builds on both backends,
// and that the line asks the dotted-id hierarchy rule before either backend
// writes: the refusal is the role's own typed error, so a batch prints the
// sentence `bd dep add` prints and rolls back on it.
func TestBatchDepAddEdge(t *testing.T) {
	tooLong := strings.Repeat("x", types.MaxDependencyTypeLen+1)
	tests := []struct {
		name       string
		args       []string
		want       *types.Dependency
		wantErr    string
		wantDotted bool
	}{
		{
			name: "default type is blocks",
			args: []string{"bd-1", "bd-2"},
			want: &types.Dependency{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks},
		},
		{
			name: "explicit type",
			args: []string{"bd-1", "bd-2", "related"},
			want: &types.Dependency{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepRelated},
		},
		{
			name:    "missing to-id",
			args:    []string{"bd-1"},
			wantErr: "dep add requires <from-id> <to-id>",
		},
		{
			name:    "invalid type",
			args:    []string{"bd-1", "bd-2", tooLong},
			wantErr: `dep add: invalid dependency type "` + tooLong + `"`,
		},
		{
			name:       "dotted child gated on its own parent",
			args:       []string{"bd-abc.1", "bd-abc"},
			wantDotted: true,
		},
		{
			name:       "any explicit type up the tree, not only blocking ones",
			args:       []string{"bd-abc.1", "bd-abc", "related"},
			wantDotted: true,
		},
		{
			name:       "parent-child past the immediate parent",
			args:       []string{"bd-abc.1.2", "bd-abc", "parent-child"},
			wantDotted: true,
		},
		{
			name: "parent-child to the immediate parent is the hierarchy itself",
			args: []string{"bd-abc.1", "bd-abc", "parent-child"},
			want: &types.Dependency{IssueID: "bd-abc.1", DependsOnID: "bd-abc", Type: types.DepParentChild},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := batchDepAddEdge(tt.args)
			switch {
			case tt.wantDotted:
				var dotted *publicops.DottedChildDependencyError
				if !errors.As(err, &dotted) {
					t.Fatalf("batchDepAddEdge(%q) = %+v, %v; want *DottedChildDependencyError", tt.args, got, err)
				}
				if !errors.Is(err, publicops.ErrValidation) {
					t.Errorf("refusal %v does not match ErrValidation", err)
				}
				if dotted.IssueID != tt.args[0] || dotted.DependsOnID != tt.args[1] {
					t.Errorf("refusal names %s -> %s, want %s -> %s", dotted.IssueID, dotted.DependsOnID, tt.args[0], tt.args[1])
				}
			case tt.wantErr != "":
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("batchDepAddEdge(%q) err = %v, want %q", tt.args, err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("batchDepAddEdge(%q): %v", tt.args, err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("batchDepAddEdge(%q) = %+v, want %+v", tt.args, got, tt.want)
				}
			}
		})
	}
}

func TestBatchCmd_Registered(t *testing.T) {
	// Ensure 'bd batch' is wired into rootCmd with the correct group.
	cmd, _, err := rootCmd.Find([]string{"batch"})
	if err != nil {
		t.Fatalf("rootCmd.Find batch: %v", err)
	}
	if cmd == nil || cmd.Name() != "batch" {
		t.Fatalf("expected batch command, got %+v", cmd)
	}
	if cmd.GroupID != "maint" {
		t.Errorf("batch GroupID = %q, want %q", cmd.GroupID, "maint")
	}
	if cmd.Flags().Lookup("file") == nil {
		t.Error("batch missing --file flag")
	}
	if cmd.Flags().Lookup("dry-run") == nil {
		t.Error("batch missing --dry-run flag")
	}
	if cmd.Flags().Lookup("message") == nil {
		t.Error("batch missing --message flag")
	}
}
