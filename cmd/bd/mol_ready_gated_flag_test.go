package main

import "testing"

// TestMolReadyGatedFlagRegistered guards against the regression where the
// `bd mol ready --gated` help text advertised the flag but no flag was
// registered on the subcommand, so the runtime rejected --gated as unknown.
//
// Both spellings — `bd ready --gated` (registered on readyCmd in ready.go)
// and `bd mol ready --gated` (registered here) — must accept the flag.
func TestMolReadyGatedFlagRegistered(t *testing.T) {
	flag := molReadyGatedCmd.Flags().Lookup("gated")
	if flag == nil {
		t.Fatal("`bd mol ready` is missing the --gated flag; cobra will reject `bd mol ready --gated` with 'unknown flag: --gated'")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--gated flag should be bool, got %s", flag.Value.Type())
	}
}

// TestReadyGatedFlagRegistered guards the sibling spelling `bd ready --gated`,
// which is the path runMolReadyGated is dispatched from in ready.go.
func TestReadyGatedFlagRegistered(t *testing.T) {
	flag := readyCmd.Flags().Lookup("gated")
	if flag == nil {
		t.Fatal("`bd ready` is missing the --gated flag; gate-resume discovery breaks")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--gated flag should be bool, got %s", flag.Value.Type())
	}
}
