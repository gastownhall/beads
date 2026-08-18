package main

import (
	"testing"
)

func TestPourIsTopLevelCommand(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"pour"})
	if err != nil {
		t.Fatalf("bd pour should be a top-level command: %v", err)
	}
	if cmd == nil || cmd.Name() != "pour" {
		t.Fatalf("bd pour resolved to %#v", cmd)
	}
	if cmd.Parent() != rootCmd {
		t.Fatalf("bd pour parent = %v, want root", cmd.Parent())
	}

	molPour, _, err := rootCmd.Find([]string{"mol", "pour"})
	if err != nil {
		t.Fatalf("bd mol pour should still exist: %v", err)
	}
	if molPour == nil || molPour.Name() != "pour" {
		t.Fatalf("bd mol pour resolved to %#v", molPour)
	}
}
