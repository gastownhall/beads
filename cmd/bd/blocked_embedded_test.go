//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedBlocked(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bl")

	// ===== Default Empty =====

	t.Run("blocked_default_empty", func(t *testing.T) {
		cmd := exec.Command(bd, "blocked")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd blocked failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		// No blocked issues on fresh db
		_ = stdout.String()
	})

	// ===== With Blocked Issue =====

	t.Run("blocked_with_issue", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker for blocked test", "--type", "task")
		blocked := bdCreate(t, bd, dir, "I am blocked", "--type", "task")

		// blocked depends on blocker (blocker blocks blocked)
		cmd := exec.Command(bd, "dep", "add", blocked.ID, blocker.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dep add failed: %v\n%s", err, out)
		}

		cmd = exec.Command(bd, "blocked")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd blocked failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), blocked.ID) {
			t.Errorf("expected %s in blocked output: %s", blocked.ID, stdout.String())
		}
	})

	// ===== Unresolvable blocks edges (bd-uschhs AC-8) =====

	t.Run("unresolvable_blocks_edges_keep_ready_and_blocked", func(t *testing.T) {
		assertUnresolvableBlocksMembership(t, unresolvableBlocksDriver{
			create: func(title string) string { return bdCreate(t, bd, dir, title, "--type", "task").ID },
			depAdd: func(from, to string) { bdDep(t, bd, dir, "add", from, to, "--type", "blocks") },
			ready:  func() map[string]bool { return readyJSONIDs(t, bd, dir, "--limit", "0") },
			blocked: func() map[string]bool {
				ids := map[string]bool{}
				for _, e := range bdBlockedJSON(t, bd, dir) {
					if id, ok := e["id"].(string); ok {
						ids[id] = true
					}
				}
				return ids
			},
		})
	})

	// ===== --json =====

	t.Run("blocked_json", func(t *testing.T) {
		cmd := exec.Command(bd, "blocked", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd blocked --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.IndexAny(s, "[{")
		if start < 0 {
			t.Fatalf("no JSON in blocked --json output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("invalid JSON in blocked output: %s", s[:min(200, len(s))])
		}
	})
}

func TestEmbeddedBlockedConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bx")

	bdCreate(t, bd, dir, "Blocked concurrent issue", "--type", "task")

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			cmd := exec.Command(bd, "blocked")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("blocked (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}

// bdBlockedJSON runs "bd blocked --json" and parses the result as a slice.
func bdBlockedJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"blocked", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd blocked --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in blocked output: %s", s)
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &entries); err != nil {
		t.Fatalf("parse blocked JSON: %v\n%s", err, s)
	}
	return entries
}

