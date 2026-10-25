package main

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// isolateForeignWorkspaceSignals clears the environment and process config
// that select a Dolt server, and binds the process config to a launcher
// workspace whose config.yaml is launcherYAML ("" for none), so a test
// classifies a foreign workspace against a known launcher.
func isolateForeignWorkspaceSignals(t *testing.T, launcherYAML string) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_DATA_DIR", "BEADS_DOLT_SERVER_DATABASE",
	} {
		t.Setenv(name, "")
	}
	// A regression that reached an open must fail fast, not start a server.
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("HOME", t.TempDir())
	launcher := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(launcher, 0o750); err != nil {
		t.Fatal(err)
	}
	if launcherYAML != "" {
		if err := os.WriteFile(filepath.Join(launcher, "config.yaml"), []byte(launcherYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DIR", launcher)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
}

// foreignOpenAccesses lists every access openForeignStore opens with.
var foreignOpenAccesses = map[string]foreignAccess{
	"read":    foreignRead,
	"preview": foreignPreview,
	"write":   foreignWrite,
	"create":  foreignCreate,
}

// TestAdmitForeignWorkspaceRefusesLegacyDoltAsAdmissionDoes pins the helper
// every foreign open runs (admitForeignWorkspace), and the opener that runs
// it (openForeignStore) for every access: a legacy Dolt workspace is refused
// with the text bd prints when run inside it, whatever dolt.mode the
// launching workspace's config selects, and is left byte-identical. Before, the
// routed and external opens reached it unchecked: a server-mode one
// auto-started a Dolt server inside it and a routed write ran bd's schema
// migrations into its database, and an embedded create made a database beside
// its root.
func TestAdmitForeignWorkspaceRefusesLegacyDoltAsAdmissionDoes(t *testing.T) {
	launchers := map[string]string{
		"embedded launcher": "",
		"server launcher":   "dolt:\n  mode: server\n",
	}
	for launcherName, launcherYAML := range launchers {
		for _, layout := range legacyworkspace.LegacyDoltLayouts() {
			t.Run(launcherName+"/"+layout.Name, func(t *testing.T) {
				isolateForeignWorkspaceSignals(t, launcherYAML)
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				layout.Write(t, beadsDir)
				before := legacyworkspace.TreeDigest(t, beadsDir)
				want := legacyworkspace.LegacyDoltRefusal(layout.Reason)

				_, err := admitForeignWorkspace(beadsDir)
				var legacyErr *configfile.LegacyWorkspaceError
				if !errors.As(err, &legacyErr) || err.Error() != want {
					t.Fatalf("admitForeignWorkspace = %v, want %q", err, want)
				}
				for name, access := range foreignOpenAccesses {
					store, err := openForeignStore(t.Context(), beadsDir, access)
					if err == nil {
						_ = store.Close()
					}
					if err == nil || err.Error() != want {
						t.Fatalf("openForeignStore(%s) = %v, want %q", name, err, want)
					}
				}
				if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
					t.Fatalf("the legacy workspace changed: before %v, after %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
				}
			})
		}
	}
}

// TestOpenForeignStoreLeavesPreDoltCloneUnchanged pins that no open of an
// existing foreign workspace rewrites a fresh clone of a SQLite-era project
// (config.json and issues.jsonl, no *.db): bd run inside it finds no
// workspace and writes nothing, so the open refuses it with that error. The
// non-mutating factories under it read its config.json in place on every arm,
// the server arm included, so it stays byte-identical even past the opener.
// Before, the read-only and preview factories migrated config.json into a
// metadata.json that dropped its version and jsonl_export. A create or
// planning target is initialized before it is opened, so the create access
// is pinned at that initialization (ensureBeadsDirForPath, the dry-run's
// checkRepoTargetInitializable, and migrate-personal's
// refuseUninitializablePlanningRepo): each refuses the clone with the same
// error. Before, bd create --repo and bd migrate-personal wrote an embedded
// database and a metadata.json there, hiding its issues.jsonl from bd run
// inside it behind an empty database.
func TestOpenForeignStoreLeavesPreDoltCloneUnchanged(t *testing.T) {
	isolateForeignWorkspaceSignals(t, "")
	layout := legacyworkspace.PreDoltClone()
	for _, name := range []string{"read", "preview", "write"} {
		t.Run("openForeignStore/"+name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			layout.Write(t, beadsDir)
			before := legacyworkspace.TreeDigest(t, beadsDir)
			store, err := openForeignStore(t.Context(), beadsDir, foreignOpenAccesses[name])
			if err == nil {
				_ = store.Close()
			}
			if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
				t.Fatalf("openForeignStore(%s) changed the clone: before %v, after %v", name, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
			}
			if !errors.Is(err, ErrNoBeadsDatabase) {
				t.Fatalf("openForeignStore(%s) = %v, want %v", name, err, ErrNoBeadsDatabase)
			}
		})
	}
	// A nil source store initializes nothing past the refusals, as in
	// TestEnsureBeadsDirForPathRefusesTargetsItWouldHide; the error is what
	// shows the clone was refused rather than initialized.
	initializers := map[string]func(*testing.T, string) error{
		"create": func(t *testing.T, beadsDir string) error {
			return ensureBeadsDirForPath(t.Context(), filepath.Dir(beadsDir), nil, true)
		},
		"create --dry-run": func(_ *testing.T, beadsDir string) error {
			_, err := checkRepoTargetInitializable(filepath.Dir(beadsDir), true)
			return err
		},
		"migrate-personal": func(_ *testing.T, beadsDir string) error {
			return refuseUninitializablePlanningRepo(beadsDir)
		},
	}
	for name, initialize := range initializers {
		t.Run("initialize/"+name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			layout.Write(t, beadsDir)
			before := legacyworkspace.TreeDigest(t, beadsDir)
			err := initialize(t, beadsDir)
			if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
				t.Fatalf("%s changed the clone: before %v, after %v", name, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
			}
			want := ErrNoBeadsDatabase.Error() + " at " + beadsDir + ": "
			if !errors.Is(err, ErrNoBeadsDatabase) || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("%s = %v, want the error openForeignStore returns (%q), with its reason", name, err, want)
			}
		})
	}
	nonMutating := map[string]func(*testing.T, string) error{
		"read-only": func(t *testing.T, beadsDir string) error {
			store, err := newReadOnlyStoreFromConfig(t.Context(), beadsDir)
			if err == nil {
				_ = store.Close()
			}
			return err
		},
		"preview": func(t *testing.T, beadsDir string) error {
			store, err := newPreviewStoreFromConfig(t.Context(), beadsDir)
			if err == nil {
				_ = store.Close()
			}
			return err
		},
	}
	for _, serverArm := range []bool{false, true} {
		for name, open := range nonMutating {
			arm := "embedded arm"
			if serverArm {
				arm = "server arm"
			}
			t.Run(name+"/"+arm, func(t *testing.T) {
				if serverArm {
					// Nothing listens there, and auto-start is off.
					t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
					t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
					t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
				}
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				layout.Write(t, beadsDir)
				before := legacyworkspace.TreeDigest(t, beadsDir)
				_ = open(t, beadsDir)
				if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
					t.Fatalf("%s factory changed the clone: before %v, after %v", name, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
				}
			})
		}
	}
}

