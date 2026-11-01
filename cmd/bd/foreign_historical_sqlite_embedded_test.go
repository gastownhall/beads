//go:build cgo

package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// TestEmbeddedForeignHistoricalSQLiteWorkspaceLeftUnchanged pins, at each
// user-visible entry point that opens or inspects a workspace bd was not
// launched in, that a historical SQLite workspace there is left byte-identical
// and is refused (or skipped) with the refusal bd prints when run inside it,
// prefixed with the entry point's own context. Every row runs every
// historical SQLite layout, so an entry point that bypasses the shared
// discovery check (configfile.LoadDiscoveredForOpen) is caught whichever
// layout it mishandles.
func TestEmbeddedForeignHistoricalSQLiteWorkspaceLeftUnchanged(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fh")
	local := crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)}
	blocked := local.create(t, "Needs the old capability")
	parent := local.create(t, "Parent")
	localConfig := filepath.Join(beadsDir, "config.local.yaml")

	// Each row configures the launcher to reach repo (a fresh copy of one
	// layout), runs one command, and reports the refusal text it must print,
	// or "" for an entry point that skips the workspace instead.
	type entryPoint struct {
		name string
		// dir is where bd runs for repo; nil runs it in the launcher.
		dir func(t *testing.T, repo string) string
		// config is the launcher's config.local.yaml for repo; "" writes none.
		config func(repo string) string
		// routes is the launcher's routes.jsonl for repo; nil writes none.
		routes func(repo string) string
		args   func(repo string) []string
		// want is the refusal's context prefix; "" means a skip, which must
		// not refuse the command on the workspace's account.
		want func(repo string) string
		// skipWarning, for a skip, is a line it must still print.
		skipWarning string
		// blockedByExternal marks a skip whose command fails because the
		// skipped project leaves its issue blocked, not because of the
		// workspace's layout.
		blockedByExternal bool
		// namesBeadsDir marks a refusal that may also come, naming the
		// refused .beads after want's prefix, from the entry point's own
		// check rather than from admission of a workspace discovery found.
		namesBeadsDir bool
	}
	external := func(repo string) string {
		return fmt.Sprintf("external_projects:\n  old: %q\n", repo)
	}
	routedDefault := func(repo string) string {
		return fmt.Sprintf("routing:\n  default: %q\n", repo)
	}
	targetRepo := func(repo string) string { return "Error: target repo " + repo + ": " }
	// A prefix route names its rig relative to the launcher's town root.
	routePath := func(repo string) string {
		rel, err := filepath.Rel(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		return rel
	}
	prefixRoute := func(repo string) string {
		return fmt.Sprintf(`{"prefix":"hr-","path":%q}`+"\n", routePath(repo))
	}
	prefixRouted := func(repo string) string {
		return fmt.Sprintf("prefix route %q to %s: ", "hr-", routePath(repo))
	}
	entryPoints := []entryPoint{
		{name: "ready (external dependency)", config: external,
			args:        func(string) []string { return []string{"ready"} },
			skipWarning: `external project "old" is unavailable`},
		{name: "blocked (external dependency)", config: external,
			args:        func(string) []string { return []string{"blocked"} },
			skipWarning: `external project "old" is unavailable`},
		{name: "dep tree (external dependency)", config: external,
			args: func(string) []string { return []string{"dep", "tree", blocked} }},
		{name: "show (external dependency)", config: external,
			args: func(string) []string { return []string{"show", blocked} }},
		{name: "update --claim (external dependency)", config: external,
			args:              func(string) []string { return []string{"update", blocked, "--claim"} },
			skipWarning:       "is blocked by [external:old:cap]",
			blockedByExternal: true},
		{name: "show (prefix-routed read)", routes: prefixRoute,
			args: func(string) []string { return []string{"show", "hr-abc"} },
			want: prefixRouted},
		{name: "update (prefix-routed write)", routes: prefixRoute,
			args: func(string) []string { return []string{"update", "hr-abc", "--status", "in_progress"} },
			want: prefixRouted},
		{name: "list (auto-routed read)", config: routedDefault,
			args: func(string) []string { return []string{"list"} },
			want: func(repo string) string { return "failed to open routed store at " + repo + ": " }},
		{name: "update (auto-routed write)", config: routedDefault,
			args: func(string) []string { return []string{"update", "fh-zzz", "--status", "in_progress"} },
			want: func(repo string) string { return "failed to open routed store at " + repo + ": " }},
		{name: "create --repo",
			args: func(repo string) []string { return []string{"create", "routed", "--repo", repo} },
			want: targetRepo},
		{name: "create --dry-run --repo",
			args: func(repo string) []string { return []string{"create", "routed", "--dry-run", "--repo", repo} },
			want: targetRepo},
		{name: "create --dry-run --repo --parent",
			args: func(repo string) []string {
				return []string{"create", "routed", "--dry-run", "--repo", repo, "--parent", parent}
			},
			want: targetRepo},
		{name: "migrate-personal (planning repo)",
			config: func(repo string) string {
				return fmt.Sprintf("routing:\n  mode: auto\n  contributor: %q\n", repo)
			},
			args: func(string) []string { return []string{"migrate-personal", "--yes"} },
			want: func(repo string) string { return "Error: planning repo " + repo + ": " }},
		// connect attaches the .beads it resolves itself, which from a linked
		// worktree with none of its own is the main checkout's, a workspace
		// admission never checked. Nothing listens on the url.
		{name: "connect from a linked worktree",
			dir:  func(t *testing.T, repo string) string { return graphModeLinkedWorktree(t, repo) },
			args: func(string) []string { return []string{"connect", "http://127.0.0.1:1"} },
			want: func(string) string { return "Error: " }, namesBeadsDir: true},
		{name: "connect --clear from a linked worktree",
			dir:  func(t *testing.T, repo string) string { return graphModeLinkedWorktree(t, repo) },
			args: func(string) []string { return []string{"connect", "--clear"} },
			want: func(string) string { return "Error: " }, namesBeadsDir: true},
		// --db selects the workspace admission then checks: already refused.
		{name: "--db",
			args: func(repo string) []string { return []string{"--db", filepath.Join(repo, ".beads"), "list"} },
			want: func(string) string { return "Error: " }},
	}

	if _, _, code := local.run(t, "dep", "add", blocked, "external:old:cap"); code != 0 {
		t.Fatalf("seeding the external dependency failed with exit %d", code)
	}
	for _, ep := range entryPoints {
		for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
			t.Run(ep.name+"/"+layout.Name, func(t *testing.T) {
				repo := t.TempDir()
				initGitRepoAt(t, repo)
				layout.Write(t, filepath.Join(repo, ".beads"))
				before := legacyworkspace.TreeDigest(t, filepath.Join(repo, ".beads"))
				if ep.config != nil {
					if err := os.WriteFile(localConfig, []byte(ep.config(repo)), 0o600); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = os.Remove(localConfig) }()
				}
				if ep.routes != nil {
					routesFile := filepath.Join(beadsDir, "routes.jsonl")
					if err := os.WriteFile(routesFile, []byte(ep.routes(repo)), 0o600); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = os.Remove(routesFile) }()
				}

				run := local
				if ep.dir != nil {
					run.dir = ep.dir(t, repo)
				}
				stdout, stderr, code := run.run(t, ep.args(repo)...)
				if after := legacyworkspace.TreeDigest(t, filepath.Join(repo, ".beads")); !maps.Equal(after, before) {
					t.Fatalf("bd %s changed the foreign workspace: before %v, after %v\nstdout:\n%s\nstderr:\n%s",
						strings.Join(ep.args(repo), " "), slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)), stdout, stderr)
				}
				if ep.want == nil {
					if (code != 0 && !ep.blockedByExternal) || strings.Contains(stderr+stdout, "explicit migration is required") {
						t.Fatalf("bd %s refused the command over a workspace it skips (exit %d):\nstdout:\n%s\nstderr:\n%s", strings.Join(ep.args(repo), " "), code, stdout, stderr)
					}
					if ep.skipWarning != "" && !strings.Contains(stderr, ep.skipWarning) {
						t.Fatalf("bd %s stderr lacks %q:\n%s", strings.Join(ep.args(repo), " "), ep.skipWarning, stderr)
					}
					return
				}
				want := ep.want(repo) + legacyworkspace.HistoricalSQLiteRefusal
				named := ep.want(repo) + filepath.Join(repo, ".beads") + ": " + legacyworkspace.HistoricalSQLiteRefusal
				if code == 0 || !(strings.Contains(stderr+stdout, want) || ep.namesBeadsDir && strings.Contains(stderr+stdout, named)) {
					t.Fatalf("bd %s exit %d, want the refusal %q\nstdout:\n%s\nstderr:\n%s",
						strings.Join(ep.args(repo), " "), code, want, stdout, stderr)
				}
			})
		}
	}
}

