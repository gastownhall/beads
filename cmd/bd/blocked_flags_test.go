package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestBlockedFlagsRefusesEveryListingFilter pins blockedFlagFilterFlags to the
// flags blockedCmd registers. --flags answers the ids read from stdin, so a
// listing filter it does not refuse is silently ignored: a filter added to
// blockedCmd but not to the refusal list would answer every id while the
// caller believes the answer was narrowed. The reverse direction catches an
// entry naming no registered flag, which Changed never reports as set.
//
// It walks Flags() and PersistentFlags() with VisitAll, not LocalFlags(),
// which would merge the root's persistent flags into blockedCmd for the rest
// of the process (see TestNoSubcommandShadowsRootJSONFlag). A root flag that
// an earlier in-process run already merged in is skipped by identity, and
// cobra's own --help by its annotation.
func TestBlockedFlagsRefusesEveryListingFilter(t *testing.T) {
	// The flags --flags itself reads: the mode switch and its qualifier.
	modeFlags := []string{"flags", "raw"}

	var registered []string
	visit := func(f *pflag.Flag) {
		if f == rootCmd.PersistentFlags().Lookup(f.Name) || len(f.Annotations[cobra.FlagSetByCobraAnnotation]) > 0 {
			return
		}
		if !slices.Contains(registered, f.Name) {
			registered = append(registered, f.Name)
		}
	}
	blockedCmd.PersistentFlags().VisitAll(visit)
	blockedCmd.Flags().VisitAll(visit)

	for _, name := range modeFlags {
		if !slices.Contains(registered, name) {
			t.Errorf("blockedCmd registers no --%s; this test's mode flags are stale", name)
		}
	}
	for _, name := range registered {
		if !slices.Contains(modeFlags, name) && !slices.Contains(blockedFlagFilterFlags, name) {
			t.Errorf("--%s is registered on blockedCmd but --flags does not refuse it: add it to blockedFlagFilterFlags, or to this test's mode flags if --flags reads it", name)
		}
	}
	for _, name := range blockedFlagFilterFlags {
		if slices.Contains(modeFlags, name) {
			t.Errorf("blockedFlagFilterFlags refuses --%s, which --flags itself reads", name)
		}
		if !slices.Contains(registered, name) {
			t.Errorf("blockedFlagFilterFlags names --%s, which blockedCmd does not register", name)
		}
	}
}
