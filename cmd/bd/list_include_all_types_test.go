package main

import (
	"strings"
	"testing"
)

// This file pins `bd list --include-all-types` (bd-2lwkfd): the CLI knob for
// ListRequest.IncludeAllTypes, the union that lifts every default type/plane
// suppression at once. The backend semantics are pinned in
// internal/workapi/list_pinned_test.go; everything here is the reachability
// and the help that makes it usable.

// TestListRegistersIncludeAllTypes is the load-bearing registration check.
// ListRequest.IncludeAllTypes is the one field that skips applyTypeSuppressions
// entirely, so frontends cannot drift from future suppressions — but it was
// unreachable from `bd list`, whose four narrow include flags each lift only
// the suppression they name.
func TestListRegistersIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types: ListRequest.IncludeAllTypes is the union that lifts every default type/plane suppression, and the CLI had no way to set it")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--include-all-types must be a bool flag, got type %s", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--include-all-types must default to false (the default listing suppresses hidden types), got default %q", flag.DefValue)
	}
	// Backticks make pflag widen the flag's help column with an invented value
	// placeholder; a bool flag has no argument to name.
	if strings.Contains(flag.Usage, "`") {
		t.Errorf("--include-all-types usage must not use backticks (pflag would widen the help column): %q", flag.Usage)
	}
	// The union says nothing about status; its help must say so, or a caller
	// will read it as "show me everything" and miss closed rows.
	if !strings.Contains(flag.Usage, "status") {
		t.Errorf("--include-all-types usage must state that default status exclusions still apply: %q", flag.Usage)
	}
}

// TestListAllUsageIsStatusOnly pins the corrected --all help. The old wording
// claimed --all "overrides default filter", but --all lifts only the default
// STATUS exclusions, the default pinned filter and the default limit; the
// type/plane suppressions run regardless of it. With --include-all-types now
// registered, help can name the flag that owns that axis.
func TestListAllUsageIsStatusOnly(t *testing.T) {
	flag := listCmd.Flags().Lookup("all")
	if flag == nil {
		t.Fatal("listCmd has no --all flag registered")
	}
	usage := flag.Usage
	if strings.Contains(usage, "overrides default filter") {
		t.Errorf("--all usage must no longer claim to override the default filter; type/plane suppressions still apply to it: %q", usage)
	}
	// The three effects --all actually has, and the flag that owns the fourth
	// axis it does not touch.
	for _, needle := range []string{"status", "pinned", "limit", "--include-all-types"} {
		if !strings.Contains(usage, needle) {
			t.Errorf("--all usage must mention %q (its actual effects, and the separate type/plane flag): %q", needle, usage)
		}
	}
}

// TestListHelpIsByteIdenticalOutsideIncludeAllTypes proves the new flag leaves
// every other help row byte-for-byte as it was: help rendered with the flag's
// single line removed equals help rendered with the flag hidden, which is what
// the help looked like before the flag existed. A usage change elsewhere in the
// table, or a column the new flag widened (backticks, a value placeholder),
// shows up here as a diff.
func TestListHelpIsByteIdenticalOutsideIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types before its help can be compared")
	}

	withFlag := listCmd.UsageString()

	flag.Hidden = true
	withoutFlag := listCmd.UsageString()
	flag.Hidden = false

	var kept []string
	for _, line := range strings.Split(withFlag, "\n") {
		// The one line the flag owns: pflag prints a long flag with no
		// shorthand as six spaces, two dashes and the name.
		if strings.HasPrefix(line, "      --include-all-types") {
			continue
		}
		kept = append(kept, line)
	}
	if got, want := strings.Join(kept, "\n"), withoutFlag; got != want {
		t.Errorf("help outside the --include-all-types row must be byte-identical to help with the flag hidden.\nwith flag, its row removed:\n%s\nwith flag hidden:\n%s", got, want)
	}
}
