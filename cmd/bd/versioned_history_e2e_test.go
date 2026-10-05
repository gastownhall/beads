//go:build cgo

package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// The end-to-end claim of versioned-history activation, against real embedded
// stores and the table it exists to fill: issue_versions.
//
// It is end-to-end on purpose, for the reason the events-journal E2Es are.
// Activation failing is INVISIBLE from inside the process -- the command
// succeeds, the write lands, every exit code is 0 -- and only a later count of
// version rows shows that nothing was recorded. This feature was wrong that way
// once already: the switch was read and applied where the root pre-run wraps the
// command's own store, which is the one store a routed update, a routed close and
// `bd create --repo` do NOT use. Each of those builds its own write-capable store
// for the target workspace, ran with history off, and reported success. A green
// unit suite was consistent with the feature doing nothing.
//
// The switch is a property of the STORE. `bd config set versioned-history.enabled
// true` writes a row of the workspace's own config table, and a store answers
// for itself: a write routed into rig B reads rig B's row, never the launching
// workspace's. The environment (and a hand-edited yaml value) is process-wide and
// can only turn recording ON; nothing but the row can turn a store off.
//
// Runs in the embedded-Dolt lane only (BEADS_TEST_EMBEDDED_DOLT=1), like every
// other test that builds and drives a real bd binary. The by-construction
// coverage that runs everywhere is the construction guard and the resolver's own
// tests.

// vhEnvVar is viper's spelling of versioned-history.enabled: the BD_ prefix, with
// dots and hyphens mapped to underscores.
const vhEnvVar = "BD_VERSIONED_HISTORY_ENABLED"

func vhRequireEmbeddedLane(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
}

// vhEnv is a subprocess environment in which nothing from the developer's own
// shell can turn the feature on or off: only what a step passes explicitly.
func vhEnv(dir string, extra ...string) []string {
	env := envWithout(bdEnv(dir), vhEnvVar)
	env = envWithout(env, "BD_EVENTS_JOURNAL")
	return append(env, extra...)
}

// vhBD runs bd and returns stdout and stderr separately. It fails the test on a
// non-zero exit.
func vhBD(t *testing.T, bd, dir string, extraEnv []string, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = vhEnv(dir, extraEnv...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd %s in %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), dir, err, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

type vhWorkspace struct {
	name     string
	dir      string
	beadsDir string
	database string
}

func vhNewWorkspace(t *testing.T, bd, dir, prefix string) vhWorkspace {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, dir)
	runBDInit(t, bd, dir, "--prefix", prefix, "--skip-hooks", "--skip-agents")
	return vhWorkspace{name: prefix, dir: dir, beadsDir: filepath.Join(dir, ".beads"), database: prefix}
}

// vhQuery runs one query against a workspace's embedded database. Callers must
// not have a bd process running against it: the embedded store is single-writer.
func vhQuery[T any](t *testing.T, w vhWorkspace, scan func(*sql.Row) (T, error), query string, args ...any) T {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(w.beadsDir, "embeddeddolt"), w.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL(%s): %v", w.database, err)
	}
	defer func() { _ = cleanup() }()
	got, err := scan(db.QueryRowContext(t.Context(), query, args...))
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return got
}

// vhVersions counts the issue_versions rows in one workspace's database.
func vhVersions(t *testing.T, w vhWorkspace) int {
	t.Helper()
	return vhQuery(t, w, func(r *sql.Row) (int, error) {
		var n int
		return n, r.Scan(&n)
	}, "SELECT COUNT(*) FROM issue_versions")
}

// vhRow reads the switch's row out of one workspace's config table. present is
// false when there is no such row, which is the state of a workspace that never
// ran `bd config set`.
func vhRow(t *testing.T, w vhWorkspace) (value string, present bool) {
	t.Helper()
	type result struct {
		value   string
		present bool
	}
	got := vhQuery(t, w, func(r *sql.Row) (result, error) {
		var v string
		err := r.Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return result{}, nil
		}
		return result{v, true}, err
	}, "SELECT value FROM config WHERE `key` = ?", versionedHistorySettingKey)
	return got.value, got.present
}

