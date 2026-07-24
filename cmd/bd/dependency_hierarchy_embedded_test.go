//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bdLinkForHierarchyTest(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"link"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd link %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestEmbeddedExplicitHierarchicalParentEdges(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hg")
	grandparent := bdCreate(t, bd, dir, "Hierarchy grandparent", "--type", "epic")
	parent := bdCreate(t, bd, dir, "Hierarchy parent", "--id", grandparent.ID+".1", "--type", "task")

	t.Run("dep add immediate parent-child succeeds", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Dep child", "--id", parent.ID+".1", "--type", "task")
		out := bdDep(t, bd, dir, "add", child.ID, parent.ID, "--type", "parent-child")
		if !strings.Contains(out, "Added dependency") {
			t.Fatalf("unexpected dep add output: %s", out)
		}
	})

	t.Run("link immediate parent-child succeeds", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Link child", "--id", parent.ID+".2", "--type", "task")
		out := bdLinkForHierarchyTest(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")
		if !strings.Contains(out, "Linked") {
			t.Fatalf("unexpected link output: %s", out)
		}
	})

	t.Run("blocking immediate parent rejected", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Blocked child", "--id", parent.ID+".3", "--type", "task")
		out := bdDepFail(t, bd, dir, "add", child.ID, parent.ID, "--type", "blocks")
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected hierarchy rejection, got: %s", out)
		}
	})

	t.Run("blocks flag immediate parent rejected", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Blocks flag child", "--id", parent.ID+".4", "--type", "task")
		out := bdDepFail(t, bd, dir, parent.ID, "--blocks", child.ID)
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected hierarchy rejection, got: %s", out)
		}
	})

	t.Run("parent-child to grandparent rejected", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Grandparent child", "--id", parent.ID+".5", "--type", "task")
		out := bdDepFail(t, bd, dir, "add", child.ID, grandparent.ID, "--type", "parent-child")
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected ancestor rejection, got: %s", out)
		}
	})

	t.Run("bulk immediate parent-child succeeds and grandparent rejects", func(t *testing.T) {
		allowed := bdCreate(t, bd, dir, "Bulk child", "--id", parent.ID+".6", "--type", "task")
		allowedPath := filepath.Join(t.TempDir(), "allowed.jsonl")
		allowedBody := fmt.Sprintf("{\"from\":%q,\"to\":%q,\"type\":\"parent-child\"}\n", allowed.ID, parent.ID)
		if err := os.WriteFile(allowedPath, []byte(allowedBody), 0o600); err != nil {
			t.Fatalf("write allowed bulk file: %v", err)
		}
		bdDep(t, bd, dir, "add", "--file", allowedPath)

		rejected := bdCreate(t, bd, dir, "Bulk grandparent child", "--id", parent.ID+".7", "--type", "task")
		rejectedPath := filepath.Join(t.TempDir(), "rejected.jsonl")
		rejectedBody := fmt.Sprintf("{\"from\":%q,\"to\":%q,\"type\":\"parent-child\"}\n", rejected.ID, grandparent.ID)
		if err := os.WriteFile(rejectedPath, []byte(rejectedBody), 0o600); err != nil {
			t.Fatalf("write rejected bulk file: %v", err)
		}
		out := bdDepFail(t, bd, dir, "add", "--file", rejectedPath)
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected bulk ancestor rejection, got: %s", out)
		}
	})
}
