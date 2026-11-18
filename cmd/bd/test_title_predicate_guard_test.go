package main

// AC-7 guard tests (GH#5025, bd-3lcjwg): the pollution rework shares one title
// predicate across the doctor classifier and the create/export surfaces, and
// these tests pin that the create-time warning, the create description
// requirement bypass and the export filter keep exactly the semantics they
// had before — the seven-prefix, separator-delimited, case-insensitive
// match on the title's first word. None of these surfaces may widen to the
// doctor classifier's content rules, and none may narrow: a "test-..." title
// still warns at create time and still drops from export even when its
// description would spare it in the pollution report.

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// TestIsTestIssueMatchesDoctorPredicate pins that cmd/bd's isTestIssue and the
// doctor classifier's doctor.IsTestTitle are the same predicate — create and
// export keep their historical prefix semantics and the pollution classifier
// consumes the identical rule.
func TestIsTestIssueMatchesDoctorPredicate(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"test leftover", true},
		{"Test-quality stub", true},
		{"TEST_hardcoded id", true},
		{"benchmark-perf run", true},
		{"sample issue", true},
		{"tmp-throwaway", true},
		{"temp scratch row", true},
		{"debug entry", true},
		{"dummy row", true},
		{"Real feature request", false},
		{"Actual bug fix", false},
		{"testing the harness", false}, // prefix word without separator
		{"testy placeholder", false},
		{"contest issue", false},
		{"latest test run", false},
	}
	for _, tt := range tests {
		if got := isTestIssue(tt.title); got != tt.want {
			t.Errorf("isTestIssue(%q) = %v, want %v", tt.title, got, tt.want)
		}
		if got := doctor.IsTestTitle(tt.title); got != tt.want {
			t.Errorf("doctor.IsTestTitle(%q) = %v, want %v (predicates must agree)", tt.title, got, tt.want)
		}
	}
}

// TestCreateRequireDescriptionBypassesTestTitle pins the create.go gate: with
// create.require-description enabled, a test-prefixed title may still be
// created without a description (the operator has already labelled the row as
// test data), while an ordinary title is rejected. --dry-run returns before
// any store access, so the test needs no database.
func TestCreateRequireDescriptionBypassesTestTitle(t *testing.T) {
	initConfigForTest(t)
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	readonlyMode = false
	t.Cleanup(func() { readonlyMode = false })

	dryRunFlag := createCmd.Flags().Lookup("dry-run")
	t.Cleanup(func() {
		_ = dryRunFlag.Value.Set("false")
		dryRunFlag.Changed = false
	})
	if err := createCmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set --dry-run: %v", err)
	}

	config.Set("create.require-description", true)

	var ordinaryErr error
	ordinaryStderr := captureStderr(t, func() {
		ordinaryErr = createCmd.RunE(createCmd, []string{"Ship the export filter"})
	})
	if ordinaryErr == nil {
		t.Fatal("expected ordinary title without description to be rejected under create.require-description")
	}
	if !strings.Contains(ordinaryStderr, "description is required") {
		t.Fatalf("expected the description-required error for an ordinary title, got stderr=%q err=%v", ordinaryStderr, ordinaryErr)
	}

	// The same create with a test-prefixed title must pass the gate and
	// reach the dry-run preview.
	var bypassStderr string
	var bypassErr error
	bypassStderr = captureStderr(t, func() {
		bypassErr = createCmd.RunE(createCmd, []string{"test prefix bypass"})
	})
	if bypassErr != nil {
		t.Fatalf("test-prefixed title must bypass the description requirement, got: %v (stderr: %q)", bypassErr, bypassStderr)
	}
}

// TestGatherCreateInputRequireDescriptionBypassesTestTitle pins the same gate
// in gatherCreateInput, the input path the --file batch and proxied-server
// routes share (create_input.go).
func TestGatherCreateInputRequireDescriptionBypassesTestTitle(t *testing.T) {
	initConfigForTest(t)
	config.Set("create.require-description", true)

	var ordinaryErr error
	ordinaryStderr := captureStderr(t, func() {
		_, ordinaryErr = gatherCreateInput(newCreateFlagsCommand(t, "--title", "Document the wisp lifecycle"), nil)
	})
	if ordinaryErr == nil {
		t.Fatal("expected ordinary title without description to be rejected under create.require-description")
	}
	if !strings.Contains(ordinaryStderr, "description is required") {
		t.Fatalf("expected the description-required error for an ordinary title, got stderr=%q err=%v", ordinaryStderr, ordinaryErr)
	}

	if in, err := gatherCreateInput(newCreateFlagsCommand(t, "--title", "test prefix input bypass"), nil); err != nil {
		t.Fatalf("test-prefixed title must bypass the description requirement in gatherCreateInput, got: %v", err)
	} else if in.title != "test prefix input bypass" {
		t.Fatalf("gatherCreateInput resolved title %q, want the given test-prefixed title", in.title)
	}
}

// TestFilterOutPollutionDropsOnlyTestTitledRows pins the export filter
// (export.go): rows whose title carries the test prefix are excluded from the
// JSONL export; every other row survives regardless of how thin its content
// is — the classifier's description rules have no place on the export path.
func TestFilterOutPollutionDropsOnlyTestTitledRows(t *testing.T) {
	issues := []*types.Issue{
		{ID: "x-1", Title: "test leftover"},
		{ID: "x-2", Title: "Ordinary work"},
		{ID: "x-3", Title: "Thin row", Description: ""},
		{ID: "x-4", Title: "Sample import stub"},
	}
	got := filterOutPollution(issues)
	if len(got) != 2 || got[0].ID != "x-2" || got[1].ID != "x-3" {
		ids := make([]string, 0, len(got))
		for _, issue := range got {
			ids = append(ids, issue.ID)
		}
		t.Fatalf("filterOutPollution kept %v, want [x-2 x-3] (only the test-titled rows drop)", ids)
	}
}
