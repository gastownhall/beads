package main

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/workapi"
)

// The filter-flag sweep (GH#6548, bd-rtblug): a repeated plain-string filter
// flag silently keeps only its last occurrence, so `bd search --status open
// --status closed` answers confidently over a silently narrowed set. bd list
// was fixed in #5739; these tests hold the sweep over every other filter
// flag in the tree.
//
// None of these tests may run in parallel: they parse against the shared
// package command tree and restore it with resetCommandFlags.

// filterFlagNames is the reviewed flag-name set whose repeated occurrence
// must not silently keep the last value.
var filterFlagNames = []string{"status", "state", "id", "type", "assignee", "label"}

// stringFilterFlagKind classifies a flag by its value's concrete type:
// "union", "once", "plain" (pflag's own overwrite-on-repeat stringValue), or
// "other" for an unrecognized custom string value. A non-string flag (a
// StringSlice, a Bool, ...) reports "" and is outside this invariant: a
// repeatable slice flag is a legitimate spelling.
func stringFilterFlagKind(f *pflag.Flag) string {
	if f.Value.Type() != "string" {
		return ""
	}
	switch reflect.TypeOf(f.Value).String() {
	case "*main.unionStringFlag":
		return "union"
	case "*main.onceStringFlag":
		return "once"
	case "*pflag.stringValue":
		return "plain"
	default:
		return "other"
	}
}

