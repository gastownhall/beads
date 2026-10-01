//go:build cgo

package main

import (
	"testing"

	"github.com/spf13/pflag"
)

// TestUpdateAddLabelShorthand pins -l to --add-label on `bd update`, and pins it
// to the same letter `bd create` uses, since matching that is the whole point of
// the flag.
func TestUpdateAddLabelShorthand(t *testing.T) {
	flag := updateCmd.Flags().Lookup("add-label")
	if flag == nil {
		t.Fatal("updateCmd should have --add-label flag")
	}
	if flag.Shorthand != "l" {
		t.Errorf("expected shorthand='l', got %q", flag.Shorthand)
	}

	createFlag := createCmd.Flags().Lookup("labels")
	if createFlag == nil {
		t.Fatal("createCmd should have --labels flag")
	}
	if flag.Shorthand != createFlag.Shorthand {
		t.Errorf("update --add-label shorthand %q should match create --labels shorthand %q",
			flag.Shorthand, createFlag.Shorthand)
	}
}

// TestUpdateAddLabelUsageHasNoBackquotes guards the rendering of the help line.
// pflag's UnquoteUsage takes the first backquoted span in a usage string as the
// flag's ARGUMENT NAME, so a usage of "matches `bd create -l`" renders as
// "-l, --add-label bd create -l" in `bd update --help` instead of the type name.
func TestUpdateAddLabelUsageHasNoBackquotes(t *testing.T) {
	flag := updateCmd.Flags().Lookup("add-label")
	if flag == nil {
		t.Fatal("updateCmd should have --add-label flag")
	}
	name, _ := pflag.UnquoteUsage(flag)
	if name != "strings" {
		t.Errorf("expected --add-label to render its argument as \"strings\", got %q "+
			"(a backquoted span in the usage string is read as the argument name)", name)
	}
}
