// Package legacyworkspace builds, for tests, the historical SQLite and legacy
// Dolt .beads layouts command admission refuses, and the pre-Dolt clones it
// does not treat as a workspace at all, and digests a directory tree so a test can
// assert that an entry point which refused or skipped one of them left it
// byte-identical.
//
// Every entry point that opens or inspects a workspace bd was not launched in
// (store factories, library opens, the Dolt constructors, the http backend's
// attach, routed and external reads) is expected to refuse these layouts with
// HistoricalSQLiteRefusal, the text admission prints for them, before it
// writes anything. Sharing the table keeps those tests on the same layouts.
//
// It must not import configfile: configfile's own tests use it.
package legacyworkspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// HistoricalSQLiteRefusal is the refusal command admission prints for every
// layout in HistoricalSQLiteLayouts.
const HistoricalSQLiteRefusal = "historical SQLite workspace detected; explicit migration is required before this bd version can open or modify the workspace. Preserve .beads unchanged and follow docs/getting-started/upgrading.md#cross-era-upgrades"

// Layout is one historical SQLite .beads layout.
type Layout struct {
	// Name labels the layout in subtest names.
	Name string
	// Files maps each file beneath .beads to its content.
	Files map[string]string
}

// sqliteDatabase is a SQLite database as far as any classifier reads one: a
// non-empty regular file that starts with the SQLite header.
const sqliteDatabase = "SQLite format 3\x00" + "\x10\x00\x01\x01 legacy page"

// HistoricalSQLiteLayouts lists the historical SQLite layouts: a pre-Dolt
// config.json (no backend key) beside beads.db, the same in metadata.json, the
// removed sqlite backend named in either file beside its database, and a
// metadata-less .beads whose only database is a SQLite file (v0.9.1's vc.db).
// Each metadata file carries keys configfile.Config has no field for, which a
// migrating load would drop.
func HistoricalSQLiteLayouts() []Layout {
	return []Layout{
		{Name: "pre-Dolt config.json", Files: map[string]string{
			"config.json": `{"database":"beads.db","version":"0.20.1","jsonl_export":"issues.jsonl"}`,
			"beads.db":    sqliteDatabase,
		}},
		{Name: "SQLite metadata.json", Files: map[string]string{
			"metadata.json": `{"database":"beads.db","jsonl_export":"issues.jsonl"}`,
			"beads.db":      sqliteDatabase,
		}},
		{Name: "sqlite backend config.json", Files: map[string]string{
			"config.json": `{"backend":"sqlite","database":"beads.db","jsonl_export":"issues.jsonl"}`,
			"beads.db":    sqliteDatabase,
		}},
		{Name: "sqlite backend metadata.json", Files: map[string]string{
			"metadata.json": `{"backend":"sqlite","database":"beads.db","jsonl_export":"issues.jsonl"}`,
			"beads.db":      sqliteDatabase,
		}},
		{Name: "metadata-less vc.db", Files: map[string]string{
			"vc.db":        sqliteDatabase,
			"issues.jsonl": `{"id":"vc-1","title":"legacy"}` + "\n",
		}},
	}
}

// PreDoltClone is a fresh clone of a SQLite-era project: the config.json and
// issues.jsonl that era's .beads/.gitignore kept tracked, and no *.db, which
// it ignored. bd run inside it does not treat it as a workspace ("no beads
// database found") and writes nothing there, and the historical SQLite check
// does not refuse it either (it needs the database file). Every entry point
// that only reads a workspace bd was not launched in must leave it
// byte-identical: its config.json carries keys configfile.Config has no field
// for, which a migrating load would drop. So must every entry point that
// would initialize it as a create or planning target: a metadata.json and a
// database written there would make it a workspace that hides its files.
func PreDoltClone() Layout {
	return Layout{Name: "pre-Dolt clone", Files: map[string]string{
		"config.json":  `{"database":"beads.db","version":"0.20.1","jsonl_export":"issues.jsonl"}`,
		"issues.jsonl": `{"id":"old-1","title":"from the SQLite era"}` + "\n",
	}}
}