// TestTargetSelectsServerMode pins that a foreign workspace's server intent
// is read from its own metadata and config.yaml, in IsDoltServerMode's
// precedence, and never from the launching workspace's config.yaml. It also
// binds targetSelectsServerMode, a copy of that precedence, to the resolver it
// copies: on every fixture, IsDoltServerMode with the configuration bd run
// inside the target loads (config.WithBeadsDir, workspaceSelectsServerMode)
// answers the same, so a change to IsDoltServerMode or HostImpliesServerMode
// that the copy does not carry fails here.
func TestTargetSelectsServerMode(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		yaml     string
		local    string
		env      map[string]string
		want     bool
	}{
		{name: "nothing", want: false},
		{name: "metadata server", metadata: `{"backend":"dolt","dolt_mode":"server"}`, want: true},
		{name: "metadata embedded over config.yaml server", metadata: `{"backend":"dolt","dolt_mode":"embedded"}`, yaml: "dolt:\n  mode: server\n", want: false},
		{name: "config.yaml server", metadata: `{"backend":"dolt"}`, yaml: "dolt:\n  mode: server\n", want: true},
		{name: "config.yaml server without metadata", yaml: "dolt:\n  mode: server\n", want: true},
		{name: "config.local.yaml embedded over config.yaml server", yaml: "dolt:\n  mode: server\n", local: "dolt:\n  mode: embedded\n", want: false},
		{name: "config.yaml remote host", yaml: "dolt:\n  host: db.example.com\n", want: true},
		{name: "config.yaml local host", yaml: "dolt:\n  host: 127.0.0.1\n", want: false},
		{name: "config.yaml mode embedded beside remote host", yaml: "dolt:\n  mode: embedded\n  host: db.example.com\n", want: false},
		{name: "metadata remote host", metadata: `{"backend":"dolt","dolt_server_host":"db.example.com"}`, want: true},
		{name: "proxied server", metadata: `{"backend":"dolt","dolt_mode":"proxied-server"}`, want: false},
		{name: "env server mode", env: map[string]string{"BEADS_DOLT_SERVER_MODE": "1"}, want: true},
		{name: "env remote host", env: map[string]string{"BEADS_DOLT_SERVER_HOST": "db.example.com"}, want: true},
		{name: "env local host over config.yaml remote host", yaml: "dolt:\n  host: db.example.com\n", env: map[string]string{"BEADS_DOLT_SERVER_HOST": "127.0.0.1"}, want: false},
		{name: "unreadable config.yaml", yaml: "dolt: [unterminated\n", want: false},
		{name: "http backend", metadata: `{"backend":"http"}`, yaml: "dolt:\n  mode: server\n", want: false},
	}
	for _, launcherYAML := range []string{"", "dolt:\n  mode: server\n"} {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				isolateForeignWorkspaceSignals(t, launcherYAML)
				for name, value := range tc.env {
					t.Setenv(name, value)
				}
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				if err := os.MkdirAll(beadsDir, 0o750); err != nil {
					t.Fatal(err)
				}
				for name, content := range map[string]string{"metadata.json": tc.metadata, "config.yaml": tc.yaml, "config.local.yaml": tc.local} {
					if content == "" {
						continue
					}
					if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				cfg, err := configfile.LoadForDiscovery(beadsDir)
				if err != nil {
					t.Fatal(err)
				}
				got := targetSelectsServerMode(beadsDir, cfg)
				if got != tc.want {
					t.Fatalf("targetSelectsServerMode = %v, want %v (launcher config.yaml %q)", got, tc.want, launcherYAML)
				}
				// The layers the copy omits (the user-level config.yaml files
				// and viper's BD_ environment) are empty here: HOME is a fresh
				// directory, and the BD_ values set empty read as unset. A
				// config.yaml Initialize cannot load leaves nothing loaded,
				// which selects nothing, as the copy reads it.
				t.Setenv("XDG_CONFIG_HOME", "")
				t.Setenv("BD_DOLT_MODE", "")
				t.Setenv("BD_DOLT_HOST", "")
				var inside bool
				_ = config.WithBeadsDir(beadsDir, func() { inside = workspaceSelectsServerMode(cfg) })
				if inside != got {
					t.Fatalf("targetSelectsServerMode = %v, but IsDoltServerMode with the target's own config loaded = %v", got, inside)
				}
			})
		}
	}
}

