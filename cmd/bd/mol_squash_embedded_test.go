//go:build cgo

package main

import (
	"context"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedSquashWispMoleculeClearsRootEphemeral mirrors
// TestSquashWispMoleculeClearsRootEphemeral but runs against the embedded
// Dolt backend used by CI. Without this, the standalone-Dolt regression test
// skips in the Embedded Dolt CI tier and the fix's lines stay uncovered.
func TestEmbeddedSquashWispMoleculeClearsRootEphemeral(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	ctx := context.Background()
	beadsDir := t.TempDir()

	s, err := embeddeddolt.Open(ctx, beadsDir, "moltest", "main")
	if err != nil {
		t.Fatalf("embeddeddolt.Open: %v", err)
	}
	defer s.Close()

	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	root := &types.Issue{
		Title:     "Wisp Molecule Root",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Ephemeral: true,
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("CreateIssue root: %v", err)
	}

	child := &types.Issue{
		Title:     "Wisp Step",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := s.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("CreateIssue child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	result, err := squashMolecule(ctx, s, root, []*types.Issue{child}, false, "", "test")
	if err != nil {
		t.Fatalf("squashMolecule: %v", err)
	}
	if !result.WispSquash {
		t.Error("expected WispSquash=true for ephemeral root")
	}

	closed, err := s.GetIssue(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetIssue root after squash: %v", err)
	}
	if closed.Status != types.StatusClosed {
		t.Errorf("root status = %v, want closed", closed.Status)
	}
	if closed.Ephemeral {
		t.Error("root Ephemeral should be false after squash; closed wisp roots otherwise leak as duplicate JSONL rows on every export cycle")
	}
}