// TestVersionedHistoryConfigSetWritesTheStoreRow is the front door, and D5's
// regression pin for the plane #6661 chose. `bd config set` has to write the
// database row the stores read, and must not write config.yaml: the key is not a
// yaml-only key, and making it one would move the switch off the store and back
// to the per-process plane this feature was moved away from.
func TestVersionedHistoryConfigSetWritesTheStoreRow(t *testing.T) {
	vhRequireEmbeddedLane(t)
	bd := buildEmbeddedBD(t)
	w := vhNewWorkspace(t, bd, filepath.Join(t.TempDir(), "ws"), "vhc")

	if value, present := vhRow(t, w); present {
		t.Fatalf("a fresh workspace already has a %s row (%q); the test's premise is wrong", versionedHistorySettingKey, value)
	}

	vhBD(t, bd, w.dir, nil, "config", "set", versionedHistorySettingKey, "true")

	if value, present := vhRow(t, w); !present || value != "true" {
		t.Errorf("config table holds (%q, present=%v) for %s after `bd config set ... true`, want (\"true\", true)", value, present, versionedHistorySettingKey)
	}
	for _, name := range []string{"config.yaml", "config.local.yaml"} {
		raw, err := os.ReadFile(filepath.Join(w.beadsDir, name))
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), "versioned-history") {
			t.Errorf("`bd config set %s` wrote to %s; the switch is a store row, not a yaml key:\n%s", versionedHistorySettingKey, name, raw)
		}
	}
	stdout, _ := vhBD(t, bd, w.dir, nil, "config", "get", versionedHistorySettingKey)
	if !strings.Contains(stdout, "true") {
		t.Errorf("`bd config get` does not read back the value `bd config set` wrote: %q", stdout)
	}

	vhBD(t, bd, w.dir, nil, "config", "set", versionedHistorySettingKey, "false")
	if value, present := vhRow(t, w); !present || value != "false" {
		t.Errorf("config table holds (%q, present=%v) after `bd config set ... false`, want (\"false\", true): the row is how a store is turned off", value, present)
	}
}

// vhMatrix is the fixture for the routed-write matrix: a launching workspace
// (the "town") and two rigs it routes into by issue-ID prefix.
type vhMatrix struct {
	bd               string
	town, rigA, rigB vhWorkspace
}

func (m vhMatrix) workspaces() []vhWorkspace { return []vhWorkspace{m.town, m.rigA, m.rigB} }

func (m vhMatrix) setRow(t *testing.T, w vhWorkspace, value string) {
	t.Helper()
	vhBD(t, m.bd, w.dir, nil, "config", "set", versionedHistorySettingKey, value)
}

// routedUpdate writes into a rig FROM THE TOWN, through the prefix route
// (routed.go: newDoltStoreFromConfig on the target's .beads).
func (m vhMatrix) routedUpdate(t *testing.T, id, title string, env ...string) {
	t.Helper()
	vhBD(t, m.bd, m.town.dir, env, "update", id, "--title", title)
}

// routedClose is the other prefix-routed write.
func (m vhMatrix) routedClose(t *testing.T, id string, env ...string) {
	t.Helper()
	vhBD(t, m.bd, m.town.dir, env, "close", id)
}

// routedCreate writes a new issue into a rig FROM THE TOWN, through --repo
// (create.go: newDoltStoreFromConfig on the target's .beads).
func (m vhMatrix) routedCreate(t *testing.T, target vhWorkspace, title string, env ...string) {
	t.Helper()
	vhBD(t, m.bd, m.town.dir, env, "create", "--silent", "--repo", target.dir, title)
}

func (m vhMatrix) create(t *testing.T, w vhWorkspace, title string) string {
	t.Helper()
	out, _ := vhBD(t, m.bd, w.dir, nil, "create", "--silent", title)
	return strings.TrimSpace(out)
}

