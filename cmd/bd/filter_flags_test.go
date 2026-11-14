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

// sweptFilterClassification is the reviewed classification of every filter
// registration among the six names: "union" where the downstream consumer
// already accepts a comma-separated OR set, "once" where it is single-valued
// all the way down and a repeat must fail loudly instead of narrowing. bd
// list's five flags (#5739) are included so the table is the complete
// classification, not just the new sweep. The migration alias shares bd
// migrate issues's FlagSet and is recorded at its own path too.
var sweptFilterClassification = map[string]map[string]string{
	"bd list":            {"status": "union", "state": "union", "id": "union", "type": "once", "assignee": "once"},
	"bd count":           {"status": "once", "assignee": "once", "type": "once", "id": "union"},
	"bd search":          {"status": "union", "assignee": "once", "type": "once"},
	"bd human list":      {"status": "union"},
	"bd gitlab sync":     {"label": "union", "type": "union", "assignee": "once"},
	"bd ready":           {"assignee": "once", "type": "once"},
	"bd stale":           {"status": "once"},
	"bd find-duplicates": {"status": "once"},
	"bd dep tree":        {"status": "once"},
	"bd dep list":        {"type": "once"},
	"bd lint":            {"status": "once", "type": "once"},
	"bd migrate issues":  {"status": "once", "type": "once"},
	"bd migrate-issues":  {"status": "once", "type": "once"},
	"bd formula list":    {"type": "once"},
	"bd mol wisp list":   {"type": "once"},
	"bd gate check":      {"type": "once"},
	"bd jira sync":       {"state": "once"},
	"bd linear sync":     {"state": "once"},
	"bd notion sync":     {"state": "once"},
}

// setterFilterFlagExemptions lists the plain-string registrations that are
// deliberately NOT swept: value-setting flags, where the shell's own
// last-one-wins override idiom (`bd create $DEFAULTS --type bug`) narrows
// nothing, and action-target flags that select one object. Every entry must
// exist, remain a plain string, and keep last-value-wins; the exemption test
// enforces all three directions.
var setterFilterFlagExemptions = map[string]map[string]string{
	"bd create":        {"type": "sets the created issue's type", "status": "sets the created issue's initial status", "id": "sets the created issue's explicit ID", "assignee": "sets the created issue's assignee"},
	"bd update":        {"status": "sets the new status", "type": "sets the new type", "assignee": "sets the new assignee"},
	"bd q":             {"type": "sets the created issue's type"},
	"bd dep add":       {"type": "sets the created dependency's type"},
	"bd link":          {"type": "sets the created dependency's type"},
	"bd gate create":   {"type": "sets the created gate's type"},
	"bd mol bond":      {"type": "sets the created bond's type"},
	"bd mol pour":      {"assignee": "sets the spawned root issue's assignee"},
	"bd audit label":   {"label": "records the label value being audited"},
	"bd admin compact": {"id": "selects the one issue to compact"},
}

// TestNoPlainStringFilterFlagSurvives is the fail-closed invariant: after
// the sweep, no filter flag among the six names may remain a plain pflag
// string (silent last-value-wins) outside the reasoned setter-exemption
// table. A new registration breaks this test the day it lands.
func TestNoPlainStringFilterFlagSurvives(t *testing.T) {
	var survivors []string
	walkFilterFlagRegistrations(rootCmd, func(path string, f *pflag.Flag) {
		switch stringFilterFlagKind(f) {
		case "union", "once":
			return
		case "plain":
			if setterFilterFlagExemptions[path][f.Name] != "" {
				return
			}
			survivors = append(survivors, path+" --"+f.Name)
		case "other":
			survivors = append(survivors, path+" --"+f.Name+" (unclassified custom string value)")
		}
	})
	if len(survivors) > 0 {
		t.Errorf("%d filter flag(s) still silently keep the last value on repeat (register them union/once, or add a reasoned setter exemption):\n  %s",
			len(survivors), strings.Join(survivors, "\n  "))
	}
}

