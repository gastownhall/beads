package main

import (
	"io"
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

	// Single-valued all the way down: a repeat is refused.
	{path: "bd list", flag: "type", class: onceFilter},
	{path: "bd list", flag: "assignee", class: onceFilter},
	// CountRequest.Status is ONE stored status, not a comma OR set; unioning
	// would turn a silent truncation into a silent zero.
	{path: "bd count", flag: "status", class: onceFilter},
	{path: "bd count", flag: "assignee", class: onceFilter},
	{path: "bd count", flag: "type", class: onceFilter},
	{path: "bd search", flag: "assignee", class: onceFilter},
	{path: "bd search", flag: "type", class: onceFilter},
	{path: "bd gitlab sync", flag: "assignee", class: onceFilter},
	{path: "bd ready", flag: "assignee", class: onceFilter},
	{path: "bd ready", flag: "type", class: onceFilter},
	{path: "bd stale", flag: "status", class: onceFilter},
	{path: "bd find-duplicates", flag: "status", class: onceFilter},
	{path: "bd dep tree", flag: "status", class: onceFilter},
	{path: "bd dep list", flag: "type", class: onceFilter},
	{path: "bd lint", flag: "status", class: onceFilter},
	{path: "bd lint", flag: "type", class: onceFilter},
	// The hidden migrate-issues alias is a copy sharing the same flag set.
	{path: "bd migrate issues", flag: "status", class: onceFilter},
	{path: "bd migrate issues", flag: "type", class: onceFilter},
	{path: "bd migrate-issues", flag: "status", class: onceFilter},
	{path: "bd migrate-issues", flag: "type", class: onceFilter},
	{path: "bd formula list", flag: "type", class: onceFilter},
	{path: "bd mol wisp list", flag: "type", class: onceFilter},
	// gate check selects gates by one type (gate create --type is a setter).
	{path: "bd gate check", flag: "type", class: onceFilter},
	// tracker.SyncOptions.State is one of open/closed/all, default all.
	{path: "bd jira sync", flag: "state", class: onceFilter, def: "all"},
	{path: "bd linear sync", flag: "state", class: onceFilter, def: "all"},
	{path: "bd notion sync", flag: "state", class: onceFilter, def: "all"},
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
// and the flag itself. It never calls LocalNonPersistentFlags, LocalFlags or
// InheritedFlags: those run cobra's mergePersistentFlags, which copies the
// root's persistent flags into the command's Flags() for the rest of the
// process and breaks TestServeFlags (see goldenLocalFlags).
func lookupFilterFlag(t *testing.T, path, name string) (*pflag.FlagSet, *pflag.Flag) {
	t.Helper()
	cmd, ok := commandsByPath(rootCmd)[path]
	if !ok {
		t.Fatalf("command %q not found in the bd command tree", path)
	}
	if fl := goldenLocalFlags(cmd).Lookup(name); fl != nil {
		return cmd.Flags(), fl
	}
	if fl := cmd.PersistentFlags().Lookup(name); fl != nil {
		return cmd.PersistentFlags(), fl
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

// TestRepeatedOnceFilterFlagsRefuse: a second non-empty occurrence of a once
// filter fails flag parsing with an error naming the flag, in every spelling
// (long, shorthand, equals form) and even when the value is identical.
func TestRepeatedOnceFilterFlagsRefuse(t *testing.T) {
	for _, r := range filterFlagCases(t, onceFilter) {
		t.Run(strings.ReplaceAll(r.path, " ", "_")+"/--"+r.flag, func(t *testing.T) {
			_, fl := lookupFilterFlag(t, r.path, r.flag)
			long := "--" + r.flag
			second := long
			if fl.Shorthand != "" {
				second = "-" + fl.Shorthand
			}
			want := "--" + r.flag + " given more than once"
			for _, args := range [][]string{
				{long, "one", second, "two"},
				{long + "=one", long + "=two"},
				{long, "same", long, "same"},
			} {
				fl, err := parseFilterArgs(t, r.path, r.flag, args...)
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("%s %s: err = %v (value %q), want a refusal containing %q",
						r.path, strings.Join(args, " "), err, fl.Value.String(), want)
				}
			}
		})
	}
}

// TestCountRepeatedFilterRefusedBeforeRun: the refusal happens while flags are
// parsed, so neither a pre-run hook nor RunE ever sees the narrowed value.
func TestCountRepeatedFilterRefusedBeforeRun(t *testing.T) {
	for _, tc := range []struct{ flag, v1, v2 string }{
		{"status", "open", "closed"},
		{"type", "bug", "task"},
		{"assignee", "alice", "bob"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			ran := ""
			sentinel := func(stage string) func(*cobra.Command, []string) error {
				return func(*cobra.Command, []string) error { ran += stage + " "; return nil }
			}
			cmd := &cobra.Command{
				Use:               "count",
				PersistentPreRunE: sentinel("PersistentPreRunE"),
				PreRunE:           sentinel("PreRunE"),
				RunE:              sentinel("RunE"),
				SilenceUsage:      true,
				SilenceErrors:     true,
			}
			registerCountFlags(cmd)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--" + tc.flag, tc.v1, "--" + tc.flag, tc.v2})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "--"+tc.flag+" given more than once") {
				got, _ := cmd.Flags().GetString(tc.flag)
				t.Errorf("count --%s %s --%s %s: err = %v, value %q; want a refusal naming --%s",
					tc.flag, tc.v1, tc.flag, tc.v2, err, got, tc.flag)
			}
			if ran != "" {
				t.Errorf("count --%s repeated reached %s; the refusal must come before any hook or RunE", tc.flag, ran)
			}
		})
	}
}