// expectDelta runs a write and asserts how many version rows EVERY workspace
// gained, so a write that lands in the wrong place fails as loudly as one that
// records nothing. want is keyed by workspace name; anything unlisted must gain
// none -- including the launching workspace, which never records a write that
// was routed into another one.
func (m vhMatrix) expectDelta(t *testing.T, step string, want map[string]int, write func()) {
	t.Helper()
	before := map[string]int{}
	for _, w := range m.workspaces() {
		before[w.name] = vhVersions(t, w)
	}
	write()
	var counts []string
	for _, w := range m.workspaces() {
		after := vhVersions(t, w)
		counts = append(counts, fmt.Sprintf("%s %d->%d", w.name, before[w.name], after))
		if got := after - before[w.name]; got != want[w.name] {
			t.Errorf("%s: %s gained %d issue_versions row(s), want %d (before %d, after %d)",
				step, w.name, got, want[w.name], before[w.name], after)
		}
	}
	// The measured counts, not just the verdict: with -v this is the
	// before/after evidence for every routed write.
	t.Logf("%s: issue_versions rows: %s", step, strings.Join(counts, ", "))
}

// newVHMatrix builds the town and its two rigs, with history off everywhere and
// a routes file mapping each rig's prefix to it.
func newVHMatrix(t *testing.T) vhMatrix {
	t.Helper()
	bd := buildEmbeddedBD(t)
	root := t.TempDir()
	m := vhMatrix{bd: bd}
	m.town = vhNewWorkspace(t, bd, filepath.Join(root, "town"), "vht")
	m.rigA = vhNewWorkspace(t, bd, filepath.Join(m.town.dir, "rig-a"), "vha")
	m.rigB = vhNewWorkspace(t, bd, filepath.Join(m.town.dir, "rig-b"), "vhb")
	routes := `{"prefix":"vha-","path":"rig-a"}` + "\n" + `{"prefix":"vhb-","path":"rig-b"}` + "\n"
	if err := os.WriteFile(filepath.Join(m.town.beadsDir, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	return m
}

// TestVersionedHistoryFollowsTheStoreItWritesInto is the routed-write matrix, with
// a count of issue_versions rows in every workspace after every write.
//
// D1: with the TARGET's row on and the launching workspace's row absent, a routed
// update, a routed close and `bd create --repo` each add exactly one version row
// to the target. At the commit this was built on, they add none: the store they
// open is never activated.
//
// D2: the reverse. The launching workspace's row on and the target's off (or
// absent), env unset: the routed write adds none. A store answers for itself, so
// the town's answer cannot leak into a store it opened for someone else.
func TestVersionedHistoryFollowsTheStoreItWritesInto(t *testing.T) {
	vhRequireEmbeddedLane(t)
	m := newVHMatrix(t)

	// One issue in each rig, plus one to close, all created while history is off.
	idA, idAClose, idB := m.create(t, m.rigA, "seed a"), m.create(t, m.rigA, "seed a close"), m.create(t, m.rigB, "seed b")

	m.expectDelta(t, "control: history off everywhere, routed update", nil, func() { m.routedUpdate(t, idA, "control") })

	t.Run("D1 target row on, launching workspace row absent", func(t *testing.T) {
		m.setRow(t, m.rigA, "true")
		if _, present := vhRow(t, m.town); present {
			t.Fatal("the launching workspace has a row; this subtest's premise is that it has none")
		}

		m.expectDelta(t, "routed update into the enabling rig", map[string]int{"vha": 1}, func() { m.routedUpdate(t, idA, "routed update a") })
		m.expectDelta(t, "routed close into the enabling rig", map[string]int{"vha": 1}, func() { m.routedClose(t, idAClose) })
		m.expectDelta(t, "bd create --repo into the enabling rig", map[string]int{"vha": 1}, func() { m.routedCreate(t, m.rigA, "routed create a") })
		// A second rig that never asked stays at zero while the first records.
		m.expectDelta(t, "routed update into a rig that never enabled history", nil, func() { m.routedUpdate(t, idB, "routed update b") })
		m.expectDelta(t, "bd create --repo into a rig that never enabled history", nil, func() { m.routedCreate(t, m.rigB, "routed create b") })
		m.expectDelta(t, "direct write inside the enabling rig", map[string]int{"vha": 1}, func() {
			vhBD(t, m.bd, m.rigA.dir, nil, "update", idA, "--title", "direct a")
		})
	})

	t.Run("flipping it back is a plain config change", func(t *testing.T) {
		m.setRow(t, m.rigA, "false")
		m.expectDelta(t, "routed update after setting the rig's row back to false", nil, func() { m.routedUpdate(t, idA, "after flip back") })
		m.setRow(t, m.rigA, "true")
		m.expectDelta(t, "routed update after enabling again, with no restart of anything", map[string]int{"vha": 1}, func() {
			m.routedUpdate(t, idA, "after flip forward")
		})
		m.setRow(t, m.rigA, "false")
	})

	t.Run("D2 launching workspace row on, target row off", func(t *testing.T) {
		m.setRow(t, m.town, "true")
		// The routed stores the town opens belong to workspaces that did not ask
		// (rig A says false outright, rig B says nothing) and must not inherit the
		// town's answer.
		m.expectDelta(t, "routed update into a rig whose own row says false", nil, func() { m.routedUpdate(t, idA, "leak check update") })
		m.expectDelta(t, "bd create --repo into a rig whose own row says false", nil, func() { m.routedCreate(t, m.rigA, "leak check create") })
		m.expectDelta(t, "routed update into a rig with no row at all", nil, func() { m.routedUpdate(t, idB, "leak check b") })
		// The town's own store, in the town's own workspace, does record.
		m.expectDelta(t, "the launching workspace's own write", map[string]int{"vht": 1}, func() {
			m.create(t, m.town, "town's own issue")
		})
		m.setRow(t, m.town, "false")
	})
}

// TestVersionedHistoryEnvIsProcessWideAndOnlyTurnsRecordingOn is D3. The
// environment applies to every store the process opens, routed targets included;
// it can turn recording on for a store that never asked, and it can never turn it
// off for a store whose row says on. Both planes are OR'd, never ranked: the
// hazard is a write that fails to record, never one that records when it needn't.
func TestVersionedHistoryEnvIsProcessWideAndOnlyTurnsRecordingOn(t *testing.T) {
	vhRequireEmbeddedLane(t)
	m := newVHMatrix(t)
	idA, idB := m.create(t, m.rigA, "seed a"), m.create(t, m.rigB, "seed b")

	on := []string{vhEnvVar + "=1"}
	// Every store this process opens records, whatever its own row says.
	m.expectDelta(t, "routed writes into both rigs under the env override", map[string]int{"vha": 1, "vhb": 1}, func() {
		m.routedUpdate(t, idA, "env a", on...)
		m.routedUpdate(t, idB, "env b", on...)
	})
	m.expectDelta(t, "bd create --repo under the env override", map[string]int{"vhb": 1}, func() {
		m.routedCreate(t, m.rigB, "env create b", on...)
	})

	// =0 is not "off". A store whose row says on keeps recording, and a store
	// with no row stays quiet.
	m.setRow(t, m.rigA, "true")
	off := []string{vhEnvVar + "=0"}
	m.expectDelta(t, "env=0 over a rig whose row enables history", map[string]int{"vha": 1}, func() {
		m.routedUpdate(t, idA, "env off a", off...)
	})
	m.expectDelta(t, "env=0 over a rig with no row", nil, func() {
		m.routedUpdate(t, idB, "env off b", off...)
	})

	// The only way off is the row.
	m.setRow(t, m.rigA, "false")
	m.expectDelta(t, "routed update after the row is set back to false", nil, func() {
		m.routedUpdate(t, idA, "row off a")
	})
}