// TestFilterFlagClassification pins the reviewed union/once choice for every
// swept registration in both directions: each table entry exists with the
// reviewed kind, and every union/once registration in the tree is in the
// table. The kind cannot be derived from the value type alone — a wrong
// union/once choice (count --status unioning into a silent zero) is exactly
// what this table catches.
func TestFilterFlagClassification(t *testing.T) {
	wantTotal := 0
	for _, flags := range sweptFilterClassification {
		wantTotal += len(flags)
	}
	if wantTotal != 34 {
		t.Errorf("classification table has %d entries, want the reviewed 34 (29 swept pairs incl. the migration alias + bd list's five)", wantTotal)
	}

	kindByRegistration := map[string]string{}
	walkFilterFlagRegistrations(rootCmd, func(path string, f *pflag.Flag) {
		if k := stringFilterFlagKind(f); k == "union" || k == "once" {
			kindByRegistration[path+" --"+f.Name] = k
		}
	})

	var problems []string
	for path, flags := range sweptFilterClassification {
		for name, want := range flags {
			got, ok := kindByRegistration[path+" --"+name]
			switch {
			case !ok:
				problems = append(problems, path+" --"+name+": no union/once registration found")
			case got != want:
				problems = append(problems, fmt.Sprintf("%s --%s: is %s, reviewed classification says %s", path, name, got, want))
			}
		}
	}
	for reg, kind := range kindByRegistration {
		path, name, _ := strings.Cut(reg, " --")
		if sweptFilterClassification[path][name] == "" {
			problems = append(problems, reg+": "+kind+" registration is not in the reviewed classification table")
		}
	}
	if len(problems) > 0 {
		t.Errorf("classification mismatches:\n  %s", strings.Join(problems, "\n  "))
	}
}

// TestFilterFlagSetterExemptionsAllExist keeps the setter-exemption table
// honest in both directions and pins the last-one-wins override idiom it
// protects: every entry must resolve to a real plain-string registration
// (an exemption for a swept or missing flag is a stale row), and repeating
// each of the 15 exempted flags keeps only the last value, exactly what the
// shell override idiom (`bd create $DEFAULTS --type bug`) relies on. This is
// a guard, not a regression test: the plain registrations never regressed,
// so there is no red-at-base — it locks the carve-out against drift from
// both sides (over-broad exemption, accidental sweep).
func TestFilterFlagSetterExemptionsAllExist(t *testing.T) {
	total := 0
	for path, flags := range setterFilterFlagExemptions {
		for name := range flags {
			total++
			_, fl := lookupFilterFlag(t, path, name)
			if kind := stringFilterFlagKind(fl); kind != "plain" {
				t.Errorf("%s --%s: exemption is a %s registration, not a plain string", path, name, kind)
			}
		}
	}
	if total != 15 {
		t.Errorf("setter exemption table has %d entries, want the reviewed 15", total)
	}

	repeats := []struct {
		path, flag, v1, v2 string
	}{
		{"bd create", "type", "task", "bug"},
		{"bd create", "status", "open", "in_progress"},
		{"bd create", "id", "bd-seed", "bd-42"},
		{"bd create", "assignee", "alice", "bob"},
		{"bd update", "status", "open", "closed"},
		{"bd update", "type", "bug", "task"},
		{"bd update", "assignee", "alice", "bob"},
		{"bd q", "type", "bug", "task"},
		{"bd dep add", "type", "blocks", "related"},
		{"bd link", "type", "blocks", "related"},
		{"bd gate create", "type", "hard", "soft"},
		{"bd mol bond", "type", "ionic", "covalent"},
		{"bd mol pour", "assignee", "alice", "bob"},
		{"bd audit label", "label", "needs-triage", "backend"},
		{"bd admin compact", "id", "bd-1", "bd-2"},
	}
	for _, tc := range repeats {
		t.Run(tc.path+" --"+tc.flag, func(t *testing.T) {
			if err := parseFilterFlags(t, tc.path, []string{"--" + tc.flag, tc.v1, "--" + tc.flag, tc.v2}); err != nil {
				t.Fatalf("repeat refused, but %s --%s is an exempted setter: %v", tc.path, tc.flag, err)
			}
			if got := filterFlagValue(t, tc.path, tc.flag); got != tc.v2 {
				t.Errorf("%s --%s %s --%s %s: got %q, want last value %q (setter exemption must keep last-one-wins)",
					tc.path, tc.flag, tc.v1, tc.flag, tc.v2, got, tc.v2)
			}
		})
	}
}