// walkFilterFlagRegistrations visits every (command path, flag) registration
// among the six filter names on the full command tree, hidden commands
// included, without deduplicating flags across paths: the hidden migration
// alias shares bd migrate issues's FlagSet and must still be recorded at its
// own path, because that is the path a user types.
func walkFilterFlagRegistrations(root *cobra.Command, fn func(path string, f *pflag.Flag)) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		seen := map[*pflag.Flag]bool{}
		visit := func(f *pflag.Flag) {
			if !slices.Contains(filterFlagNames, f.Name) || seen[f] {
				return
			}
			seen[f] = true
			fn(c.CommandPath(), f)
		}
		c.PersistentFlags().VisitAll(visit)
		c.Flags().VisitAll(visit)
		if inherited := c.InheritedFlags(); inherited != nil {
			inherited.VisitAll(visit)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// commandByPath resolves a command in root's tree by its CommandPath
// ("bd migrate issues"), hidden commands included.
func commandByPath(root *cobra.Command, path string) *cobra.Command {
	var found *cobra.Command
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if found != nil {
			return
		}
		if c.CommandPath() == path {
			found = c
			return
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return found
}

// lookupFilterFlag resolves a swept flag by command path and flag name.
func lookupFilterFlag(t *testing.T, path, name string) (*cobra.Command, *pflag.Flag) {
	t.Helper()
	cmd := commandByPath(rootCmd, path)
	if cmd == nil {
		t.Fatalf("no command at path %s", path)
	}
	fl := cmd.Flags().Lookup(name)
	if fl == nil {
		t.Fatalf("%s has no --%s flag", path, name)
	}
	return cmd, fl
}

// parseFilterFlags parses args against the command at path on the shared
// tree, from a freshly reset state, and restores the tree afterwards.
func parseFilterFlags(t *testing.T, path string, args []string) error {
	t.Helper()
	resetCommandFlags(rootCmd)
	t.Cleanup(func() { resetCommandFlags(rootCmd) })
	cmd := commandByPath(rootCmd, path)
	if cmd == nil {
		t.Fatalf("no command at path %s", path)
	}
	return cmd.ParseFlags(args)
}

// filterFlagValue reads a swept flag's current value through the same
// GetString every production consumer uses.
func filterFlagValue(t *testing.T, path, name string) string {
	t.Helper()
	cmd, _ := lookupFilterFlag(t, path, name)
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		t.Fatalf("%s --%s: GetString: %v", path, name, err)
	}
	return v
}

// deriveSweptFilterFlags collects the tree's union/once filter registrations
// of the given kinds, failing loudly when the derivation matches nothing: an
// empty sweep must not pass as a green one.
func deriveSweptFilterFlags(t *testing.T, kinds ...string) []struct {
	path string
	f    *pflag.Flag
} {
	t.Helper()
	var out []struct {
		path string
		f    *pflag.Flag
	}
	walkFilterFlagRegistrations(rootCmd, func(path string, f *pflag.Flag) {
		if slices.Contains(kinds, stringFilterFlagKind(f)) {
			out = append(out, struct {
				path string
				f    *pflag.Flag
			}{path, f})
		}
	})
	return out
}

// TestRepeatedUnionFilterFlagsUnion derives every union filter from the
// registrations, so a new union flag is covered the day it lands.
func TestRepeatedUnionFilterFlagsUnion(t *testing.T) {
	cases := deriveSweptFilterFlags(t, "union")
	if len(cases) < 8 {
		t.Fatalf("derived %d union filter flags, want at least 8 (bd list's three, search status, human list status, gitlab label/type, count id); the sweep or the derivation is incomplete", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.path+" --"+tc.f.Name, func(t *testing.T) {
			if err := parseFilterFlags(t, tc.path, []string{"--" + tc.f.Name, "alpha", "--" + tc.f.Name, "beta"}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			if got := filterFlagValue(t, tc.path, tc.f.Name); got != "alpha,beta" {
				t.Errorf("repeated --%s = %q, want union %q", tc.f.Name, got, "alpha,beta")
			}
			// A comma occurrence plus a repeat joins the same set, and a
			// duplicate occurrence adds nothing.
			if err := parseFilterFlags(t, tc.path, []string{"--" + tc.f.Name, "alpha", "--" + tc.f.Name, "beta,alpha"}); err != nil {
				t.Fatalf("ParseFlags (comma+repeat): %v", err)
			}
			if got := filterFlagValue(t, tc.path, tc.f.Name); got != "alpha,beta" {
				t.Errorf("comma+repeat --%s = %q, want deduped %q", tc.f.Name, got, "alpha,beta")
			}
			// Shorthand and equals forms join the same union where a
			// shorthand exists.
			if sh := tc.f.Shorthand; sh != "" {
				if err := parseFilterFlags(t, tc.path, []string{"-" + sh, "alpha", "--" + tc.f.Name + "=beta"}); err != nil {
					t.Fatalf("ParseFlags (shorthand+equals): %v", err)
				}
				if got := filterFlagValue(t, tc.path, tc.f.Name); got != "alpha,beta" {
					t.Errorf("shorthand+equals --%s = %q, want union %q", tc.f.Name, got, "alpha,beta")
				}
			}
		})
	}
}

// TestSearchRepeatedStatusUnionsIntoListRequest checks the consumer, not
// just the flag value: a repeated --status reaches SearchListRequest as the
// same comma set the equivalent comma input produces.
func TestSearchRepeatedStatusUnionsIntoListRequest(t *testing.T) {
	resetCommandFlags(rootCmd)
	t.Cleanup(func() { resetCommandFlags(rootCmd) })

	if err := parseFilterFlags(t, "bd search", []string{"--status", "open", "--status", "closed"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	req, err := searchListRequest(commandByPath(rootCmd, "bd search"), "query")
	if err != nil {
		t.Fatalf("searchListRequest: %v", err)
	}
	if req.Status != "open,closed" {
		t.Errorf("repeated --status reached SearchListRequest as %q, want %q", req.Status, "open,closed")
	}

	// The equivalent comma form must produce the same request.
	if err := parseFilterFlags(t, "bd search", []string{"--status", "open,closed"}); err != nil {
		t.Fatalf("ParseFlags(comma): %v", err)
	}
	reqComma, err := searchListRequest(commandByPath(rootCmd, "bd search"), "query")
	if err != nil {
		t.Fatalf("searchListRequest(comma): %v", err)
	}
	if reqComma.Status != req.Status {
		t.Errorf("comma form reached SearchListRequest as %q, repeated form as %q; want identical", reqComma.Status, req.Status)
	}

	// bd human list --status reaches workapi.BuildListFilter through
	// humanListFilter; the union must carry both statuses into the filter.
	if err := parseFilterFlags(t, "bd human list", []string{"--status", "open", "--status", "closed"}); err != nil {
		t.Fatalf("ParseFlags(human): %v", err)
	}
	if got := filterFlagValue(t, "bd human list", "status"); got != "open,closed" {
		t.Fatalf("human list repeated --status = %q, want %q", got, "open,closed")
	}
	if _, err := humanListFilter("open,closed", workapi.ListConfig{}); err != nil {
		t.Errorf("humanListFilter(open,closed): %v; both statuses must reach the filter", err)
	}

	// bd count --id reaches CountRequest.IDFilter, documented as a
	// comma-separated id set: both repeated ids must survive.
	if err := parseFilterFlags(t, "bd count", []string{"--id", "bd-1", "--id", "bd-2"}); err != nil {
		t.Fatalf("ParseFlags(count id): %v", err)
	}
	if got := filterFlagValue(t, "bd count", "id"); got != "bd-1,bd-2" {
		t.Errorf("count repeated --id = %q, want %q", got, "bd-1,bd-2")
	}
}

// TestRepeatedOnceFilterFlagsRefuse derives every once filter from the
// registrations: a repeat must fail during parsing with an error naming the
// flag, never silently narrow.
func TestRepeatedOnceFilterFlagsRefuse(t *testing.T) {
	cases := deriveSweptFilterFlags(t, "once")
	if len(cases) < 26 {
		t.Fatalf("derived %d once filter flags, want at least 26 (bd list's two, the swept single-valued filters incl. gitlab assignee, and the three --state defaults); the sweep or the derivation is incomplete", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.path+" --"+tc.f.Name, func(t *testing.T) {
			err := parseFilterFlags(t, tc.path, []string{"--" + tc.f.Name, "alpha", "--" + tc.f.Name, "beta"})
			if err == nil {
				t.Fatalf("repeated --%s accepted (value %q); want a parse refusal", tc.f.Name, filterFlagValue(t, tc.path, tc.f.Name))
			}
			if !strings.Contains(err.Error(), tc.f.Name) || !strings.Contains(err.Error(), "more than once") {
				t.Errorf("repeated --%s error = %q, want it to name the flag and say it was given more than once", tc.f.Name, err)
			}
			// Identical non-empty repeats still refuse: a silent "same value,
			// no harm" exception would hide the narrowing on every flag whose
			// downstream canonicalizes (aliases, case).
			if err := parseFilterFlags(t, tc.path, []string{"--" + tc.f.Name, "alpha", "--" + tc.f.Name, "alpha"}); err == nil {
				t.Errorf("identical repeated --%s accepted; want a refusal", tc.f.Name)
			}
			// A shorthand repeat is the same repeat where a shorthand exists.
			if sh := tc.f.Shorthand; sh != "" {
				if err := parseFilterFlags(t, tc.path, []string{"-" + sh, "alpha", "--" + tc.f.Name, "beta"}); err == nil {
					t.Errorf("shorthand+long repeated --%s accepted; want a refusal", tc.f.Name)
				}
			}
		})
	}
}

// TestCountRepeatedFilterRefusedBeforeRun builds a fresh command carrying
// bd count's flag set (registerCountFlags exists so a test can do exactly
// this) with sentinel hooks: a repeated once filter must fail during flag
// parsing, before any hook or RunE runs.
func TestCountRepeatedFilterRefusedBeforeRun(t *testing.T) {
	for _, tc := range []struct {
		flag   string
		values []string
	}{
		{"status", []string{"open", "closed"}},
		{"type", []string{"bug", "task"}},
		{"assignee", []string{"alice", "bob"}},
	} {
		t.Run("count --"+tc.flag, func(t *testing.T) {
			reachedHook, reachedRun := false, false
			cmd := &cobra.Command{
				Use:           "count-fresh",
				SilenceUsage:  true,
				SilenceErrors: true,
				PersistentPreRunE: func(*cobra.Command, []string) error {
					reachedHook = true
					return nil
				},
				RunE: func(*cobra.Command, []string) error {
					reachedRun = true
					return nil
				},
			}
			registerCountFlags(cmd)
			cmd.SetArgs([]string{"--" + tc.flag, tc.values[0], "--" + tc.flag, tc.values[1]})

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("repeated --%s accepted; want a refusal", tc.flag)
			}
			if !strings.Contains(err.Error(), tc.flag) || !strings.Contains(err.Error(), "more than once") {
				t.Errorf("repeated --%s error = %q, want it to name the flag and say it was given more than once", tc.flag, err)
			}
			if reachedHook {
				t.Errorf("repeated --%s reached PersistentPreRunE; want the refusal before any hook", tc.flag)
			}
			if reachedRun {
				t.Errorf("repeated --%s reached RunE; want the refusal before execution", tc.flag)
			}
		})
	}

	// bd gate check --type selects a result set (never exempt, unlike gate
	// create --type): the registered flag must refuse a repeat at parse time,
	// with no gate evaluation involved.
	t.Run("gate check --type", func(t *testing.T) {
		err := parseFilterFlags(t, "bd gate check", []string{"--type", "gh", "--type", "timer"})
		if err == nil {
			t.Fatal("repeated gate check --type accepted; want a refusal")
		}
		if !strings.Contains(err.Error(), "type") || !strings.Contains(err.Error(), "more than once") {
			t.Errorf("repeated gate check --type error = %q, want it to name the flag and say it was given more than once", err)
		}
	})
}

// TestGitLabRepeatedFiltersReachCLIFilter checks the gitlab sync consumers:
// repeated --label/--type values load into the filter globals and reach
// buildCLIFilter and the type list as the joined set, not just the flag
// string. The globals are no longer pflag-bound, so the test saves and
// restores them explicitly.
func TestGitLabRepeatedFiltersReachCLIFilter(t *testing.T) {
	savedLabel, savedAssignee, savedType := gitlabFilterLabel, gitlabFilterAssignee, gitlabTypeFilter
	t.Cleanup(func() {
		gitlabFilterLabel, gitlabFilterAssignee, gitlabTypeFilter = savedLabel, savedAssignee, savedType
	})

	if err := parseFilterFlags(t, "bd gitlab sync", []string{"--label", "backend", "--label", "bug", "--type", "epic", "--type", "feature", "--assignee", "alice"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	loadGitLabFilterFlags(commandByPath(rootCmd, "bd gitlab sync").Flags())

	if gitlabFilterLabel != "backend,bug" {
		t.Errorf("gitlabFilterLabel = %q, want the union %q", gitlabFilterLabel, "backend,bug")
	}
	if got := fmt.Sprint(parseTypeList(gitlabTypeFilter)); got != "[epic feature]" {
		t.Errorf("parseTypeList(gitlabTypeFilter) = %s, want both joined types [epic feature]", got)
	}
	if got := fmt.Sprint(parseTypeList(filterFlagValue(t, "bd gitlab sync", "type"))); got != "[epic feature]" {
		t.Errorf("parseTypeList(type flag read) = %s, want [epic feature]", got)
	}
	if gitlabFilterAssignee != "alice" {
		t.Errorf("gitlabFilterAssignee = %q, want %q", gitlabFilterAssignee, "alice")
	}
	filter := buildCLIFilter()
	if filter == nil {
		t.Fatal("buildCLIFilter() = nil with filters loaded")
	}
	if filter.Labels != "backend,bug" {
		t.Errorf("buildCLIFilter().Labels = %q, want the union %q", filter.Labels, "backend,bug")
	}
	if filter.Assignee != "alice" {
		t.Errorf("buildCLIFilter().Assignee = %q, want %q", filter.Assignee, "alice")
	}
}
