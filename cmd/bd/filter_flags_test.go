package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// A filter flag given twice must never silently keep only its last value
// (bd-rtblug, following #5739's fix for bd list). Each filter among the
// names status/state/id/type/assignee/label is reviewed below against what
// its consumer accepts:
//
//   - union: the consumer takes a comma-separated set, so a repeat joins it.
//   - once: the consumer takes ONE value, so a second occurrence is refused.
//
// The class is per command, not per flag name: `bd search --status` unions
// (workapi.ApplyStatusFilter takes a comma OR set) while `bd count --status`
// refuses (issueops.CountRequest.Status is one stored status).

type filterFlagClass string

const (
	unionFilter filterFlagClass = "union"
	onceFilter  filterFlagClass = "once"
)

type reviewedFilterFlag struct {
	path  string // cobra CommandPath
	flag  string
	class filterFlagClass
	// def is the registered default a once filter reports when not given.
	def string
}

// reviewedFilterFlags is the reviewed classification of every string filter
// flag of the six names in the command tree.
var reviewedFilterFlags = []reviewedFilterFlag{
	// bd list (#5739) - workapi.BuildListFilter takes comma sets for these.
	{path: "bd list", flag: "status", class: unionFilter},
	{path: "bd list", flag: "state", class: unionFilter},
	{path: "bd list", flag: "id", class: unionFilter},
	// CountRequest.IDFilter is documented as a comma-separated id set.
	{path: "bd count", flag: "id", class: unionFilter},
	// searchListRequest -> ApplyStatusFilter: comma-separated OR set.
	{path: "bd search", flag: "status", class: unionFilter},
	// humanListFilter -> BuildListFilter: comma-separated OR set.
	{path: "bd human list", flag: "status", class: unionFilter},
	// GitLab labels are a comma list (AND), types go through parseTypeList.
	{path: "bd gitlab sync", flag: "label", class: unionFilter},
	{path: "bd gitlab sync", flag: "type", class: unionFilter},
}

// filterFlagCases returns the reviewed rows of one class, failing the test
// when there are none so an empty table cannot pass as a green sweep.
func filterFlagCases(t *testing.T, class filterFlagClass) []reviewedFilterFlag {
	t.Helper()
	var out []reviewedFilterFlag
	for _, r := range reviewedFilterFlags {
		if r.class == class {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s filter flags in reviewedFilterFlags; the sweep derived nothing", class)
	}
	return out
}

// commandsByPath maps every command path in root's tree, hidden commands and
// aliases built by copying a command included, to its command.
func commandsByPath(root *cobra.Command) map[string]*cobra.Command {
	out := map[string]*cobra.Command{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		out[c.CommandPath()] = c
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return out
}

// lookupFilterFlag resolves a reviewed row to the flag set that declares it
// and the flag itself.
func lookupFilterFlag(t *testing.T, path, name string) (*pflag.FlagSet, *pflag.Flag) {
	t.Helper()
	cmd, ok := commandsByPath(rootCmd)[path]
	if !ok {
		t.Fatalf("command %q not found in the bd command tree", path)
	}
	for _, fs := range []*pflag.FlagSet{cmd.LocalNonPersistentFlags(), cmd.PersistentFlags(), cmd.Flags()} {
		if fl := fs.Lookup(name); fl != nil {
			return fs, fl
		}
	}
	t.Fatalf("%s has no --%s flag", path, name)
	return nil, nil
}

// parseFilterArgs parses args on the flag set declaring path's --name, from
// the registered baseline, and restores the baseline afterwards. Tests that
// call it touch the shared command tree and must not run in parallel.
func parseFilterArgs(t *testing.T, path, name string, args ...string) (*pflag.Flag, error) {
	t.Helper()
	resetCommandFlags(rootCmd)
	t.Cleanup(func() { resetCommandFlags(rootCmd) })
	_, fl := lookupFilterFlag(t, path, name)
	cmd := commandsByPath(rootCmd)[path]
	return fl, cmd.Flags().Parse(args)
}

// TestRepeatedUnionFilterFlagsUnion: a repeated union filter yields the same
// comma set as the equivalent comma input, deduplicated.
func TestRepeatedUnionFilterFlagsUnion(t *testing.T) {
	for _, r := range filterFlagCases(t, unionFilter) {
		t.Run(strings.ReplaceAll(r.path, " ", "_")+"/--"+r.flag, func(t *testing.T) {
			fl, err := parseFilterArgs(t, r.path, r.flag, "--"+r.flag, "one", "--"+r.flag+"=two")
			if err != nil {
				t.Fatalf("%s --%s one --%s=two: %v", r.path, r.flag, r.flag, err)
			}
			repeated := fl.Value.String()

			fl, err = parseFilterArgs(t, r.path, r.flag, "--"+r.flag, "one,two")
			if err != nil {
				t.Fatalf("%s --%s one,two: %v", r.path, r.flag, err)
			}
			if comma := fl.Value.String(); repeated != comma || repeated != "one,two" {
				t.Errorf("%s: repeated --%s = %q, comma form = %q; want both %q (last-value-wins drops a value)",
					r.path, r.flag, repeated, comma, "one,two")
			}

			fl, err = parseFilterArgs(t, r.path, r.flag, "--"+r.flag, "one", "--"+r.flag, "two,one", "--"+r.flag, "one")
			if err != nil {
				t.Fatalf("%s repeated --%s with duplicates: %v", r.path, r.flag, err)
			}
			if got := fl.Value.String(); got != "one,two" {
				t.Errorf("%s: --%s one --%s two,one --%s one = %q, want %q (deduplicated set)", r.path, r.flag, r.flag, r.flag, got, "one,two")
			}
		})
	}
}

// TestSearchRepeatedStatusUnionsIntoListRequest follows the union filters
// past the flag into what their consumers build: bd search's ListRequest,
// bd human list's IssueFilter and bd count's CountRequest.
func TestSearchRepeatedStatusUnionsIntoListRequest(t *testing.T) {
	t.Run("search --status", func(t *testing.T) {
		if _, err := parseFilterArgs(t, "bd search", "status", "--status", "open", "-s", "closed"); err != nil {
			t.Fatalf("parse: %v", err)
		}
		req, err := searchListRequest(searchCmd, "q")
		if err != nil {
			t.Fatalf("searchListRequest: %v", err)
		}
		if req.Status != "open,closed" {
			t.Errorf("ListRequest.Status = %q, want %q", req.Status, "open,closed")
		}
	})

	t.Run("human list --status", func(t *testing.T) {
		if _, err := parseFilterArgs(t, "bd human list", "status", "--status", "open", "--status", "closed"); err != nil {
			t.Fatalf("parse: %v", err)
		}
		status, _ := humanListCmd.Flags().GetString("status")
		filter, err := humanListFilter(status, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("humanListFilter(%q): %v", status, err)
		}
		if !slices.Equal(filter.Statuses, []types.Status{types.StatusOpen, types.StatusClosed}) {
			t.Errorf("IssueFilter.Statuses = %v (Status=%v), want [open closed]", filter.Statuses, filter.Status)
		}
	})

	t.Run("count --id", func(t *testing.T) {
		cmd := newCountFlagSet(t)
		if err := cmd.Flags().Parse([]string{"--id", "bd-1", "--id", "bd-2"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		req, _, err := parseCountRequest(cmd)
		if err != nil {
			t.Fatalf("parseCountRequest: %v", err)
		}
		if req.IDFilter != "bd-1,bd-2" {
			t.Errorf("CountRequest.IDFilter = %q, want %q", req.IDFilter, "bd-1,bd-2")
		}
	})
}
