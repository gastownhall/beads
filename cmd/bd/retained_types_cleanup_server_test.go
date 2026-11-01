//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAdminCleanupServerModeKeepsRetainedTypes drives `bd admin cleanup`, a
// server-mode-only command, end to end against a Dolt sql-server: with
// types.retained naming a type, `--force` deletes the other closed issues and
// keeps the retained ones — a no-history row included, which cleanup's
// unfiltered search merges in from the wisps table — and reports them as
// retention_required. `--cascade` is refused while the setting names a type,
// because the Deleter role expands a cascade to every dependent past the
// guarded selection.
//
// The setting is written as "op,Chore": the stored spelling of a built-in type
// in another case is canonicalized by the read, so it still protects the
// `chore` rows bd writes.
func TestAdminCleanupServerModeKeepsRetainedTypes(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	l := &migratePersonalServerLeg{t: t, bd: buildBDUnderTest(t), env: externalServerTestEnv(t)}
	dir, _ := l.initServerRepo("rkc")
	l.run(dir, nil, "config", "set", "types.custom", "op")
	l.run(dir, nil, "config", "set", "types.retained", "op,Chore")

	closed := func(title string, args ...string) string {
		t.Helper()
		id := l.create(dir, title, args...)
		l.run(dir, nil, "close", id)
		return id
	}
	keptOp := closed("op row", "--type", "op", "--no-history")
	keptChore := closed("retained chore", "--type", "chore")
	deleted := closed("ordinary task", "--type", "task")

	if out, err := l.runResult(dir, "admin", "cleanup", "--cascade", "--force"); err == nil ||
		!strings.Contains(out, "--cascade is refused") || !strings.Contains(out, "types.retained") {
		t.Fatalf("bd admin cleanup --cascade --force: err = %v, want a refusal naming --cascade and types.retained:\n%s", err, out)
	}
	if l.title(dir, deleted) == "" {
		t.Fatalf("bd admin cleanup --cascade --force deleted %s although it was refused", deleted)
	}

	out := l.run(dir, nil, "admin", "cleanup", "--force", "--json")
	var report map[string]any
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("bd admin cleanup --force --json printed no JSON object:\n%s", out)
	}
	if err := json.Unmarshal([]byte(out[start:]), &report); err != nil {
		t.Fatalf("bd admin cleanup --force --json: %v\n%s", err, out)
	}
	if report["deleted_count"] != float64(1) || report["retention_required"] != float64(2) {
		t.Errorf("bd admin cleanup --force --json = %v, want deleted_count 1 and retention_required 2", report)
	}
	if l.title(dir, deleted) != "" {
		t.Errorf("bd admin cleanup --force kept %s, which is not of a retained type", deleted)
	}
	if l.title(dir, keptChore) == "" {
		t.Errorf("bd admin cleanup --force deleted %s, a chore that types.retained (Chore) names", keptChore)
	}
	if l.wispTitle(dir, keptOp) == "" {
		t.Errorf("bd admin cleanup --force deleted %s, a no-history op row that types.retained names", keptOp)
	}

	text := l.run(dir, nil, "admin", "cleanup", "--force")
	if !strings.Contains(text, "Kept 2 closed issue(s) of a retained type") || !strings.Contains(text, "No closed issues to delete") {
		t.Errorf("bd admin cleanup --force with only retained issues left, want the kept line and nothing to delete:\n%s", text)
	}
}

// runResult runs bd with args in dir and returns its combined output and
// error, for a command the test expects to fail.
func (l *migratePersonalServerLeg) runResult(dir string, args ...string) (string, error) {
	l.t.Helper()
	return runExternalServerBD(l.t, dir, l.env, args...)
}

// wispTitle returns the title of issue id in dir's wisps table, or "" when it
// holds no such row.
func (l *migratePersonalServerLeg) wispTitle(dir, id string) string {
	l.t.Helper()
	rows := l.rows(dir, "SELECT title FROM wisps WHERE id = '"+id+"'")
	if len(rows) == 0 {
		return ""
	}
	return rows[0]
}