// TestEmbeddedRoutedTargetRefusesLegacyDoltWorkspace pins, at each
// user-visible entry point that opens a workspace bd was not launched in, that
// a legacy Dolt workspace there (a non-empty .beads/dolt root, no embedded
// repository and no current-era version witness) is refused as bd refuses it
// when run inside it, with that text and the entry point's own context, or
// skipped where the entry point skips, and is left byte-identical. Every row
// runs every legacy Dolt layout, including one whose server mode comes only
// from its own config.yaml and a bd 0.62.0 server workspace. Before, a routed
// create created an embedded database beside the legacy root, which hid its
// data and turned the refusal bd prints there into an empty workspace;
// migrate-personal reported a missing database; a create --dry-run previewed
// what the create refused; and the routed reads and writes and external
// dependencies opened it, which in server mode auto-started a Dolt server
// inside it and, for a write, ran bd's schema migrations into its database.
func TestEmbeddedRoutedTargetRefusesLegacyDoltWorkspace(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fl")
	local := crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)}
	localConfig := filepath.Join(beadsDir, "config.local.yaml")

	type entryPoint struct {
		name   string
		config func(repo string) string
		routes func(repo string) string
		args   func(repo string) []string
		// want is the refusal's context prefix; nil marks a skip, which must
		// print skipWarning and not refuse the command.
		want        func(repo string) string
		skipWarning string
	}
	targetRepo := func(repo string) string { return "Error: target repo " + repo + ": " }
	routedDefault := func(repo string) string {
		return fmt.Sprintf("routing:\n  default: %q\n", repo)
	}
	routedAt := func(repo string) string { return "failed to open routed store at " + repo + ": " }
	routePath := func(repo string) string {
		rel, err := filepath.Rel(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		return rel
	}
	prefixRoute := func(repo string) string {
		return fmt.Sprintf(`{"prefix":"hr-","path":%q}`+"\n", routePath(repo))
	}
	prefixRouted := func(repo string) string {
		return fmt.Sprintf("prefix route %q to %s: ", "hr-", routePath(repo))
	}
	// migrate-personal moves personal issues only when there are some.
	parent := local.create(t, "Personal")
	entryPoints := []entryPoint{
		{name: "create --repo",
			args: func(repo string) []string { return []string{"create", "routed", "--repo", repo} },
			want: targetRepo},
		{name: "create --dry-run --repo",
			args: func(repo string) []string { return []string{"create", "routed", "--dry-run", "--repo", repo} },
			want: targetRepo},
		{name: "create --dry-run --repo --parent",
			args: func(repo string) []string {
				return []string{"create", "routed", "--dry-run", "--repo", repo, "--parent", parent}
			},
			want: targetRepo},
		{name: "create (auto-routed)", config: routedDefault,
			args: func(string) []string { return []string{"create", "routed"} },
			want: targetRepo},
		{name: "migrate-personal (planning repo)",
			config: func(repo string) string {
				return fmt.Sprintf("routing:\n  mode: auto\n  contributor: %q\n", repo)
			},
			args: func(string) []string { return []string{"migrate-personal", "--yes"} },
			want: func(repo string) string { return "Error: planning repo " + repo + ": " }},
		{name: "list (auto-routed read)", config: routedDefault,
			args: func(string) []string { return []string{"list"} },
			want: routedAt},
		{name: "update (auto-routed write)", config: routedDefault,
			args: func(string) []string { return []string{"update", "fl-zzz", "--status", "in_progress"} },
			want: routedAt},
		{name: "show (prefix-routed read)", routes: prefixRoute,
			args: func(string) []string { return []string{"show", "hr-abc"} },
			want: prefixRouted},
		{name: "update (prefix-routed write)", routes: prefixRoute,
			args: func(string) []string { return []string{"update", "hr-abc", "--status", "in_progress"} },
			want: prefixRouted},
		{name: "ready (external dependency)",
			config:      func(repo string) string { return fmt.Sprintf("external_projects:\n  old: %q\n", repo) },
			args:        func(string) []string { return []string{"ready"} },
			skipWarning: `external project "old" is unavailable`},
	}
	blocked := local.create(t, "Needs the old capability")
	if _, _, code := local.run(t, "dep", "add", blocked, "external:old:cap"); code != 0 {
		t.Fatalf("seeding the external dependency failed with exit %d", code)
	}

	for _, layout := range legacyworkspace.LegacyDoltLayouts() {
		refusal := legacyworkspace.LegacyDoltRefusal(layout.Reason)
		for _, ep := range entryPoints {
			t.Run(layout.Name+"/"+ep.name, func(t *testing.T) {
				repo := t.TempDir()
				initGitRepoAt(t, repo)
				repoBeadsDir := filepath.Join(repo, ".beads")
				layout.Write(t, repoBeadsDir)
				inside := crossModeEnv{mode: "embedded", bd: bd, dir: repo, env: bdEnv(repo)}
				if _, stderr, code := inside.run(t, "list"); code == 0 || !strings.Contains(stderr, refusal) {
					t.Fatalf("bd list inside the legacy workspace did not refuse it with %q (exit %d):\n%s", refusal, code, stderr)
				}
				before := legacyworkspace.TreeDigest(t, repoBeadsDir)
				if ep.config != nil {
					if err := os.WriteFile(localConfig, []byte(ep.config(repo)), 0o600); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = os.Remove(localConfig) }()
				}
				if ep.routes != nil {
					routesFile := filepath.Join(beadsDir, "routes.jsonl")
					if err := os.WriteFile(routesFile, []byte(ep.routes(repo)), 0o600); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = os.Remove(routesFile) }()
				}

				stdout, stderr, code := local.run(t, ep.args(repo)...)
				if after := legacyworkspace.TreeDigest(t, repoBeadsDir); !maps.Equal(after, before) {
					t.Fatalf("bd %s changed the legacy workspace: before %v, after %v\nstdout:\n%s\nstderr:\n%s",
						strings.Join(ep.args(repo), " "), slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)), stdout, stderr)
				}
				if ep.want == nil {
					if code != 0 || strings.Contains(stderr+stdout, "explicit migration is required") {
						t.Fatalf("bd %s refused the command over a workspace it skips (exit %d):\nstdout:\n%s\nstderr:\n%s", strings.Join(ep.args(repo), " "), code, stdout, stderr)
					}
					if !strings.Contains(stderr, ep.skipWarning) {
						t.Fatalf("bd %s stderr lacks %q:\n%s", strings.Join(ep.args(repo), " "), ep.skipWarning, stderr)
					}
				} else if want := ep.want(repo) + refusal; code == 0 || !strings.Contains(stderr+stdout, want) {
					t.Fatalf("bd %s exit %d, want the refusal %q\nstdout:\n%s\nstderr:\n%s",
						strings.Join(ep.args(repo), " "), code, want, stdout, stderr)
				}
				if _, stderr, code := inside.run(t, "list"); code == 0 || !strings.Contains(stderr, refusal) {
					t.Fatalf("after bd %s, bd list inside the legacy workspace no longer refuses it (exit %d):\n%s",
						strings.Join(ep.args(repo), " "), code, stderr)
				}
			})
		}
	}
}

