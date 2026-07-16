package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func newGuardTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "t", RunE: func(*cobra.Command, []string) error { return nil }}
	registerOwnershipGuardFlags(cmd)
	cmd.Flags().Bool("json", false, "")
	return cmd
}

// Changed()-gating: unset flags produce no guard; explicitly-set zero values
// ("" and 0) are valid assertions (unassigned / pristine fence).
func TestOwnershipGuardFlagGating(t *testing.T) {
	cmd := newGuardTestCmd()
	if _, ok := ownershipGuardFromFlags(cmd); ok {
		t.Fatal("unset flags produced a guard")
	}

	cmd = newGuardTestCmd()
	if err := cmd.Flags().Set("if-assignee", ""); err != nil {
		t.Fatal(err)
	}
	g, ok := ownershipGuardFromFlags(cmd)
	if !ok || g.Assignee == nil || *g.Assignee != "" {
		t.Fatalf("explicit empty --if-assignee must assert unassigned, got %+v ok=%v", g, ok)
	}
	if g.Fence != nil {
		t.Fatalf("fence axis set without --if-fence: %+v", g)
	}

	cmd = newGuardTestCmd()
	if err := cmd.Flags().Set("if-fence", "0"); err != nil {
		t.Fatal(err)
	}
	g, ok = ownershipGuardFromFlags(cmd)
	if !ok || g.Fence == nil || *g.Fence != 0 {
		t.Fatalf("explicit --if-fence 0 must assert fence==0, got %+v ok=%v", g, ok)
	}
}

// Guards scope to one issue: multi-target invocations are refused.
func TestValidateGuardInvocationSingleTarget(t *testing.T) {
	cmd := newGuardTestCmd()
	if err := cmd.Flags().Set("if-fence", "3"); err != nil {
		t.Fatal(err)
	}
	if err := validateGuardInvocation(cmd, 2); err == nil {
		t.Fatal("guard with 2 targets must be refused")
	}
	if err := validateGuardInvocation(cmd, 1); err != nil {
		t.Fatalf("guard with 1 target refused: %v", err)
	}
	// No guard: any target count passes.
	cmd = newGuardTestCmd()
	if err := validateGuardInvocation(cmd, 5); err != nil {
		t.Fatalf("unguarded multi-target refused: %v", err)
	}
}

// All three guarded verbs carry the flags.
func TestGuardFlagsRegisteredOnVerbs(t *testing.T) {
	for _, c := range []*cobra.Command{unclaimCmd, closeCmd, updateCmd} {
		for _, f := range []string{"if-assignee", "if-fence"} {
			if c.Flags().Lookup(f) == nil {
				t.Errorf("%s missing --%s", c.Name(), f)
			}
		}
	}
}