// PreConfigJSONClone is a fresh clone of a project bd set up before it wrote
// config.json (before v0.17.7): the issues.jsonl and .gitignore that era's
// .beads kept tracked, and no *.db, which it ignored. bd run inside it does
// not treat it as a workspace ("no beads database found") and writes nothing
// there. Every entry point that would initialize it as a create or planning
// target must leave it byte-identical: a database written there with the
// create's issue in it would make it a workspace whose issues.jsonl the
// upgrade auto-import, which imports only into an empty database, never
// loads.
func PreConfigJSONClone() Layout {
	return Layout{Name: "pre-config.json clone", Files: map[string]string{
		".gitignore":   "# SQLite databases\n*.db\n*.db-journal\n*.db-wal\n*.db-shm\n\n!*.jsonl\n",
		"issues.jsonl": `{"id":"old-1","title":"from before config.json"}` + "\n",
	}}
}

// UnloadableLegacyConfigLayouts lists metadata-less .beads layouts whose
// legacy config.json does not load: malformed JSON, and JSON that is not an
// object. Command admission refuses each with "loading config: parsing
// config: ...", so an entry point that would initialize such a .beads (write a
// metadata.json or a database there) must refuse it with that error first. A
// config.json bd cannot read at all is the third such layout; it needs a
// permission change, so tests build it from these files themselves.
func UnloadableLegacyConfigLayouts() []Layout {
	return []Layout{
		{Name: "malformed config.json", Files: map[string]string{
			"config.json": `{not json`,
		}},
		{Name: "non-object config.json", Files: map[string]string{
			"config.json": `[1,2,3]`,
		}},
	}
}

// LegacyDoltRefusal returns the refusal command admission prints for a
// legacy Dolt layout, reason naming it ("legacy Dolt workspace", "legacy
// Dolt server workspace from bd 0.62.0").
func LegacyDoltRefusal(reason string) string {
	return reason + " detected; explicit migration is required before this bd version can open or modify the workspace. Preserve .beads unchanged and follow docs/getting-started/upgrading.md#cross-era-upgrades"
}

// LegacyDoltLayout is a legacy Dolt .beads layout and the reason admission
// refuses it with (LegacyDoltRefusal).
type LegacyDoltLayout struct {
	Layout
	Reason string
}

// LegacyDoltLayouts lists legacy Dolt layouts command admission refuses when
// bd runs inside them, whatever the launching workspace's dolt.mode (a
// launcher whose merged config enables dolt.shared-server admits the first
// two, whose config.yaml sets no dolt.shared-server): a non-empty .beads/dolt
// root with no embedded repository and no current-era version witness, whose
// metadata names no dolt_mode; the same where the
// workspace's own config.yaml selects server mode; and a server workspace
// whose version witness names bd 0.62.0.
func LegacyDoltLayouts() []LegacyDoltLayout {
	manifest := "legacy manifest"
	return []LegacyDoltLayout{
		{Layout: Layout{Name: "legacy Dolt", Files: map[string]string{
			"metadata.json":                  `{"backend":"dolt","database":"dolt","dolt_database":"beads"}`,
			"dolt/beads/.dolt/noms/manifest": manifest,
		}}, Reason: "legacy Dolt workspace"},
		{Layout: Layout{Name: "legacy Dolt with config.yaml server mode", Files: map[string]string{
			"metadata.json":                  `{"backend":"dolt","database":"dolt","dolt_database":"beads"}`,
			"config.yaml":                    "dolt:\n  mode: server\n",
			"dolt/beads/.dolt/noms/manifest": manifest,
		}}, Reason: "legacy Dolt server workspace"},
		{Layout: Layout{Name: "legacy Dolt server from bd 0.62.0", Files: map[string]string{
			"metadata.json":                   `{"backend":"dolt","dolt_mode":"server","dolt_database":"herald"}`,
			".local_version":                  "0.62.0\n",
			"dolt/herald/.dolt/noms/manifest": manifest,
		}}, Reason: "legacy Dolt server workspace from bd 0.62.0"},
	}
}

// Write creates beadsDir (and its parents) holding the layout's files.
func (l Layout) Write(t testing.TB, beadsDir string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range l.Files {
		path := filepath.Join(beadsDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TreeDigest maps every path beneath root, relative to it, to its kind and,
// for a regular file, its content, so two digests are equal only when nothing
// was created, removed or rewritten beneath root.
func TreeDigest(t testing.TB, root string) map[string]string {
	t.Helper()
	digest := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			digest[rel] = "dir"
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			digest[rel] = "symlink:" + target
		default:
			content, err := os.ReadFile(path) // #nosec G304 -- test fixture tree
			if err != nil {
				return err
			}
			digest[rel] = "file:" + string(content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", root, err)
	}
	return digest
}