// TestEmbeddedBlockedLabelFilters covers --label, --label-any and
// --exclude-label on bd blocked. The flags filter the blocked issues
// themselves, never their blockers: a blocked issue stays visible under
// --label whatever labels the thing blocking it happens to carry.
func TestEmbeddedBlockedLabelFilters(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bt")

	blocker := bdCreate(t, bd, dir, "Blocker", "--type", "task", "--labels", "theme:blocker-only")
	alpha := bdCreate(t, bd, dir, "Blocked alpha", "--type", "task", "--labels", "theme:alpha")
	beta := bdCreate(t, bd, dir, "Blocked beta", "--type", "task", "--labels", "theme:beta")
	alphaUrgent := bdCreate(t, bd, dir, "Blocked alpha urgent", "--type", "task", "--labels", "theme:alpha,urgent")

	for _, blocked := range []string{alpha.ID, beta.ID, alphaUrgent.ID} {
		cmd := exec.Command(bd, "dep", "add", blocked, blocker.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dep add %s -> %s failed: %v\n%s", blocked, blocker.ID, err, out)
		}
	}

	t.Run("label_scopes_to_one_theme", func(t *testing.T) {
		ids := idSet(bdBlockedJSON(t, bd, dir, "--label", "theme:alpha"))
		if !ids[alpha.ID] || !ids[alphaUrgent.ID] {
			t.Errorf("expected both theme:alpha blocked issues, got %v", ids)
		}
		if ids[beta.ID] {
			t.Errorf("theme:alpha filter leaked %s: %v", beta.ID, ids)
		}
	})

	t.Run("repeated_label_is_AND", func(t *testing.T) {
		ids := idSet(bdBlockedJSON(t, bd, dir, "--label", "theme:alpha", "--label", "urgent"))
		if !ids[alphaUrgent.ID] {
			t.Errorf("expected %s carrying both labels, got %v", alphaUrgent.ID, ids)
		}
		if ids[alpha.ID] {
			t.Errorf("%s carries only theme:alpha and must not match an AND of both: %v", alpha.ID, ids)
		}
	})

	t.Run("label_any_is_OR", func(t *testing.T) {
		ids := idSet(bdBlockedJSON(t, bd, dir, "--label-any", "theme:alpha,theme:beta"))
		for _, id := range []string{alpha.ID, beta.ID, alphaUrgent.ID} {
			if !ids[id] {
				t.Errorf("expected %s from --label-any, got %v", id, ids)
			}
		}
	})

	t.Run("exclude_label_drops_matches", func(t *testing.T) {
		ids := idSet(bdBlockedJSON(t, bd, dir, "--exclude-label", "theme:alpha"))
		if ids[alpha.ID] || ids[alphaUrgent.ID] {
			t.Errorf("--exclude-label theme:alpha still returned alpha issues: %v", ids)
		}
		if !ids[beta.ID] {
			t.Errorf("expected %s to survive exclusion, got %v", beta.ID, ids)
		}
	})

	t.Run("blocker_labels_do_not_filter_the_blocked", func(t *testing.T) {
		// theme:blocker-only lives on the blocker, not on anything blocked,
		// so scoping to it must return nothing rather than the issues it
		// blocks.
		ids := idSet(bdBlockedJSON(t, bd, dir, "--label", "theme:blocker-only"))
		for _, id := range []string{alpha.ID, beta.ID, alphaUrgent.ID} {
			if ids[id] {
				t.Errorf("blocked issue %s matched a label carried only by its blocker: %v", id, ids)
			}
		}
	})

	t.Run("no_label_flags_returns_everything", func(t *testing.T) {
		ids := idSet(bdBlockedJSON(t, bd, dir))
		for _, id := range []string{alpha.ID, beta.ID, alphaUrgent.ID} {
			if !ids[id] {
				t.Errorf("expected %s in unfiltered blocked output, got %v", id, ids)
			}
		}
	})

	// the blocked role normalizes, as every sibling label filter does.
	// Without it an untrimmed value returns nothing and is indistinguishable
	// from "nothing blocked carries that label".
	t.Run("leading_space_is_trimmed_like_every_other_label_filter", func(t *testing.T) {
		ids := idSet(bdBlockedJSON(t, bd, dir, "--label", " theme:alpha"))
		if !ids[alpha.ID] || !ids[alphaUrgent.ID] {
			t.Errorf("expected ' theme:alpha' to match theme:alpha, got %v", ids)
		}
		if ids[beta.ID] {
			t.Errorf("' theme:alpha' leaked %s: %v", beta.ID, ids)
		}
	})

	t.Run("empty_element_does_not_annihilate_the_filter", func(t *testing.T) {
		ids := idSet(bdBlockedJSON(t, bd, dir, "--label", "theme:alpha,,urgent"))
		if !ids[alphaUrgent.ID] {
			t.Errorf("expected %s to survive an empty label element, got %v", alphaUrgent.ID, ids)
		}
	})

	t.Run("normalization_applies_to_label_any_and_exclude_label", func(t *testing.T) {
		anyIDs := idSet(bdBlockedJSON(t, bd, dir, "--label-any", " theme:beta"))
		if !anyIDs[beta.ID] {
			t.Errorf("expected ' theme:beta' to match via --label-any, got %v", anyIDs)
		}
		excludeIDs := idSet(bdBlockedJSON(t, bd, dir, "--exclude-label", " theme:alpha"))
		if excludeIDs[alpha.ID] || excludeIDs[alphaUrgent.ID] {
			t.Errorf("' theme:alpha' failed to exclude the alpha issues: %v", excludeIDs)
		}
		if !excludeIDs[beta.ID] {
			t.Errorf("exclusion dropped %s, which it should have kept: %v", beta.ID, excludeIDs)
		}
	})
}

// unresolvableBlocksDriver is the four operations
// assertUnresolvableBlocksMembership needs, so the embedded and proxied-server
// configurations run one fixture with one set of expectations.
type unresolvableBlocksDriver struct {
	create  func(title string) string
	depAdd  func(from, to string)
	ready   func() map[string]bool
	blocked func() map[string]bool
}

// assertUnresolvableBlocksMembership pins bd ready and bd blocked membership
// for a blocks edge onto a local open issue, an `external:` reference and a
// cross-repo id with no row here (bd-uschhs AC-8). The combined outgoing read
// changes only what bd show names; readiness and the blocked policy keep
// their existing answers, so this passes before and after the change. Those
// answers differ by target shape and are pinned as they stand: an `external:`
// reference blocks, while a bare cross-repo id with no row does not.
func assertUnresolvableBlocksMembership(t *testing.T, d unresolvableBlocksDriver) {
	t.Helper()
	blocker := d.create("Local open blocker")
	local := d.create("Blocked by a local issue")
	external := d.create("Blocked by an external reference")
	missing := d.create("Blocked by a cross-repo id")
	free := d.create("No blocking edge")
	d.depAdd(local, blocker)
	d.depAdd(external, "external:otherproj:cap")
	d.depAdd(missing, "liveop-gone")

	ready, blocked := d.ready(), d.blocked()
	for _, tc := range []struct {
		name, id       string
		ready, blocked bool
	}{
		{"local", local, false, true},
		{"external", external, false, true},
		{"missing", missing, true, false},
		{"free", free, true, false},
		{"blocker", blocker, true, false},
	} {
		if ready[tc.id] != tc.ready {
			t.Errorf("%s (%s): in bd ready = %v, want %v", tc.name, tc.id, ready[tc.id], tc.ready)
		}
		if blocked[tc.id] != tc.blocked {
			t.Errorf("%s (%s): in bd blocked = %v, want %v", tc.name, tc.id, blocked[tc.id], tc.blocked)
		}
	}
}
