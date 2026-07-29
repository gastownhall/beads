package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCreateCommandRejectsExtraPositionalArguments(t *testing.T) {
	err := createCmd.Args(createCmd, []string{"bug", "real title"})
	if err == nil {
		t.Fatal("expected create to reject a second positional argument")
	}
	if !strings.Contains(err.Error(), "at most 1 arg") {
		t.Fatalf("expected actionable maximum-argument error, got: %v", err)
	}
}

func TestCreateCommandAllowsOnePositionalArgument(t *testing.T) {
	if err := createCmd.Args(createCmd, []string{"title"}); err != nil {
		t.Fatalf("expected create to accept a single title argument, got: %v", err)
	}
	// Zero args with no --file/--graph/--title fails title resolution at
	// Args time (upstream moved title validation into Args via
	// validateCreateArgs), not with an arg-count error.
	if err := createCmd.Args(createCmd, nil); err == nil {
		t.Fatal("expected title-resolution error for zero args")
	}
}

func TestCreateCommandRegistersEmptyDescriptionOptIn(t *testing.T) {
	if createCmd.Flags().Lookup("allow-empty-description") == nil {
		t.Fatal("expected create to register --allow-empty-description")
	}
}

func TestGatherCreateInputRejectsEmptyBodyFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(filePath, nil, 0644); err != nil {
		t.Fatalf("write empty body file: %v", err)
	}

	cmd := &cobra.Command{Use: "create [title]"}
	registerCommonIssueFlags(cmd)
	cmd.Flags().Bool("allow-empty-description", false, "Allow empty description input from stdin or file")
	if err := cmd.ParseFlags([]string{"--body-file", filePath}); err != nil {
		t.Fatalf("parse create flags: %v", err)
	}

	_, err := gatherCreateInput(cmd, []string{"title"})
	if err == nil {
		t.Fatal("expected create input gathering to reject an empty body file")
	}
}