// TestEmbeddedForeignPreDoltCloneLeftUnchanged pins, at the entry points that
// read a workspace bd was not launched in, that a fresh clone of a SQLite-era
// project (its tracked config.json and issues.jsonl, no *.db) is left
// byte-identical. bd run inside it finds no workspace there and writes
// nothing, and the historical SQLite check does not refuse it (it needs the
// database), so an external dependency skips it with its usual warning and a
// routed open fails without writing. Before, an external dependency or an
// auto-routed read migrated its config.json into a metadata.json that dropped
// its version and jsonl_export.
func TestEmbeddedForeignPreDoltCloneLeftUnchanged(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fp")
	local := crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)}
	blocked := local.create(t, "Needs the old capability")
	localConfig := filepath.Join(beadsDir, "config.local.yaml")
	if _, _, code := local.run(t, "dep", "add", blocked, "external:old:cap"); code != 0 {
		t.Fatalf("seeding the external dependency failed with exit %d", code)
	}
	external := func(repo string) string {
		return fmt.Sprintf("external_projects:\n  old: %q\n", repo)
	}
	routedDefault := func(repo string) string {
		return fmt.Sprintf("routing:\n  default: %q\n", repo)
	}
	entryPoints := []struct {
		name   string
		config func(repo string) string
		routes func(repo string) string
		args   []string
		// skipWarning, when set, marks a skip: exit 0 and this warning.
		skipWarning string
	}{
		{name: "ready (external dependency)", config: external, args: []string{"ready"},
			skipWarning: `external project "old" is unavailable`},
		{name: "blocked (external dependency)", config: external, args: []string{"blocked"},
			skipWarning: `external project "old" is unavailable`},
		{name: "list (auto-routed read)", config: routedDefault, args: []string{"list"}},
		{name: "update (auto-routed write)", config: routedDefault, args: []string{"update", "fp-zzz", "--status", "in_progress"}},
		{name: "show (prefix-routed read)", args: []string{"show", "hr-abc"},
			routes: func(repo string) string {
				rel, err := filepath.Rel(dir, repo)
				if err != nil {
					t.Fatal(err)
				}
				return fmt.Sprintf(`{"prefix":"hr-","path":%q}`+"\n", rel)
			}},
	}
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			repo := t.TempDir()
			initGitRepoAt(t, repo)
			repoBeadsDir := filepath.Join(repo, ".beads")
			legacyworkspace.PreDoltClone().Write(t, repoBeadsDir)
			before := legacyworkspace.TreeDigest(t, repoBeadsDir)
			if ep.config != nil {
				if err := os.WriteFile(localConfig, []byte(ep.config(repo)), 0o600); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Remove(localConfig) }()
			}
			if ep.routes != nil {
				routesFile := filepath.Join(beadsDir, "routes.jsonl")
				if err := os.WriteFile(routesFile, []byte(ep.routes(repo)), 0o600); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Remove(routesFile) }()
			}
			stdout, stderr, code := local.run(t, ep.args...)
			if after := legacyworkspace.TreeDigest(t, repoBeadsDir); !maps.Equal(after, before) {
				t.Fatalf("bd %s changed the clone: before %v, after %v\nstdout:\n%s\nstderr:\n%s",
					strings.Join(ep.args, " "), slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)), stdout, stderr)
			}
			if ep.skipWarning != "" && (code != 0 || !strings.Contains(stderr, ep.skipWarning)) {
				t.Fatalf("bd %s exit %d, want a skip with %q:\nstdout:\n%s\nstderr:\n%s", strings.Join(ep.args, " "), code, ep.skipWarning, stdout, stderr)
			}
		})
	}
}