// TestRefuseExistingCreateTargetRefusesEmbeddedOpenOfServerWorkspace pins the
// create-target refusal of a workspace whose own config.yaml selects server
// mode, where its metadata names no dolt_mode, when this process would open
// it embedded: the open takes dolt.mode from the launcher, so it would create
// an embedded database beside the server data bd run there reads. A launcher
// that opens it through a server, and a target whose metadata names its mode,
// pass; a removed backend is left to the open's own refusal.
func TestRefuseExistingCreateTargetRefusesEmbeddedOpenOfServerWorkspace(t *testing.T) {
	write := func(t *testing.T, files map[string]string) string {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		legacyworkspace.Layout{Files: files}.Write(t, beadsDir)
		return beadsDir
	}
	serverYAML := "dolt:\n  mode: server\n"
	t.Run("embedded launcher", func(t *testing.T) {
		isolateForeignWorkspaceSignals(t, "")
		beadsDir := write(t, map[string]string{"metadata.json": `{"backend":"dolt","dolt_database":"beads"}`, "config.yaml": serverYAML})
		before := legacyworkspace.TreeDigest(t, beadsDir)
		var refusal *existingTargetRefusal
		if err := refuseExistingCreateTarget(beadsDir); !errors.As(err, &refusal) || !strings.Contains(err.Error(), "its own config selects Dolt server mode") {
			t.Fatalf("refuseExistingCreateTarget = %v, want the server-mode refusal", err)
		}
		if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
			t.Fatalf("refuseExistingCreateTarget changed the target")
		}
	})
	t.Run("server launcher", func(t *testing.T) {
		isolateForeignWorkspaceSignals(t, serverYAML)
		beadsDir := write(t, map[string]string{"metadata.json": `{"backend":"dolt","dolt_database":"beads"}`, "config.yaml": serverYAML})
		if err := refuseExistingCreateTarget(beadsDir); err != nil {
			t.Fatalf("refuseExistingCreateTarget = %v, want nil", err)
		}
	})
	t.Run("metadata names the mode", func(t *testing.T) {
		isolateForeignWorkspaceSignals(t, "")
		beadsDir := write(t, map[string]string{"metadata.json": `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"beads"}`, "config.yaml": serverYAML})
		if err := refuseExistingCreateTarget(beadsDir); err != nil {
			t.Fatalf("refuseExistingCreateTarget = %v, want nil", err)
		}
	})
	t.Run("removed backend", func(t *testing.T) {
		isolateForeignWorkspaceSignals(t, "")
		beadsDir := write(t, map[string]string{"metadata.json": `{"backend":"postgres","database":"beads"}`})
		if err := refuseExistingCreateTarget(beadsDir); err != nil {
			t.Fatalf("refuseExistingCreateTarget = %v, want nil (the open refuses it)", err)
		}
	})
}