// TestSweptFilterFlagsKeepSingleAndCommaForms is the negative control for the
// sweep: a single value, an explicit empty value and a comma form reach the
// consumer byte for byte as before, with no new validation, and an explicit
// empty occurrence still resets the value (so `--state open --state=
// --state closed` is accepted, not refused).
func TestSweptFilterFlagsKeepSingleAndCommaForms(t *testing.T) {
	for _, r := range reviewedFilterFlags {
		t.Run(strings.ReplaceAll(r.path, " ", "_")+"/--"+r.flag, func(t *testing.T) {
			long := "--" + r.flag
			for _, tc := range []struct {
				args []string
				want string
			}{
				{[]string{long, "open"}, "open"},
				{[]string{long + "="}, ""},
				{[]string{long, "open,closed"}, "open,closed"},
				{[]string{long, "open, closed"}, "open, closed"},
				{[]string{long, "not-a-real-value"}, "not-a-real-value"},
				{[]string{long, "open", long + "=", long, "closed"}, "closed"},
			} {
				fl, err := parseFilterArgs(t, r.path, r.flag, tc.args...)
				if err != nil {
					t.Errorf("%s %s: %v; want it accepted as before", r.path, strings.Join(tc.args, " "), err)
					continue
				}
				if got := fl.Value.String(); got != tc.want {
					t.Errorf("%s %s: value %q, want %q", r.path, strings.Join(tc.args, " "), got, tc.want)
				}
			}
		})
	}

	t.Run("count --status comma form stays one stored status", func(t *testing.T) {
		cmd := newCountFlagSet(t)
		if err := cmd.Flags().Parse([]string{"--status", "open,closed"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		req, _, err := parseCountRequest(cmd)
		if err != nil {
			t.Fatalf("parseCountRequest: %v", err)
		}
		// Deliberately unchanged: CountRequest.Status is one stored status,
		// so this still matches nothing rather than being split or refused.
		if req.Status != "open,closed" {
			t.Errorf("CountRequest.Status = %q, want %q passed through unchanged", req.Status, "open,closed")
		}
	})

	t.Run("search --status all still lifts the status filter", func(t *testing.T) {
		if _, err := parseFilterArgs(t, "bd search", "status", "--status", "all"); err != nil {
			t.Fatalf("parse: %v", err)
		}
		req, err := searchListRequest(searchCmd, "q")
		if err != nil {
			t.Fatalf("searchListRequest: %v", err)
		}
		if req.Status != "" {
			t.Errorf("ListRequest.Status = %q, want empty for --status all", req.Status)
		}
	})

	t.Run("human list --status all plus open keeps the consumer's refusal", func(t *testing.T) {
		if _, err := parseFilterArgs(t, "bd human list", "status", "--status", "all", "--status", "open"); err != nil {
			t.Fatalf("parse: %v", err)
		}
		status, _ := humanListCmd.Flags().GetString("status")
		if _, err := humanListFilter(status, workapi.ListConfig{}); err == nil || !strings.Contains(err.Error(), `"all" cannot be combined`) {
			t.Errorf("humanListFilter(%q): err = %v, want the existing all-plus-status refusal", status, err)
		}
	})
}

// TestFilterFlagDefaultsSurviveReset: a once filter with a non-empty default
// (the tracker sync --state flags, default "all") reports that default when
// fresh and again after the test harness restores a parsed flag, with Changed
// lowered, and the restored flag accepts one new value instead of counting
// the earlier parse as a first occurrence.
func TestFilterFlagDefaultsSurviveReset(t *testing.T) {
	var withDefault int
	for _, r := range reviewedFilterFlags {
		if r.def == "" {
			continue
		}
		withDefault++
		t.Run(strings.ReplaceAll(r.path, " ", "_")+"/--"+r.flag, func(t *testing.T) {
			check := func(stage string, fl *pflag.Flag) {
				t.Helper()
				fs, _ := lookupFilterFlag(t, r.path, r.flag)
				if got, err := fs.GetString(r.flag); err != nil || got != r.def {
					t.Errorf("%s: GetString(%q) = %q, %v; want %q", stage, r.flag, got, err, r.def)
				}
				if fl.DefValue != r.def {
					t.Errorf("%s: DefValue = %q, want %q", stage, fl.DefValue, r.def)
				}
				if fl.Changed {
					t.Errorf("%s: Changed = true, want false", stage)
				}
			}

			fl, err := parseFilterArgs(t, r.path, r.flag)
			if err != nil {
				t.Fatalf("parse with no args: %v", err)
			}
			check("fresh", fl)

			if fl, err = parseFilterArgs(t, r.path, r.flag, "--"+r.flag, "open"); err != nil {
				t.Fatalf("--%s open: %v", r.flag, err)
			}
			if got := fl.Value.String(); got != "open" || !fl.Changed {
				t.Fatalf("--%s open: value %q changed=%v, want open/true", r.flag, got, fl.Changed)
			}

			resetCommandFlags(rootCmd)
			check("after reset", fl)

			cmd := commandsByPath(rootCmd)[r.path]
			if err := cmd.Flags().Parse([]string{"--" + r.flag, "closed"}); err != nil {
				t.Fatalf("--%s closed after reset: %v; the reset must clear the occurrence", r.flag, err)
			}
			if got := fl.Value.String(); got != "closed" {
				t.Errorf("--%s closed after reset: value %q, want closed", r.flag, got)
			}
		})
	}
	if withDefault == 0 {
		t.Fatal("no reviewed filter flag has a non-empty default; nothing was checked")
	}
}

// filterFlagNames are the filter names the sweep reviewed. Filters with other
// names (gitlab --project/--milestone, count --title, ...) are out of scope.
var filterFlagNames = []string{"status", "state", "id", "type", "assignee", "label"}

type filterFlagExemption struct {
	path, flag, reason string
}

// filterFlagSetterExemptions are plain string flags of the reviewed names that
// SET a value or name ONE action target instead of filtering a result set.
// For a setter, last-one-wins is the shell's override idiom
// (`bd create $DEFAULTS --type bug`) and narrows nothing.
var filterFlagSetterExemptions = []filterFlagExemption{
	{"bd create", "type", "sets the new issue's type"},
	{"bd create", "status", "sets the new issue's initial status"},
	{"bd create", "id", "sets the new issue's explicit id"},
	{"bd create", "assignee", "sets the new issue's assignee"},
	{"bd update", "status", "sets the issue's new status"},
	{"bd update", "type", "sets the issue's new type"},
	{"bd update", "assignee", "sets the issue's new assignee"},
	{"bd q", "type", "sets the created issue's type"},
	{"bd dep add", "type", "sets the new dependency's type"},
	{"bd link", "type", "sets the new dependency's type"},
	{"bd gate create", "type", "sets the new gate's type"},
	{"bd mol bond", "type", "sets the bond type"},
	{"bd mol pour", "assignee", "sets the poured root's assignee"},
	{"bd audit label", "label", "records one label value on the entry"},
	{"bd admin compact", "id", "names the one issue to compact"},
}

// filterFlagKind classifies a string-typed flag by its value.
func filterFlagKind(fl *pflag.Flag) string {
	switch fl.Value.(type) {
	case *unionStringFlag:
		return string(unionFilter)
	case *onceStringFlag:
		return string(onceFilter)
	default:
		return "plain"
	}
}

type filterFlagRegistration struct {
	path, flag, kind string
}

// stringFilterFlagRegistrations walks root's tree path by path (hidden
// commands and copied aliases included; a flag set shared by two paths is
// reported under both) and returns every string-typed flag of the reviewed
// names, declared locally or persistently on that command.
func stringFilterFlagRegistrations(root *cobra.Command) []filterFlagRegistration {
	var out []filterFlagRegistration
	for path, cmd := range commandsByPath(root) {
		seen := map[string]bool{}
		visit := func(fl *pflag.Flag) {
			if seen[fl.Name] || !slices.Contains(filterFlagNames, fl.Name) || fl.Value.Type() != "string" {
				return
			}
			seen[fl.Name] = true
			out = append(out, filterFlagRegistration{path, fl.Name, filterFlagKind(fl)})
		}
		cmd.PersistentFlags().VisitAll(visit)
		// goldenLocalFlags, not LocalNonPersistentFlags: the latter merges
		// the root's persistent flags into every command's Flags() for the
		// rest of the process, which breaks TestServeFlags.
		goldenLocalFlags(cmd).VisitAll(visit)
	}
	slices.SortFunc(out, func(a, b filterFlagRegistration) int {
		return strings.Compare(a.path+" --"+a.flag, b.path+" --"+b.flag)
	})
	return out
}

// TestNoPlainStringFilterFlagSurvives is the fail-closed invariant: a string
// flag of a reviewed name is a union or once filter, or a reasoned setter
// exemption. A new plain-string filter breaks this test the day it lands.
func TestNoPlainStringFilterFlagSurvives(t *testing.T) {
	exempt := map[string]bool{}
	for _, e := range filterFlagSetterExemptions {
		exempt[e.path+" --"+e.flag] = true
	}
	regs := stringFilterFlagRegistrations(rootCmd)
	if len(regs) == 0 {
		t.Fatal("the command-tree walk found no string flags of the reviewed names")
	}
	var survivors []string
	for _, r := range regs {
		if r.kind == "plain" && !exempt[r.path+" --"+r.flag] {
			survivors = append(survivors, r.path+" --"+r.flag)
		}
	}
	if len(survivors) > 0 {
		t.Errorf("%d plain-string filter flag(s) keep only their last occurrence; register them with addUnionFilterFlag/addOnceFilterFlag (or, for a setter, add a reasoned exemption):\n  %s",
			len(survivors), strings.Join(survivors, "\n  "))
	}
}

// TestFilterFlagClassification checks the tree against the reviewed table in
// both directions: every reviewed row is registered with exactly the reviewed
// class, and every union/once value in the tree has a reviewed row. Deriving
// the class from the registered value alone could not catch a wrong choice
// (count --status unioned like search --status, gate check --type exempted
// like gate create --type).
func TestFilterFlagClassification(t *testing.T) {
	got := map[string]string{}
	for _, r := range stringFilterFlagRegistrations(rootCmd) {
		got[r.path+" --"+r.flag] = r.kind
	}
	reviewed := map[string]bool{}
	for _, r := range reviewedFilterFlags {
		key := r.path + " --" + r.flag
		if reviewed[key] {
			t.Errorf("%s is listed twice in reviewedFilterFlags", key)
		}
		reviewed[key] = true
		kind, ok := got[key]
		switch {
		case !ok:
			t.Errorf("%s: reviewed as %s but not registered as a string flag", key, r.class)
		case kind != string(r.class):
			t.Errorf("%s: registered as %s, reviewed as %s", key, kind, r.class)
		}
	}
	for key, kind := range got {
		if kind != "plain" && !reviewed[key] {
			t.Errorf("%s: registered as a %s filter but has no row in reviewedFilterFlags; review its consumer and add one", key, kind)
		}
	}
	if len(reviewedFilterFlags) != 34 {
		t.Errorf("reviewedFilterFlags has %d rows, want 34 (bd list's 5 plus the 29 swept command-path/flag pairs); update this count only with a reviewed row", len(reviewedFilterFlags))
	}
}

// TestFilterFlagSetterExemptionsAllExist: every exemption names an existing
// plain string flag and carries a reason, and repeating it still lets the
// last value win, as a setter override should.
func TestFilterFlagSetterExemptionsAllExist(t *testing.T) {
	if len(filterFlagSetterExemptions) == 0 {
		t.Fatal("filterFlagSetterExemptions is empty")
	}
	seen := map[string]bool{}
	for _, e := range filterFlagSetterExemptions {
		key := e.path + " --" + e.flag
		t.Run(strings.ReplaceAll(e.path, " ", "_")+"/--"+e.flag, func(t *testing.T) {
			if seen[key] {
				t.Errorf("%s is listed twice", key)
			}
			seen[key] = true
			if strings.TrimSpace(e.reason) == "" {
				t.Errorf("%s: exemption has no reason", key)
			}
			if slices.ContainsFunc(reviewedFilterFlags, func(r reviewedFilterFlag) bool { return r.path == e.path && r.flag == e.flag }) {
				t.Errorf("%s is both exempt and a reviewed filter", key)
			}
			_, fl := lookupFilterFlag(t, e.path, e.flag)
			if fl.Value.Type() != "string" || filterFlagKind(fl) != "plain" {
				t.Fatalf("%s: value %T (type %s), want a plain string setter", key, fl.Value, fl.Value.Type())
			}
			fl, err := parseFilterArgs(t, e.path, e.flag, "--"+e.flag, "first", "--"+e.flag, "second")
			if err != nil {
				t.Fatalf("%s repeated: %v; a setter repeat must be accepted", key, err)
			}
			if got := fl.Value.String(); got != "second" {
				t.Errorf("%s repeated: value %q, want the last value %q", key, got, "second")
			}
		})
	}
}

// TestFilterFlagsAllocatedPerRegistration: registerCountFlags on two
// independent commands gives each its own filter values, so parsing one
// command's filters leaves the other's values and Changed bits untouched.
func TestFilterFlagsAllocatedPerRegistration(t *testing.T) {
	a, b := newCountFlagSet(t), newCountFlagSet(t)
	names := []string{"status", "assignee", "type", "id"}
	for _, name := range names {
		fa, fb := a.Flags().Lookup(name), b.Flags().Lookup(name)
		if filterFlagKind(fa) == "plain" || filterFlagKind(fb) == "plain" {
			t.Errorf("count --%s: values %T/%T, want union/once filter values", name, fa.Value, fb.Value)
		}
		if fa.Value == fb.Value || fa.Value == countCmd.Flags().Lookup(name).Value {
			t.Errorf("count --%s: two registrations share one value instance", name)
		}
	}
	if err := a.Flags().Parse([]string{"--status", "open", "--assignee", "alice", "--type", "bug", "--id", "bd-1", "--id", "bd-2"}); err != nil {
		t.Fatalf("parse on the first command: %v", err)
	}
	for _, name := range names {
		fb := b.Flags().Lookup(name)
		if fb.Changed || fb.Value.String() != "" {
			t.Errorf("count --%s on the second command: value %q changed=%v after parsing the first; want untouched", name, fb.Value.String(), fb.Changed)
		}
	}
	// And the other way: the second command now parses its own repeat-free
	// values without tripping on the first command's occurrences.
	if err := b.Flags().Parse([]string{"--status", "closed", "--type", "task"}); err != nil {
		t.Fatalf("parse on the second command: %v", err)
	}
	if got, _ := a.Flags().GetString("status"); got != "open" {
		t.Errorf("first command's --status = %q after parsing the second, want open", got)
	}
	if got, _ := b.Flags().GetString("status"); got != "closed" {
		t.Errorf("second command's --status = %q, want closed", got)
	}
}

// TestNotionStateFilterFeedsSyncOptions pins the read that fills
// tracker.SyncOptions.State: --state is now a custom once value with no bound
// global, so runNotionSync reads it back with cmd.Flags().GetString("state")
// (notion.go). That read must return exactly what the user passed, and the
// fresh default must still read "all".
func TestNotionStateFilterFeedsSyncOptions(t *testing.T) {
	const path = "bd notion sync"
	syncState := func(stage string) string {
		t.Helper()
		got, err := commandsByPath(rootCmd)[path].Flags().GetString("state")
		if err != nil {
			t.Fatalf("%s: GetString(state): %v", stage, err)
		}
		return got
	}

	if _, err := parseFilterArgs(t, path, "state"); err != nil {
		t.Fatalf("parse with no args: %v", err)
	}
	if got := syncState("fresh"); got != "all" {
		t.Errorf("fresh: SyncOptions.State would be %q, want default all", got)
	}

	if _, err := parseFilterArgs(t, path, "state", "--state", "open"); err != nil {
		t.Fatalf("--state open: %v", err)
	}
	if got := syncState("--state open"); got != "open" {
		t.Errorf("--state open: SyncOptions.State would be %q, want open", got)
	}
}
