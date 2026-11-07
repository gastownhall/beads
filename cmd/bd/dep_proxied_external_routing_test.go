//go:build cgo

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestProxiedServerDepAddRoutedExternalTarget is #5861 item 2's dep-add half
// (AC-8): the post-write lookup that decides whether a stored cross-prefix
// edge warns now reads through the routed external connection, so an edge into
// an externally served proxied rig is verified against that rig's beads. A
// target that exists there is stored with the usual success line and no typo
// notice; a target that does not warns like any unresolved edge, naming the
// target and the bd dep remove that deletes it. Before the fix the route
// refused to open the rig (proxy.store.unrouted), depTargetLookupMiss left no
// miss for the refusal, and a typo into such a rig was stored with no notice
// at all.
func TestProxiedServerDepAddRoutedExternalTarget(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	pair := newRoutedProxiedPair(t, bd, "gxe", "gxf")
	src := bdProxiedCreate(t, bd, pair.town.dir, "Dep source awaiting a routed target", "--type", "task")
	target := bdProxiedCreate(t, bd, pair.rig.dir, "Existing routed dep target", "--type", "task")

	t.Run("existing routed target stores without a warning", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command(bd, "dep", "add", src.ID, target.ID)
		cmd.Dir = pair.town.dir
		cmd.Env = bdProxiedEnv(pair.town.dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd dep add %s %s: %v\nstdout:\n%s\nstderr:\n%s", src.ID, target.ID, err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Added dependency") {
			t.Errorf("stdout = %q, want the usual Added dependency line", stdout.String())
		}
		if text := stderr.String(); strings.Contains(text, "was not found") || strings.Contains(text, "typo") || strings.Contains(text, "bd dep remove") {
			t.Errorf("a target the routed rig holds must not warn, stderr:\n%s", text)
		}
	})

	t.Run("absent routed target warns", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command(bd, "dep", "add", src.ID, "gxf-404")
		cmd.Dir = pair.town.dir
		cmd.Env = bdProxiedEnv(pair.town.dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd dep add %s gxf-404: %v\nstdout:\n%s\nstderr:\n%s", src.ID, err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Added dependency") {
			t.Errorf("stdout = %q, want the edge stored with the usual line", stdout.String())
		}
		remove := "bd dep remove " + src.ID + " gxf-404"
		if !strings.Contains(stderr.String(), "gxf-404 was not found") || !strings.Contains(stderr.String(), remove) {
			t.Fatalf("stderr = %q, want the unresolved-target warning naming gxf-404 and %q", stderr.String(), remove)
		}
		// The printed remedy removes the stored row.
		if out := bdProxiedDep(t, bd, pair.town.dir, "remove", src.ID, "gxf-404"); !strings.Contains(out, "Removed dependency") {
			t.Fatalf("printed remove did not remove the edge: %s", out)
		}
	})
}