// TestDoctorMultiRepoTypesRefusesLegacyDoltRepo pins doctor's Multi-Repo
// Types check, the entry point that reads a repos.additional repository's
// database, against the legacy Dolt layouts: bd's foreign-workspace admission
// (doctor.AdmitAdditionalRepo) refuses the repo before its database is
// opened, the check says so beside its config.yaml types, and the repo is
// left byte-identical. Before, the open auto-started a Dolt server in a legacy
// server workspace.
func TestDoctorMultiRepoTypesRefusesLegacyDoltRepo(t *testing.T) {
	if doctor.AdmitAdditionalRepo == nil {
		t.Fatal("bd does not set doctor.AdmitAdditionalRepo")
	}
	for _, layout := range legacyworkspace.LegacyDoltLayouts() {
		t.Run(layout.Name, func(t *testing.T) {
			isolateForeignWorkspaceSignals(t, "")
			// If the refusal were missing, the open must fail fast.
			t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
			t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
			repo := t.TempDir()
			beadsDir := filepath.Join(repo, ".beads")
			layout.Write(t, beadsDir)
			yaml := "types:\n  custom: [legacy-node]\n"
			if existing, ok := layout.Files["config.yaml"]; ok {
				yaml = existing + yaml
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			before := legacyworkspace.TreeDigest(t, beadsDir)
			config.Set("repos.primary", t.TempDir())
			config.Set("repos.additional", []string{repo})

			check := doctor.CheckMultiRepoTypes(t.TempDir())
			if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
				t.Fatalf("CheckMultiRepoTypes changed the additional repo: before %v, after %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
			}
			want := strings.Join([]string{
				"  " + repo + ": legacy-node",
				"    database not read, so only config.yaml types are listed: " + legacyworkspace.LegacyDoltRefusal(layout.Reason),
			}, "\n")
			if check.Detail != want {
				t.Fatalf("CheckMultiRepoTypes detail:\n%s\nwant:\n%s", check.Detail, want)
			}
		})
	}
}

// metadataLessTargetFixture is one metadata-less .beads a create or planning
// target can hold: its layout, whether its config.json is made unreadable
// after it is written, whether that config.json does not load, which
// admission refuses with "loading config: ...", and whether admission refuses
// it as a legacy Dolt workspace.
type metadataLessTargetFixture struct {
	layout     legacyworkspace.Layout
	unreadable bool
	unloadable bool
	legacyDolt bool
}

// metadataLessTargetFixtures lists the metadata-less .beads layouts command
// admission refuses or admits from their contents: the historical SQLite
// layouts that keep their pointer in config.json or none, the pre-Dolt
// clone, a legacy Dolt root, and the config.json layouts that do not load,
// including one bd cannot read (skipped where a permission change does not
// stop a read).
func metadataLessTargetFixtures(t *testing.T) []metadataLessTargetFixture {
	t.Helper()
	var fixtures []metadataLessTargetFixture
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		if _, ok := layout.Files["metadata.json"]; !ok {
			fixtures = append(fixtures, metadataLessTargetFixture{layout: layout})
		}
	}
	fixtures = append(fixtures, metadataLessTargetFixture{layout: legacyworkspace.PreDoltClone()})
	// A non-empty .beads/dolt root with no metadata.json, which admission
	// refuses as a legacy Dolt workspace. Every LegacyDoltLayouts entry has a
	// metadata.json, so this is the one fixture that reaches the
	// admitForeignWorkspace call in refuseUninitializableTarget, where nothing
	// else refuses it.
	fixtures = append(fixtures, metadataLessTargetFixture{layout: legacyworkspace.Layout{
		Name:  "metadata-less legacy Dolt",
		Files: map[string]string{"dolt/beads/.dolt/noms/manifest": "legacy manifest"},
	}, legacyDolt: true})
	for _, layout := range legacyworkspace.UnloadableLegacyConfigLayouts() {
		fixtures = append(fixtures, metadataLessTargetFixture{layout: layout, unloadable: true})
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		fixtures = append(fixtures, metadataLessTargetFixture{
			layout: legacyworkspace.Layout{Name: "unreadable config.json", Files: map[string]string{
				"config.json": `{"database":"beads.db"}`,
			}},
			unreadable: true,
			unloadable: true,
		})
	}
	return fixtures
}