// TestEmbeddedCreateRepoRefusesRemovedBackendAsTheOpenDoes pins the wording of
// bd create --repo into a target whose metadata.json names a removed or
// unrecognized backend: the target store's open refuses it, as on a target
// with no other problem ("failed to open target store: ..."), not the
// create-target checks that run before it, which would call it a failure to
// initialize a repository that already has metadata. The target is left
// byte-identical.
func TestEmbeddedCreateRepoRefusesRemovedBackendAsTheOpenDoes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rb")
	local := crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)}
	for _, backend := range []string{"postgres", "frobnitz"} {
		t.Run(backend, func(t *testing.T) {
			repo := t.TempDir()
			repoBeadsDir := filepath.Join(repo, ".beads")
			legacyworkspace.Layout{Files: map[string]string{
				"metadata.json": fmt.Sprintf(`{"backend":%q,"database":"beads"}`, backend),
			}}.Write(t, repoBeadsDir)
			before := legacyworkspace.TreeDigest(t, repoBeadsDir)
			stdout, stderr, code := local.run(t, "create", "routed", "--repo", repo)
			if after := legacyworkspace.TreeDigest(t, repoBeadsDir); !maps.Equal(after, before) {
				t.Fatalf("bd create changed the target: before %v, after %v", before, after)
			}
			want := fmt.Sprintf("Error: failed to open target store: storage backend %q", backend)
			if code == 0 || !strings.Contains(stderr, want) || strings.Contains(stderr, "failed to initialize target repo") {
				t.Fatalf("bd create --repo exit %d, want %q:\nstdout:\n%s\nstderr:\n%s", code, want, stdout, stderr)
			}
		})
	}
}

