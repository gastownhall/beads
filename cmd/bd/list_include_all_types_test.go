package main

import (
	"strings"
	"testing"
)

// TestListRegistersIncludeAllTypes pins the flag itself. ListRequest's
// IncludeAllTypes is the one switch whose contract is "lift every type
// suppression, including ones added later", and before this
// `bd list` had no flag for it: callers had to spell out the four
// --include-* flags and would silently narrow when a fifth suppression lands.
func TestListRegistersIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types (ListRequest.IncludeAllTypes)")
	}
	if got := flag.Value.Type(); got != "bool" {
		t.Fatalf("--include-all-types must be a bool, got %s", got)
	}
	if flag.DefValue != "false" {
		t.Fatalf("--include-all-types must default to false, got %q", flag.DefValue)
	}
	// pflag turns a backticked word into the flag's value placeholder, which
	// would print `--include-all-types word` for a bool.
	if strings.Contains(flag.Usage, "`") {
		t.Errorf("--include-all-types usage must not contain backticks, got %q", flag.Usage)
	}
	if !strings.Contains(flag.Usage, "status") || !strings.Contains(flag.Usage, "still apply") {
		t.Errorf("--include-all-types usage must say the default status exclusions still apply, got %q", flag.Usage)
	}
}

// TestListAllUsageIsStatusOnly: --all lifts the default status and pinned
// exclusions and the default limit, nothing on the type axis. Its old help
// ("overrides default filter") read as lifting every default, which sent
// callers looking for gates and wisps to a flag that never shows them.
func TestListAllUsageIsStatusOnly(t *testing.T) {
	flag := listCmd.Flags().Lookup("all")
	if flag == nil {
		t.Fatal("bd list must register --all")
	}
	usage := flag.Usage
	if strings.Contains(usage, "overrides default filter") {
		t.Errorf("--all must not claim to override the default filter: type and plane suppressions still apply; got %q", usage)
	}
	for _, want := range []string{"status", "pinned", "limit", "--include-all-types"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--all usage must mention %q, got %q", want, usage)
		}
	}
}

// TestListHelpIsByteIdenticalOutsideIncludeAllTypes: the new flag adds its
// own line to `bd list --help` and changes no byte of any other. pflag pads
// every line to the widest flag column, so a flag rendered wider than the
// widest one (a backticked word in its usage becomes a placeholder) would
// reflow the whole listing.
func TestListHelpIsByteIdenticalOutsideIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types")
	}
	shown := listCmd.Flags().FlagUsages()
	defer func(prev bool) { flag.Hidden = prev }(flag.Hidden)
	flag.Hidden = true
	hidden := listCmd.Flags().FlagUsages()

	var others []string
	removed := 0
	for _, line := range strings.Split(shown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--include-all-types") {
			removed++
			continue
		}
		others = append(others, line)
	}
	if removed != 1 {
		t.Fatalf("want one --include-all-types help line, got %d", removed)
	}
	if got := strings.Join(others, "\n"); got != hidden {
		t.Errorf("--include-all-types changed other bd list help lines:\n--- with it (its line removed)\n%s\n--- without it\n%s", got, hidden)
	}
}