// write lays the fixture out at beadsDir. Its tree digest is taken while
// config.json can still be read; restore makes it readable again.
func (f metadataLessTargetFixture) write(t *testing.T, beadsDir string) (digest map[string]string, restore func()) {
	t.Helper()
	f.layout.Write(t, beadsDir)
	digest = legacyworkspace.TreeDigest(t, beadsDir)
	restore = func() {}
	if f.unreadable {
		configPath := filepath.Join(beadsDir, "config.json")
		if err := os.Chmod(configPath, 0); err != nil {
			t.Fatal(err)
		}
		restore = func() { _ = os.Chmod(configPath, 0o600) }
		t.Cleanup(restore)
	}
	return digest, restore
}

// TestRefuseUninitializableTargetRefusesWhatAdmissionRefuses pins, over one
// shared set of metadata-less layouts, that the check a routed create and
// migrate-personal run before initializing a target
// (refuseUninitializableTarget, and refuseUninitializablePlanningRepo around
// it) refuses every layout command admission (guardLegacyUpgradeWorkspace)
// refuses, with admission's text, and writes nothing. Those callers write a
// metadata.json and a database before any configfile.Load could report the
// layout. In particular a legacy config.json that does not load (malformed,
// not an object, unreadable) is refused with admission's "loading config:"
// error; admitForeignWorkspace alone admits it, leaving it to a Load.
func TestRefuseUninitializableTargetRefusesWhatAdmissionRefuses(t *testing.T) {
	isolateForeignWorkspaceSignals(t, "")
	for _, fixture := range metadataLessTargetFixtures(t) {
		t.Run(fixture.layout.Name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			before, restore := fixture.write(t, beadsDir)
			admission := guardLegacyUpgradeWorkspace(beadsDir)
			for name, check := range map[string]func(string) error{
				"refuseUninitializableTarget":       refuseUninitializableTarget,
				"refuseUninitializablePlanningRepo": refuseUninitializablePlanningRepo,
			} {
				err := check(beadsDir)
				if admission == nil {
					continue
				}
				if err == nil || !strings.Contains(err.Error(), admission.Error()) {
					t.Errorf("%s = %v, want admission's refusal %q", name, err, admission)
				}
			}
			if fixture.unloadable && (admission == nil || !strings.HasPrefix(admission.Error(), "loading config: ")) {
				t.Fatalf("guardLegacyUpgradeWorkspace = %v, want a \"loading config:\" refusal", admission)
			}
			if want := legacyworkspace.LegacyDoltRefusal("legacy Dolt workspace"); fixture.legacyDolt && (admission == nil || admission.Error() != want) {
				t.Fatalf("guardLegacyUpgradeWorkspace = %v, want %q", admission, want)
			}
			restore()
			if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
				t.Fatalf("the target changed: before %v, after %v", before, after)
			}
		})
	}
}