// TestEmbeddedCreateTargetRefusesLegacyConfigItWouldHide pins, at each entry
// point that initializes a metadata-less target .beads (bd create --repo, its
// dry-run, an auto-routed create, and migrate-personal's planning repo), that
// a target holding a legacy config.json that bd run inside it rejects is
// refused with the error bd prints there, and is left byte-identical: one
// whose config.json does not load (malformed, not a JSON object, or
// unreadable), with "loading config: ...", and a fresh clone of a SQLite-era
// project (config.json and issues.jsonl, no database), with "no beads
// database found". So is a fresh clone of a project from before bd wrote
// config.json (issues.jsonl, no database), whose refusal names
// bd init --from-jsonl. Without those checks the create made an embedded
// database and a metadata.json there, after which bd inside the target opened
// the new database instead of refusing it or finding no workspace (hiding the
// clone's issues.jsonl behind it: bd's upgrade auto-import loads it only into
// an empty database), and the dry-run previewed a create that bd inside the
// target refuses.
func TestEmbeddedCreateTargetRefusesLegacyConfigItWouldHide(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ul")
	local := crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)}
	localConfig := filepath.Join(beadsDir, "config.local.yaml")
	type fixture struct {
		layout     legacyworkspace.Layout
		unreadable bool
		// want is the part of the error bd run inside the target prints that
		// names the cause.
		want string
		// remedy, when set, is the part of the refusal that names the way
		// forward, with <repo> standing for the target.
		remedy string
	}
	fixtures := []fixture{
		{layout: legacyworkspace.PreDoltClone(), want: ErrNoBeadsDatabase.Error()},
		{layout: legacyworkspace.PreConfigJSONClone(), want: ErrNoBeadsDatabase.Error(), remedy: "Run `bd init --from-jsonl` in <repo>, which imports it, then rerun"},
	}
	for _, layout := range legacyworkspace.UnloadableLegacyConfigLayouts() {
		fixtures = append(fixtures, fixture{layout: layout, want: "loading config: parsing config: "})
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		fixtures = append(fixtures, fixture{
			layout:     legacyworkspace.Layout{Name: "unreadable config.json", Files: map[string]string{"config.json": `{"database":"beads.db"}`}},
			unreadable: true,
			want:       "loading config: reading legacy config: ",
		})
	}
	initTarget := func(string) string { return "Error: failed to initialize target repo: " }
	entryPoints := []struct {
		name   string
		config func(repo string) string
		args   func(repo string) []string
		// prefix is the entry point's context ahead of admission's error, as
		// main prints it.
		prefix func(repo string) string
		// personal seeds a personal issue first: migrate-personal reads the
		// planning repo only when there is one to move.
		personal bool
	}{
		{name: "create --repo", prefix: initTarget,
			args: func(repo string) []string { return []string{"create", "routed", "--repo", repo} }},
		{name: "create --dry-run --repo", prefix: initTarget,
			args: func(repo string) []string { return []string{"create", "routed", "--dry-run", "--repo", repo} }},
		{name: "create (auto-routed)", prefix: initTarget,
			config: func(repo string) string { return fmt.Sprintf("routing:\n  default: %q\n", repo) },
			args:   func(string) []string { return []string{"create", "routed"} }},
		{name: "migrate-personal (planning repo)",
			config: func(repo string) string {
				return fmt.Sprintf("routing:\n  mode: auto\n  contributor: %q\n", repo)
			},
			args:     func(string) []string { return []string{"migrate-personal", "--yes"} },
			prefix:   func(repo string) string { return "Error: failed to open planning store at " + repo + ": " },
			personal: true},
	}
	for _, fx := range fixtures {
		for _, ep := range entryPoints {
			t.Run(fx.layout.Name+"/"+ep.name, func(t *testing.T) {
				repo := t.TempDir()
				initGitRepoAt(t, repo)
				repoBeadsDir := filepath.Join(repo, ".beads")
				fx.layout.Write(t, repoBeadsDir)
				before := legacyworkspace.TreeDigest(t, repoBeadsDir)
				configPath := filepath.Join(repoBeadsDir, "config.json")
				if fx.unreadable {
					if err := os.Chmod(configPath, 0); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = os.Chmod(configPath, 0o600) }()
				}
				inside := crossModeEnv{mode: "embedded", bd: bd, dir: repo, env: bdEnv(repo)}
				if _, stderr, code := inside.run(t, "list"); code == 0 || !strings.Contains(stderr, fx.want) {
					t.Fatalf("bd list inside the target did not refuse it with %q (exit %d):\n%s", fx.want, code, stderr)
				}
				if ep.config != nil {
					if err := os.WriteFile(localConfig, []byte(ep.config(repo)), 0o600); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = os.Remove(localConfig) }()
				}

				if ep.personal {
					local.create(t, "Personal")
				}
				stdout, stderr, code := local.run(t, ep.args(repo)...)
				remedy := strings.ReplaceAll(fx.remedy, "<repo>", repo)
				if want := ep.prefix(repo) + fx.want; code == 0 || !strings.Contains(stderr, want) || !strings.Contains(stderr, remedy) {
					t.Fatalf("bd %s exit %d, want the refusal %q naming %q\nstdout:\n%s\nstderr:\n%s",
						strings.Join(ep.args(repo), " "), code, want, remedy, stdout, stderr)
				}
				if _, stderr, code := inside.run(t, "list"); code == 0 || !strings.Contains(stderr, fx.want) {
					t.Fatalf("after bd %s, bd list inside the target no longer refuses it (exit %d):\n%s",
						strings.Join(ep.args(repo), " "), code, stderr)
				}
				if fx.unreadable {
					if err := os.Chmod(configPath, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if after := legacyworkspace.TreeDigest(t, repoBeadsDir); !maps.Equal(after, before) {
					t.Fatalf("bd %s changed the target: before %v, after %v\nstdout:\n%s\nstderr:\n%s",
						strings.Join(ep.args(repo), " "), slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)), stdout, stderr)
				}
			})
		}
	}
	// The way forward the issues.jsonl-only clone's refusal names keeps its
	// issues: bd init --from-jsonl there imports issues.jsonl, and the rerun
	// create then writes beside them.
	t.Run(legacyworkspace.PreConfigJSONClone().Name+"/remedy", func(t *testing.T) {
		repo := t.TempDir()
		initGitRepoAt(t, repo)
		legacyworkspace.PreConfigJSONClone().Write(t, filepath.Join(repo, ".beads"))
		inside := crossModeEnv{mode: "embedded", bd: bd, dir: repo, env: bdEnv(repo)}
		if stdout, stderr, code := inside.run(t, "init", "--from-jsonl", "--prefix", "old", "--quiet", "--non-interactive"); code != 0 {
			t.Fatalf("bd init --from-jsonl inside the clone exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		routed := local.create(t, "Routed after the remedy", "--repo", repo)
		ids := listIssueIDs(bdListJSON(t, bd, repo, "--all"))
		if !slices.Contains(ids, "old-1") || !slices.Contains(ids, routed) {
			t.Fatalf("bd list inside the clone after the remedy and a rerun = %v, want old-1 and %s", ids, routed)
		}
	})
}
