//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestEmbeddedRepoStoredPathLifecycle drives bd repo add/sync/remove against a
// real embedded store (bd-59llyl): a relative add from a subdirectory stores
// an absolute entry that sync and remove agree on from any cwd, remove deletes
// only that entry's hydrated issues and clears its sync cache, and a legacy
// relative entry stays removable by its stored spelling.
func TestEmbeddedRepoStoredPathLifecycle(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt repo tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	primary, primaryBeads, _ := bdInit(t, bd, "--prefix", "pr")
	configPath := filepath.Join(primaryBeads, "config.yaml")
	local := bdCreate(t, bd, primary, "Local issue")

	// Hydration sources: siblings of the primary workspace under the test's
	// temp root, each with an issues.jsonl export.
	parent := filepath.Dir(primary)
	other := writeHydrationSource(t, parent, "other", "ot")
	third := writeHydrationSource(t, parent, "third", "th")
	sub := filepath.Join(primary, "sub", "dir")
	elsewhere := t.TempDir()
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(cwd string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = cwd
		cmd.Env = append(bdEnv(primary), "BEADS_DIR="+primaryBeads)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	mustRun := func(cwd string, args ...string) {
		t.Helper()
		if out, err := run(cwd, args...); err != nil {
			t.Fatalf("bd %s (cwd %s): %v\n%s", strings.Join(args, " "), cwd, err, out)
		}
	}
	additional := func() []string {
		t.Helper()
		repos, err := config.GetReposFromYAML(configPath)
		if err != nil {
			t.Fatal(err)
		}
		return repos.Additional
	}
	ids := func() string {
		t.Helper()
		var got []string
		for _, issue := range bdListJSONAllowError(t, bd, primary, "--all", "--limit", "0") {
			got = append(got, issue.ID)
		}
		sort.Strings(got)
		return strings.Join(got, " ")
	}

	relOther, err := filepath.Rel(sub, other)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(sub, "repo", "add", relOther)
	mustRun(primary, "repo", "add", third)
	if got := additional(); len(got) != 2 || got[0] != other || got[1] != third {
		t.Fatalf("repos.additional = %q, want [%q %q]", got, other, third)
	}

	mustRun(elsewhere, "repo", "sync")
	all := strings.Join([]string{local.ID, "ot-1", "ot-2", "th-1"}, " ")
	if got := ids(); got != sortedRepoIssueIDs(all) {
		t.Fatalf("issues after sync = %q, want %q", got, sortedRepoIssueIDs(all))
	}

	// Remove by the relative spelling it was added with, from that cwd.
	mustRun(sub, "repo", "remove", relOther)
	if got := additional(); len(got) != 1 || got[0] != third {
		t.Fatalf("repos.additional after remove = %q, want [%q]", got, third)
	}
	if got, want := ids(), sortedRepoIssueIDs(local.ID+" th-1"); got != want {
		t.Fatalf("issues after remove = %q, want %q", got, want)
	}

	// Re-add and sync: remove cleared the entry's sync cache, so the
	// unchanged export imports again.
	mustRun(sub, "repo", "add", relOther)
	mustRun(elsewhere, "repo", "sync")
	if got := ids(); got != sortedRepoIssueIDs(all) {
		t.Fatalf("issues after re-add and sync = %q, want %q", got, sortedRepoIssueIDs(all))
	}

	// Remove by the stored absolute string from an unrelated cwd.
	mustRun(elsewhere, "repo", "remove", other)
	if got, want := ids(), sortedRepoIssueIDs(local.ID+" th-1"); got != want {
		t.Fatalf("issues after absolute remove = %q, want %q", got, want)
	}

	// Invalid arguments fail and leave config and issues alone.
	for _, arg := range []string{"", relOther, "../no-such-repo"} {
		if out, err := run(sub, "repo", "remove", arg); err == nil {
			t.Fatalf("bd repo remove %q succeeded, want an error:\n%s", arg, out)
		}
	}
	if got := additional(); len(got) != 1 || got[0] != third {
		t.Fatalf("repos.additional after failed removes = %q, want [%q]", got, third)
	}
	if got, want := ids(), sortedRepoIssueIDs(local.ID+" th-1"); got != want {
		t.Fatalf("issues after failed removes = %q, want %q", got, want)
	}

	// A legacy relative entry, written verbatim by an older bd, is removed by
	// its stored spelling from another cwd along with its hydrated issues.
	mustRun(primary, "repo", "remove", third)
	if err := config.SetReposInYAML(configPath, &config.ReposConfig{Primary: ".", Additional: []string{"../third"}}); err != nil {
		t.Fatal(err)
	}
	mustRun(primary, "repo", "sync")
	if got, want := ids(), sortedRepoIssueIDs(local.ID+" th-1"); got != want {
		t.Fatalf("issues after legacy sync = %q, want %q", got, want)
	}
	mustRun(elsewhere, "repo", "remove", "../third")
	if got := additional(); len(got) != 0 {
		t.Fatalf("repos.additional after legacy remove = %q, want empty", got)
	}
	if got := ids(); got != local.ID {
		t.Fatalf("issues after legacy remove = %q, want %q", got, local.ID)
	}
}

// writeHydrationSource creates parent/name/.beads/issues.jsonl holding
// prefix-1 and, for "ot", prefix-2, and returns parent/name.
func writeHydrationSource(t *testing.T, parent, name, prefix string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	n := 1
	if prefix == "ot" {
		n = 2
	}
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(`{"id":"` + prefix + "-" + string(rune('0'+i)) + `","title":"` + name + ` issue","status":"open","priority":2,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sortedRepoIssueIDs(ids string) string {
	fields := strings.Fields(ids)
	sort.Strings(fields)
	return strings.Join(fields, " ")
}