// TestFilterFlagsAllocatedPerRegistration pins the allocation discipline the
// helpers in filter_flags.go follow: every registration gets its own
// unionStringFlag/onceStringFlag value, never a shared package-level var.
// A shared value would leak parses between commands (two bd invocations in
// one process, or test commands after a parse) and turn the refusal into a
// false positive. Guard by design: the helpers have allocated per
// registration since they landed in AC-1, so no base-red exists.
func TestFilterFlagsAllocatedPerRegistration(t *testing.T) {
	first, second := &cobra.Command{}, &cobra.Command{}
	registerCountFlags(first)
	registerCountFlags(second)

	if err := first.ParseFlags([]string{"--status", "open", "--id", "bd-1"}); err != nil {
		t.Fatalf("parse on first command: %v", err)
	}

	for _, name := range []string{"status", "assignee", "type", "id"} {
		f1, f2 := first.Flags().Lookup(name), second.Flags().Lookup(name)
		if f1 == nil || f2 == nil {
			t.Fatalf("--%s missing from one of the two registrations", name)
		}
		if reflect.ValueOf(f1.Value).Pointer() == reflect.ValueOf(f2.Value).Pointer() {
			t.Errorf("--%s: both registrations share one value (package-level var): a parse on one command would surface on the other", name)
		}
		if f2.Changed {
			t.Errorf("--%s: second command shows Changed after parsing only the first command", name)
		}
		if v, _ := second.Flags().GetString(name); v != "" {
			t.Errorf("--%s: second command holds %q after parsing only the first command", name, v)
		}
	}

	if v, _ := first.Flags().GetString("status"); v != "open" {
		t.Errorf("first command --status = %q, want open", v)
	}
	if v, _ := first.Flags().GetString("id"); v != "bd-1" {
		t.Errorf("first command --id = %q, want bd-1", v)
	}
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

// TestSweptFilterFlagsKeepSingleAndCommaForms is the negative control: the
// sweep must not quietly become "ignore the filter". A single value, a comma
// value, and the empty reset keep their exact base meaning, byte for byte,
// and an explicit empty between two occurrences is still a full reset.
func TestSweptFilterFlagsKeepSingleAndCommaForms(t *testing.T) {
	cases := deriveSweptFilterFlags(t, "union", "once")
	if len(cases) != 34 {
		t.Fatalf("derived %d swept filter flags, want exactly 34 (29 swept pairs incl. the migration alias + bd list's five); the sweep or the derivation is incomplete", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.path+" --"+tc.f.Name, func(t *testing.T) {
			kind := stringFilterFlagKind(tc.f)

			if err := parseFilterFlags(t, tc.path, []string{"--" + tc.f.Name, "alpha"}); err != nil {
				t.Fatalf("single --%s: ParseFlags: %v", tc.f.Name, err)
			}
			if got := filterFlagValue(t, tc.path, tc.f.Name); got != "alpha" {
				t.Errorf("single --%s = %q, want %q unchanged", tc.f.Name, got, "alpha")
			}

			if err := parseFilterFlags(t, tc.path, []string{"--" + tc.f.Name + "=alpha,beta"}); err != nil {
				t.Fatalf("comma --%s: ParseFlags: %v", tc.f.Name, err)
			}
			if got := filterFlagValue(t, tc.path, tc.f.Name); got != "alpha,beta" {
				t.Errorf("comma --%s = %q, want %q byte-preserved", tc.f.Name, got, "alpha,beta")
			}

			// The empty value resets: a subsequent occurrence is the first
			// one again (bd children already relies on this for --status).
			reset := []string{"--" + tc.f.Name, "alpha", "--" + tc.f.Name + "=", "--" + tc.f.Name, "beta"}
			if err := parseFilterFlags(t, tc.path, reset); err != nil {
				t.Fatalf("empty-reset --%s chain: ParseFlags: %v", tc.f.Name, err)
			}
			if got := filterFlagValue(t, tc.path, tc.f.Name); got != "beta" {
				t.Errorf("empty-reset --%s chain = %q, want %q", tc.f.Name, got, "beta")
			}

			if kind == "once" {
				// A comma value stays one occurrence for a once flag: no new
				// validation is added, the downstream keeps its base reading
				// (including bd count's documented match-nothing status).
				if err := parseFilterFlags(t, tc.path, []string{"--" + tc.f.Name + "=a,b"}); err != nil {
					t.Fatalf("comma once --%s: ParseFlags: %v", tc.f.Name, err)
				}
				if got := filterFlagValue(t, tc.path, tc.f.Name); got != "a,b" {
					t.Errorf("comma once --%s = %q, want %q", tc.f.Name, got, "a,b")
				}
			}
		})
	}
}

// TestFilterFlagDefaultsSurviveReset covers the state filters that default
// to "all": fresh and restored through the in-process test harness, GetString
// and DefValue report the default, Changed is false, a single value is
// accepted, and a repeat is refused rather than narrowed. A reset that
// emptied the value instead of restoring "all" would turn the next
// invocation's default from "all statuses" into "none".
func TestFilterFlagDefaultsSurviveReset(t *testing.T) {
	statePaths := []string{"bd jira sync", "bd linear sync", "bd notion sync"}
	for _, path := range statePaths {
		t.Run(path+" --state", func(t *testing.T) {
			resetCommandFlags(rootCmd)
			t.Cleanup(func() { resetCommandFlags(rootCmd) })

			cmd, fl := lookupFilterFlag(t, path, "state")
			if got := filterFlagValue(t, path, "state"); got != "all" {
				t.Fatalf("fresh --state = %q, want default %q", got, "all")
			}
			if fl.DefValue != "all" {
				t.Errorf("--state DefValue = %q, want %q", fl.DefValue, "all")
			}
			if cmd.Flags().Changed("state") {
				t.Error("fresh --state reads as Changed")
			}

			if err := cmd.ParseFlags([]string{"--state", "open"}); err != nil {
				t.Fatalf("ParseFlags(single --state): %v", err)
			}
			if got := filterFlagValue(t, path, "state"); got != "open" {
				t.Fatalf("single --state = %q, want %q", got, "open")
			}

			// The harness restore puts the flag back to its registered
			// default "all", not the empty string.
			resetCommandFlags(rootCmd)
			if got := filterFlagValue(t, path, "state"); got != "all" {
				t.Fatalf("--state after harness reset = %q, want %q", got, "all")
			}
			if cmd.Flags().Changed("state") {
				t.Error("--state still reads as Changed after harness reset")
			}
			if err := cmd.ParseFlags([]string{"--state", "closed"}); err != nil {
				t.Fatalf("ParseFlags(--state closed) after reset: %v", err)
			}
			if got := filterFlagValue(t, path, "state"); got != "closed" {
				t.Fatalf("--state after reset+parse = %q, want %q", got, "closed")
			}

			// A repeat is refused, naming the flag, and the explicit empty
			// between two occurrences is a reset, not a refusal.
			err := parseFilterFlags(t, path, []string{"--state", "open", "--state", "closed"})
			if err == nil {
				t.Fatal("repeated --state accepted; want a refusal")
			}
			if !strings.Contains(err.Error(), "state") || !strings.Contains(err.Error(), "more than once") {
				t.Errorf("repeated --state error = %q, want it to name the flag and say it was given more than once", err)
			}
			if err := parseFilterFlags(t, path, []string{"--state", "open", "--state=", "--state", "closed"}); err != nil {
				t.Fatalf("empty-reset --state chain: ParseFlags: %v", err)
			}
			if got := filterFlagValue(t, path, "state"); got != "closed" {
				t.Errorf("empty-reset --state chain = %q, want %q", got, "closed")
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

	// The load must re-read the flags every run: after the harness resets the
	// tree and a later invocation passes no filter flags, the globals carry
	// no stale union from this parse. A pflag-bound global would have kept
	// the flags' own reset; the explicit load path has to clear on its own.
	resetCommandFlags(rootCmd)
	if err := parseFilterFlags(t, "bd gitlab sync", nil); err != nil {
		t.Fatalf("ParseFlags after reset: %v", err)
	}
	loadGitLabFilterFlags(commandByPath(rootCmd, "bd gitlab sync").Flags())
	if gitlabFilterLabel != "" {
		t.Errorf("gitlabFilterLabel = %q after a filter-less run, want cleared", gitlabFilterLabel)
	}
	if gitlabTypeFilter != "" {
		t.Errorf("gitlabTypeFilter = %q after a filter-less run, want cleared", gitlabTypeFilter)
	}
	if gitlabFilterAssignee != "" {
		t.Errorf("gitlabFilterAssignee = %q after a filter-less run, want cleared", gitlabFilterAssignee)
	}
	if buildCLIFilter() != nil {
		t.Error("buildCLIFilter() != nil with no filters loaded")
	}
}

// TestNotionStateFilterFeedsSyncOptions pins the read that populates
// tracker.SyncOptions.State: the --state flag became a custom once value
// (GH#6548), so the old pflag global binding went and runNotionSync reads
// the flag back with GetString when building the options (notion.go). The
// round trip must return exactly what the user passed, and the fresh
// default must still read "all". Guard by design: the GetString load has
// existed since the flag was converted, so no base-red exists.
func TestNotionStateFilterFeedsSyncOptions(t *testing.T) {
	if err := parseFilterFlags(t, "bd notion sync", nil); err != nil {
		t.Fatalf("ParseFlags (fresh): %v", err)
	}
	if got := filterFlagValue(t, "bd notion sync", "state"); got != "all" {
		t.Errorf("fresh --state reads %q, want default all", got)
	}
	if err := parseFilterFlags(t, "bd notion sync", []string{"--state", "open"}); err != nil {
		t.Fatalf("ParseFlags --state open: %v", err)
	}
	if got := filterFlagValue(t, "bd notion sync", "state"); got != "open" {
		t.Errorf("--state open reads %q, want open (this GetString is what tracker.SyncOptions.State receives)", got)
	}
}
