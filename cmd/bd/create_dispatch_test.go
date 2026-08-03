//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateDirectModeRejectsAllowEmptyDescriptionWithFile is a regression
// test for the 2026-07-23 CHANGES_REQUESTED review on gastownhall/beads#4984:
// direct-mode (non-proxied) --file dispatch returned from RunE before the
// validateDescriptionUpdate/--allow-empty-description check ever ran, so the
// flag was silently accepted-and-ignored with --file. This asserts the guard
// added in create.go now rejects it before createIssuesFromMarkdown runs.
func TestCreateDirectModeRejectsAllowEmptyDescriptionWithFile(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)

	readonlyMode = false
	t.Cleanup(func() { readonlyMode = false })

	fileFlag := createCmd.Flags().Lookup("file")
	allowEmptyFlag := createCmd.Flags().Lookup("allow-empty-description")
	t.Cleanup(func() {
		_ = fileFlag.Value.Set("")
		fileFlag.Changed = false
		_ = allowEmptyFlag.Value.Set("false")
		allowEmptyFlag.Changed = false
	})

	tmpDir := t.TempDir()
	mdPath := filepath.Join(tmpDir, "plan.md")
	if err := os.WriteFile(mdPath, []byte("# Issue\n\nBody\n"), 0644); err != nil {
		t.Fatalf("write markdown fixture: %v", err)
	}

	if err := createCmd.Flags().Set("file", mdPath); err != nil {
		t.Fatalf("set --file: %v", err)
	}
	if err := createCmd.Flags().Set("allow-empty-description", "true"); err != nil {
		t.Fatalf("set --allow-empty-description: %v", err)
	}

	var err error
	stderr := captureStderr(t, func() {
		err = createCmd.RunE(createCmd, nil)
	})
	if err == nil {
		t.Fatal("expected direct-mode --file dispatch to reject --allow-empty-description")
	}
	if !strings.Contains(stderr, "--allow-empty-description is not valid with --file") {
		t.Fatalf("expected --file rejection message on stderr, got: %v (err: %v)", stderr, err)
	}
}

// TestCreateDirectModeRejectsAllowEmptyDescriptionWithGraph mirrors the --file
// case above for the --graph dispatch path.
func TestCreateDirectModeRejectsAllowEmptyDescriptionWithGraph(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)

	readonlyMode = false
	t.Cleanup(func() { readonlyMode = false })

	graphFlag := createCmd.Flags().Lookup("graph")
	allowEmptyFlag := createCmd.Flags().Lookup("allow-empty-description")
	t.Cleanup(func() {
		_ = graphFlag.Value.Set("")
		graphFlag.Changed = false
		_ = allowEmptyFlag.Value.Set("false")
		allowEmptyFlag.Changed = false
	})

	// The guard fires before createIssuesFromGraph reads the file, so the
	// path need not exist or contain a valid plan.
	graphPath := filepath.Join(t.TempDir(), "plan.json")

	if err := createCmd.Flags().Set("graph", graphPath); err != nil {
		t.Fatalf("set --graph: %v", err)
	}
	if err := createCmd.Flags().Set("allow-empty-description", "true"); err != nil {
		t.Fatalf("set --allow-empty-description: %v", err)
	}

	var err error
	stderr := captureStderr(t, func() {
		err = createCmd.RunE(createCmd, nil)
	})
	if err == nil {
		t.Fatal("expected direct-mode --graph dispatch to reject --allow-empty-description")
	}
	if !strings.Contains(stderr, "--allow-empty-description is not valid with --graph") {
		t.Fatalf("expected --graph rejection message on stderr, got: %v (err: %v)", stderr, err)
	}
}

// TestCreateSingleIssueAllowEmptyDescriptionRoundTrip exercises the opt-in
// success path end to end: a single-issue (non --file/--graph) create with an
// empty description read from an external source (--body-file pointing at an
// empty file) plus --allow-empty-description should actually create the
// issue, not merely register the flag (the review's should-fix #2: thin
// coverage of the opt-in path).
func TestCreateSingleIssueAllowEmptyDescriptionRoundTrip(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)

	// saveAndRestoreGlobals doesn't cover these three; restore them
	// explicitly so this test can't contaminate the package run.
	savedRootCtx, savedJSONOutput, savedActor := rootCtx, jsonOutput, actor
	t.Cleanup(func() {
		rootCtx, jsonOutput, actor = savedRootCtx, savedJSONOutput, savedActor
	})

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)

	store = s
	rootCtx = context.Background()
	jsonOutput = false
	readonlyMode = false
	actor = "test-actor"
	t.Cleanup(func() { readonlyMode = false })

	bodyFilePath := filepath.Join(tmpDir, "empty-body.md")
	if err := os.WriteFile(bodyFilePath, nil, 0644); err != nil {
		t.Fatalf("write empty body file: %v", err)
	}

	bodyFileFlag := createCmd.Flags().Lookup("body-file")
	allowEmptyFlag := createCmd.Flags().Lookup("allow-empty-description")
	t.Cleanup(func() {
		_ = bodyFileFlag.Value.Set("")
		bodyFileFlag.Changed = false
		_ = allowEmptyFlag.Value.Set("false")
		allowEmptyFlag.Changed = false
	})

	if err := createCmd.Flags().Set("body-file", bodyFilePath); err != nil {
		t.Fatalf("set --body-file: %v", err)
	}
	if err := createCmd.Flags().Set("allow-empty-description", "true"); err != nil {
		t.Fatalf("set --allow-empty-description: %v", err)
	}

	title := "Allow empty description round trip"
	if err := createCmd.RunE(createCmd, []string{title}); err != nil {
		t.Fatalf("expected --allow-empty-description opt-in to succeed, got: %v", err)
	}

	issues, err := s.SearchIssues(rootCtx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("search issues: %v", err)
	}
	var created bool
	for _, iss := range issues {
		if iss.Title == title {
			created = true
			if iss.Description != "" {
				t.Fatalf("expected empty description, got %q", iss.Description)
			}
		}
	}
	if !created {
		t.Fatalf("expected issue %q to be created via --allow-empty-description opt-in", title)
	}
}
