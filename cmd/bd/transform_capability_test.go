package main

import (
	"github.com/spf13/cobra"
	"testing"
)

func TestProxyTransformRefusals(t *testing.T) {
	for _, path := range []string{"rename", "rename-prefix", "duplicate", "supersede"} {
		root := &cobra.Command{Use: "bd"}
		cmd := &cobra.Command{Use: path}
		root.AddCommand(cmd)
		if err := validateProxyTransformBeforeProvider(cmd); err == nil {
			t.Errorf("%s unexpectedly allowed", path)
		}
	}
}

func TestProxyDuplicatesAutoMergeDryRunMatrix(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	cmd := &cobra.Command{Use: "duplicates"}
	cmd.Flags().Bool("auto-merge", false, "")
	cmd.Flags().Bool("dry-run", false, "")
	root.AddCommand(cmd)
	_ = cmd.Flags().Set("auto-merge", "true")
	if err := validateProxyTransformBeforeProvider(cmd); err == nil {
		t.Fatalf("non-dry auto-merge error = %v", err)
	}
	_ = cmd.Flags().Set("dry-run", "true")
	if err := validateProxyTransformBeforeProvider(cmd); err != nil {
		t.Fatalf("dry-run auto-merge refused: %v", err)
	}
}
