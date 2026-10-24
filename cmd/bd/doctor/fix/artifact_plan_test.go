package fix

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestPlanArtifacts_DeletesOnlyWithPositiveOwnership pins the one predicate
// behind every Classic Artifacts deletion. For each layout it checks what
// classifyBeadsDir establishes about the .beads directory, which findings
// PlanArtifacts reports and which of them are safe to delete, and that
// ClassicArtifacts removes exactly the safe ones and leaves everything else
// byte-identical. Only a .beads positively on Dolt (an orphaned WAL or SHM)
// and a redirect-only stub holding no database state (its scaffolding) give
// up anything; a database, a backup, the WAL or SHM of a database still
// there, and everything in a directory whose backend doctor cannot establish
// are reported for review instead.
func TestPlanArtifacts_DeletesOnlyWithPositiveOwnership(t *testing.T) {
	const sqliteDB = "SQLite format 3\x00rest"
	const torn = "{\"database\":\"beads.db\",\n<<<<<<< HEAD\n"
	cases := []struct {
		name  string
		dir   string            // the .beads directory, relative to the walked root
		files map[string]string // its entries ("x/" is a directory)
		extra map[string]string // other paths under the root
		links map[string]string // symlinks under the root: link -> target, both relative to the root
		owner beadsDirOwner
		skip  bool
		// want maps each reported path, relative to the .beads directory
		// ("." for a directory-level cruft finding), to SafeDelete.
		want map[string]bool
	}{
		// Positive Dolt evidence: an orphaned WAL or SHM is removable.
		{name: "backend dolt", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`, "beads.db-wal": "w", "beads.db-shm": "s"}, owner: ownerDolt, want: map[string]bool{"beads.db-wal": true, "beads.db-shm": true}},
		{name: "dolt_mode only", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"dolt_mode":"server"}`, "beads.db-wal": "w"}, owner: ownerDolt, want: map[string]bool{"beads.db-wal": true}},
		{name: "dolt_database only", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"dolt_database":"x"}`, "beads.db-wal": "w"}, owner: ownerDolt, want: map[string]bool{"beads.db-wal": true}},
		{name: "embedded repository, no metadata", dir: "sub/.beads", files: map[string]string{"embeddeddolt/x/.dolt/noms/": "", "beads.db-wal": "w"}, owner: ownerDolt, want: map[string]bool{"beads.db-wal": true}},
		{name: "dolt store, no metadata", dir: "sub/.beads", files: map[string]string{"dolt/": "", "beads.db-wal": "w"}, owner: ownerDolt, want: map[string]bool{"beads.db-wal": true}},
		{name: "dolt_data_dir in legacy config.json", dir: "sub/.beads", files: map[string]string{"config.json": `{"dolt_data_dir":"custom"}`, "custom/": "", "beads.db-wal": "w"}, owner: ownerDolt, want: map[string]bool{"beads.db-wal": true}},
		// On Dolt a database, its WAL and SHM, and a backup stay.
		{name: "dolt beside beads.db", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`, "beads.db": sqliteDB, "beads.db-wal": "w", "beads.db-shm": "s", "beads.backup-20260204.db": "b"}, owner: ownerDolt, want: map[string]bool{"beads.db": false, "beads.db-wal": false, "beads.db-shm": false, "beads.backup-20260204.db": false}},
		// SQLite opens beads.db-wal and beads.db-shm by name beside beads.db,
		// so links there are its WAL and SHM wherever they point; without
		// beads.db the link is an orphaned sidecar, and its target stays.
		{name: "dolt beside beads.db, WAL and SHM linking to non-SQLite bytes", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`, "beads.db": sqliteDB}, extra: map[string]string{"elsewhere/wal": walHeader, "elsewhere/shm": "s"}, links: map[string]string{"sub/.beads/beads.db-wal": "elsewhere/wal", "sub/.beads/beads.db-shm": "elsewhere/shm"}, owner: ownerDolt, want: map[string]bool{"beads.db": false, "beads.db-wal": false, "beads.db-shm": false}},
		{name: "dolt, orphaned WAL linking to non-SQLite bytes", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`}, extra: map[string]string{"elsewhere/wal": walHeader}, links: map[string]string{"sub/.beads/beads.db-wal": "elsewhere/wal"}, owner: ownerDolt, want: map[string]bool{"beads.db-wal": true}},
		// No positive evidence: reported for review, nothing removed.
		{name: "torn metadata.json", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: torn, "beads.db": sqliteDB, "beads.db-wal": "w", "beads.db-shm": "s", "beads.backup-20260204.db": "b"}, owner: ownerUnreadable, want: map[string]bool{"beads.db": false, "beads.db-wal": false, "beads.db-shm": false, "beads.backup-20260204.db": false}},
		{name: "torn config.json", dir: "sub/.beads", files: map[string]string{"config.json": torn, "beads.db-wal": "w"}, owner: ownerUnreadable, want: map[string]bool{"beads.db-wal": false}},
		{name: "no metadata, WAL only", dir: "sub/.beads", files: map[string]string{"beads.db-wal": "w"}, owner: ownerUndetermined, want: map[string]bool{"beads.db-wal": false}},
		{name: "no metadata, ambiguous classic layout", dir: "sub/.beads", files: map[string]string{".gitignore": "*.db\n", "beads.db": sqliteDB, "beads.db-wal": "w", "beads.db-shm": "s"}, owner: ownerUndetermined, want: map[string]bool{"beads.db": false, "beads.db-wal": false, "beads.db-shm": false}},
		{name: "classic metadata without its database", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"database":"beads.db"}`, "beads.db-wal": "w"}, owner: ownerUndetermined, want: map[string]bool{"beads.db-wal": false}},
		// Another backend, or historical SQLite: neither reported nor removed.
		{name: "sqlite backend", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"sqlite"}`, "beads.db": sqliteDB, "beads.db-wal": "w"}, owner: ownerOtherBackend, want: map[string]bool{}},
		{name: "classic metadata", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: `{"database":"beads.db","jsonl_export":"issues.jsonl"}`, "beads.db": sqliteDB, "beads.db-wal": "w"}, owner: ownerOtherBackend, want: map[string]bool{}},
		{name: "metadata-less sole SQLite database", dir: "sub/.beads", files: map[string]string{"beads.db": sqliteDB, "beads.db-wal": "w", "beads.db-shm": "s"}, owner: ownerOtherBackend, want: map[string]bool{}},
		// Redirect-only locations: a stub's scaffolding goes, a workspace stays.
		{name: "polecat stub", dir: "polecats/p1/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "config.yaml": "c", "README.md": "r", "formulas/x.toml": "f"}, owner: ownerRedirectStub, want: map[string]bool{".": true}},
		// Without a redirect, metadata that configures Dolt, even with one
		// Dolt setting alone, is a workspace's own (a server-mode workspace
		// keeps no local store): only an orphaned WAL or SHM goes. Beside a
		// redirect it is a stub's, and stale pre-Dolt metadata configures
		// nothing.
		{name: "crew server-mode workspace, no redirect", dir: "crew/alice/.beads", files: map[string]string{configfile.ConfigFileName: `{"dolt_mode":"server","dolt_database":"rig"}`, "config.yaml": "c", "routes.jsonl": "{}", "beads.db-wal": "w"}, owner: ownerDolt, want: map[string]bool{".": false, "beads.db-wal": true}},
		{name: "polecat metadata naming only backend dolt, no redirect", dir: "polecats/p7/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`, "config.yaml": "c"}, owner: ownerDolt, want: map[string]bool{".": false}},
		{name: "crew server-mode workspace whose metadata sets only dolt_server_host, no redirect", dir: "crew/alice/.beads", files: map[string]string{configfile.ConfigFileName: `{"dolt_server_host":"db.example.com"}`, "config.yaml": "c"}, owner: ownerDolt, want: map[string]bool{".": false}},
		{name: "crew metadata naming only a missing dolt_data_dir, no redirect", dir: "crew/alice/.beads", files: map[string]string{configfile.ConfigFileName: `{"dolt_data_dir":"../../../store"}`, "config.yaml": "c"}, owner: ownerDolt, want: map[string]bool{".": false}},
		{name: "polecat stub beside its redirect, Dolt server metadata", dir: "polecats/p8/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server","dolt_database":"rig"}`, "config.yaml": "c", "README.md": "r"}, owner: ownerRedirectStub, want: map[string]bool{".": true}},
		{name: "beads worktree with pre-Dolt metadata, no redirect", dir: ".git/beads-worktrees/wt/.beads", files: map[string]string{configfile.ConfigFileName: `{"database":"beads.db","jsonl_export":"issues.jsonl"}`, "config.yaml": "c", "issues.jsonl": "{}"}, owner: ownerRedirectStub, want: map[string]bool{".": true}},
		{name: "crew classic SQLite workspace", dir: "crew/alice/.beads", files: map[string]string{configfile.ConfigFileName: `{"database":"beads.db","jsonl_export":"issues.jsonl"}`, "beads.db": sqliteDB, "beads.db-wal": "w", "beads.db-shm": "s"}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "polecat legacy config.json workspace", dir: "polecats/p1/.beads", files: map[string]string{"config.json": `{"database":"beads.db"}`, "beads.db": sqliteDB, "beads.db-wal": "w"}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "crew embedded Dolt workspace", dir: "crew/alice/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"embedded"}`, "embeddeddolt/al/.dolt/noms/": "", "config.yaml": "c"}, owner: ownerDolt, want: map[string]bool{".": false}},
		{name: "refinery server-mode store", dir: "refinery/rig/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server"}`, "dolt/rig/.dolt/": ""}, owner: ownerDolt, want: map[string]bool{".": false}},
		// The rig root mayor/rig/.beads redirects to is canonical, not a stub,
		// with or without a local store.
		{name: "rig root mayor/rig redirects to", dir: ".beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`, "embeddeddolt/rg/.dolt/noms/": ""}, extra: map[string]string{"mayor/rig/.beads/redirect": "../../.beads\n"}, owner: ownerDolt, want: map[string]bool{}},
		{name: "server-mode rig root mayor/rig redirects to", dir: "rig/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server","dolt_database":"my_rig"}`, "config.yaml": "c", "routes.jsonl": "{}"}, extra: map[string]string{"rig/mayor/rig/.beads/redirect": "# canonical\n../../.beads\n", "rig/polecats/": ""}, owner: ownerDolt, want: map[string]bool{}},
		{name: "rig root mayor/rig does not redirect to", dir: "rig/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server","dolt_database":"my_rig"}`, "config.yaml": "c"}, extra: map[string]string{"rig/mayor/rig/.beads/redirect": "../../../elsewhere/.beads\n", "rig/polecats/": ""}, owner: ownerDolt, want: map[string]bool{".": false}},
		// Positive evidence of another backend outranks the redirect-only
		// path, whatever the database file is named.
		{name: "crew sqlite_path not *.db", dir: "crew/alice/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"sqlite","sqlite_path":"issues.sqlite3"}`, "issues.sqlite3": sqliteDB}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "polecat sqlite_path in a subdirectory", dir: "polecats/p3/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"sqlite","sqlite_path":"data/tracker.sqlite3"}`, "data/tracker.sqlite3": sqliteDB}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "crew classic database not *.db", dir: "crew/bob/.beads", files: map[string]string{configfile.ConfigFileName: `{"database":"tracker.sqlite","jsonl_export":"issues.jsonl"}`, "tracker.sqlite": sqliteDB}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "crew postgres backend", dir: "crew/carol/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"postgres"}`, "config.yaml": "c"}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "crew http backend", dir: "crew/dave/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"http"}`, "config.yaml": "c"}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "refinery unknown backend", dir: "refinery/rig/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"future-x"}`, "store/x.bin": "opaque"}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		{name: "polecat stub beside its redirect naming sqlite", dir: "polecats/p1/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", configfile.ConfigFileName: `{"backend":"sqlite"}`, "README.md": "r"}, owner: ownerOtherBackend, want: map[string]bool{".": false}},
		// A stub holding a database under any name is not a stub.
		{name: "polecat stub holding a SQLite file not named *.db", dir: "polecats/p1/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "beads.db.bak": sqliteDB}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "polecat stub holding an upper-case .DB", dir: "polecats/p1/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "OLD.DB": "x"}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "polecat stub holding a sidecar-named directory with a database", dir: "polecats/p2/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "old.db-wal/inner.db": sqliteDB}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "polecat stub holding a SQLite file deep in a directory", dir: "polecats/p2/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "keep/copy.bin": sqliteDB}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "beads worktree holding bd backups", dir: ".git/beads-worktrees/wt/.beads", files: map[string]string{"backup/issues.jsonl": "{}", "config.yaml": "c"}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "polecat subdirectory holding a Dolt repository", dir: "polecats/p1/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "old/rig/.dolt/": ""}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "polecat with torn metadata", dir: "polecats/p1/.beads", files: map[string]string{configfile.ConfigFileName: torn, "config.yaml": "c"}, owner: ownerUnreadable, want: map[string]bool{".": false}},
		// A Dolt database in its raw chunk-store layout (a file:// Dolt backup
		// or remote: manifest, *.darc, LOCK, oldgen/, no .dolt) is database
		// state under any name.
		{name: "polecat stub holding a Dolt chunk-store backup", dir: "polecats/p1/.beads", files: mergeTree(map[string]string{"redirect": "../../mayor/rig/.beads\n"}, doltChunkStore("snap/")), owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "crew server-mode stub holding a Dolt chunk-store backup", dir: "crew/bob/.beads", files: mergeTree(map[string]string{configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server"}`}, doltChunkStore("dolt-backups/")), owner: ownerDolt, want: map[string]bool{".": false}},
		{name: "crew stub holding a header-less *.sqlite", dir: "crew/zed/.beads", files: map[string]string{"vault.sqlite": "\x8f\x12opaque"}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		// Links are followed to classify, never to delete.
		{name: "polecat stub linking to a Dolt store", dir: "polecats/p9/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n"}, extra: map[string]string{"x/data/db/.dolt/": ""}, links: map[string]string{"polecats/p9/.beads/data": "x/data"}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		{name: "polecat stub linking to scaffolding", dir: "polecats/p9/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n"}, extra: map[string]string{"x/formulas/a.toml": "f"}, links: map[string]string{"polecats/p9/.beads/formulas": "x/formulas"}, owner: ownerRedirectStub, want: map[string]bool{".": true}},
		// Metadata naming an existing local Dolt store points at a database.
		{name: "crew metadata naming an existing dolt_data_dir", dir: "crew/yan/.beads", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt","dolt_data_dir":"../../../store"}`, "config.yaml": "c"}, extra: map[string]string{"store/yan/.dolt/": ""}, owner: ownerDolt, want: map[string]bool{".": false}},
		{name: "crew stub beside its redirect, metadata naming a missing dolt_data_dir", dir: "crew/yan/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", configfile.ConfigFileName: `{"backend":"dolt","dolt_data_dir":"../../../store"}`, "config.yaml": "c"}, owner: ownerRedirectStub, want: map[string]bool{".": true}},
		// Lower-casing can lengthen a name in bytes (U+023A is 2 bytes,
		// its lower case U+2C65 is 3); the sidecar check must not slice the
		// original name by the lower-cased length.
		{name: "polecat stub holding an orphaned sidecar whose name lengthens when lower-cased", dir: "polecats/p4/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", strings.Repeat("\u023a", 5) + ".db-wal": "w"}, owner: ownerRedirectStub, want: map[string]bool{".": true}},
		{name: "polecat stub holding a journal beside its database, names lengthening when lower-cased", dir: "polecats/p4/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", strings.Repeat("\u023a", 9) + ".db": "x", strings.Repeat("\u023a", 9) + ".db-journal": "j"}, owner: ownerUndetermined, want: map[string]bool{".": false}},
		// A clean stub (redirect, .gitkeep and the metadata kept beside a
		// redirect) is not reported, whatever that metadata says (#4692).
		{name: "clean stub, metadata beside its redirect naming sqlite", dir: "polecats/p1/.beads", files: map[string]string{"redirect": "../../.beads\n", ".gitkeep": "", configfile.ConfigFileName: `{"backend":"sqlite"}`}, owner: ownerOtherBackend, want: map[string]bool{}},
		{name: "clean stub, torn metadata beside its redirect", dir: "polecats/p2/.beads", files: map[string]string{"redirect": "../../.beads\n", configfile.ConfigFileName: torn}, owner: ownerUnreadable, want: map[string]bool{}},
		{name: "clean stub, metadata beside its redirect naming an existing dolt_data_dir", dir: "polecats/p3/.beads", files: map[string]string{"redirect": "../../.beads\n", configfile.ConfigFileName: `{"backend":"dolt","dolt_data_dir":"../../../store"}`}, extra: map[string]string{"store/p3/.dolt/": ""}, owner: ownerDolt, want: map[string]bool{}},
		// A format this build cannot open: skipped, not even reported.
		{name: "graph format marker", dir: "sub/.beads", files: map[string]string{configfile.GraphFormatMarkerFile: "link-preview-v9\n", "beads.db-wal": "w"}, owner: ownerUnsupportedFormat, skip: true, want: map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, filepath.FromSlash(tc.dir))
			writeBeadsTree(t, beadsDir, tc.files)
			writeBeadsTree(t, root, tc.extra)
			for link, target := range tc.links {
				if runtime.GOOS == "windows" {
					t.Skip("symlinks need privileges on Windows")
				}
				if err := os.Symlink(filepath.Join(root, filepath.FromSlash(target)), filepath.Join(root, filepath.FromSlash(link))); err != nil {
					t.Fatal(err)
				}
			}
			outside := snapshotBeadsTree(t, root)

			if got := classifyBeadsDir(beadsDir).owner; got != tc.owner {
				t.Errorf("classifyBeadsDir owner = %d, want %d", got, tc.owner)
			}
			findings, skip := PlanArtifacts(beadsDir)
			if skip != tc.skip {
				t.Errorf("PlanArtifacts skip = %v, want %v", skip, tc.skip)
			}
			got := map[string]bool{}
			removable := map[string]bool{}
			for _, f := range findings {
				rel, err := filepath.Rel(beadsDir, f.Path)
				if err != nil {
					t.Fatal(err)
				}
				got[filepath.ToSlash(rel)] = f.SafeDelete
				if !f.SafeDelete && len(f.Remove) > 0 {
					t.Errorf("%s is not safe to delete but plans removals %q", rel, f.Remove)
				}
				for _, path := range f.Remove {
					removable[path] = true
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PlanArtifacts findings (path: safe) = %v, want %v", got, tc.want)
			}

			before := snapshotBeadsTree(t, beadsDir)
			if err := ClassicArtifacts(root); err != nil {
				t.Fatalf("ClassicArtifacts: %v", err)
			}
			want := map[string]string{}
			for rel, content := range before {
				if !removedBy(removable, filepath.Join(beadsDir, filepath.FromSlash(rel))) {
					want[rel] = content
				}
			}
			if after := snapshotBeadsTree(t, beadsDir); !reflect.DeepEqual(after, want) {
				t.Fatalf("ClassicArtifacts left %s as\n %q\nwant exactly the planned removals gone: %q", tc.dir, after, want)
			}
			wantOutside := map[string]string{}
			for rel, content := range outside {
				if !removedBy(removable, filepath.Join(root, filepath.FromSlash(rel))) {
					wantOutside[rel] = content
				}
			}
			if after := snapshotBeadsTree(t, root); !reflect.DeepEqual(after, wantOutside) {
				t.Fatalf("ClassicArtifacts changed something it did not plan to remove:\n got %q\nwant %q", after, wantOutside)
			}
		})
	}
}

// mergeTree returns the union of trees.
func mergeTree(trees ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, tree := range trees {
		for k, v := range tree {
			out[k] = v
		}
	}
	return out
}

// removedBy reports whether path is one of removed or lies under one.
func removedBy(removed map[string]bool, path string) bool {
	for p := path; ; p = filepath.Dir(p) {
		if removed[p] {
			return true
		}
		if parent := filepath.Dir(p); parent == p {
			return false
		}
	}
}

// TestRemoveDisposable_RefusesDatabaseState pins the deletion primitive every
// bd doctor cleanup goes through: whatever its caller planned, it refuses a
// SQLite database (a backup included, or any file with the SQLite header
// whatever its name; names match case-insensitively), the WAL, SHM or journal
// of a database still beside it (or a link in its place, wherever it points),
// a Dolt store, embedded Dolt or bd backup directory, and a
// directory holding any of those at any depth, and it never follows a
// symlink.
func TestRemoveDisposable_RefusesDatabaseState(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		target  string
		refused bool
	}{
		{name: "beads.db", files: map[string]string{"beads.db": "x"}, target: "beads.db", refused: true},
		{name: "backup database", files: map[string]string{"beads.backup-pre-dolt-20260226.db": "x"}, target: "beads.backup-pre-dolt-20260226.db", refused: true},
		{name: "any .db", files: map[string]string{"vc.db": "x"}, target: "vc.db", refused: true},
		{name: "WAL beside its database", files: map[string]string{"beads.db": "x", "beads.db-wal": "w"}, target: "beads.db-wal", refused: true},
		{name: "SHM beside its database", files: map[string]string{"beads.db": "x", "beads.db-shm": "s"}, target: "beads.db-shm", refused: true},
		{name: "journal beside its database", files: map[string]string{"beads.db": "x", "beads.db-journal": "j"}, target: "beads.db-journal", refused: true},
		{name: "orphaned WAL", files: map[string]string{"beads.db-wal": "w"}, target: "beads.db-wal"},
		{name: "dolt store", files: map[string]string{"dolt/": ""}, target: "dolt", refused: true},
		{name: "embedded Dolt", files: map[string]string{"embeddeddolt/": ""}, target: "embeddeddolt", refused: true},
		{name: "bd backups", files: map[string]string{"backup/issues.jsonl": "{}"}, target: "backup", refused: true},
		{name: "directory holding a Dolt repository", files: map[string]string{"old/rig/.dolt/": ""}, target: "old", refused: true},
		{name: "directory holding a database", files: map[string]string{"old/deep/x.db": "x"}, target: "old", refused: true},
		{name: "SQLite file not named *.db", files: map[string]string{"beads.db.bak": "SQLite format 3\x00rest"}, target: "beads.db.bak", refused: true},
		{name: "*.sqlite3", files: map[string]string{"issues.sqlite3": "SQLite format 3\x00rest"}, target: "issues.sqlite3", refused: true},
		{name: "upper-case .DB", files: map[string]string{"OLD.DB": "x"}, target: "OLD.DB", refused: true},
		{name: "upper-case WAL beside its database", files: map[string]string{"OLD.DB": "x", "OLD.DB-WAL": "w"}, target: "OLD.DB-WAL", refused: true},
		{name: "sidecar-named directory holding a database", files: map[string]string{"old.db-wal/inner.db": "x"}, target: "old.db-wal", refused: true},
		{name: "directory holding a SQLite file under any name", files: map[string]string{"keep/copy.bin": "SQLite format 3\x00rest"}, target: "keep", refused: true},
		{name: "header-less *.sqlite", files: map[string]string{"vault.sqlite": "encrypted"}, target: "vault.sqlite", refused: true},
		{name: "*.db3", files: map[string]string{"old.db3": "x"}, target: "old.db3", refused: true},
		{name: "WAL beside its *.sqlite3", files: map[string]string{"issues.sqlite3": "x", "issues.sqlite3-wal": "w"}, target: "issues.sqlite3-wal", refused: true},
		{name: "directory holding a sidecar-named file", files: map[string]string{"old/beads.db-wal": "w"}, target: "old", refused: true},
		{name: "Dolt chunk-store backup", files: doltChunkStore("snap/"), target: "snap", refused: true},
		{name: "Dolt chunk-store manifest under any name", files: map[string]string{"keep/m": doltManifest}, target: "keep", refused: true},
		{name: "Dolt archive file", files: map[string]string{"keep/x.darc": "a"}, target: "keep", refused: true},
		{name: "Dolt journal index", files: map[string]string{"journal.idx": "i"}, target: "journal.idx", refused: true},
		{name: "Dolt NBS table file", files: map[string]string{"keep/" + nbsTableName: "t"}, target: "keep", refused: true},
		{name: "orphaned WAL whose name lengthens when lower-cased", files: map[string]string{strings.Repeat("\u023a", 5) + ".db-wal": "w"}, target: strings.Repeat("\u023a", 5) + ".db-wal"},
		{name: "WAL beside its database, names lengthening when lower-cased", files: map[string]string{strings.Repeat("\u023a", 5) + ".db": "x", strings.Repeat("\u023a", 5) + ".db-wal": "w"}, target: strings.Repeat("\u023a", 5) + ".db-wal", refused: true},
		{name: "orphaned journal whose name lengthens when lower-cased", files: map[string]string{strings.Repeat("\u023a", 9) + ".db-journal": "j"}, target: strings.Repeat("\u023a", 9) + ".db-journal"},
		{name: "short file", files: map[string]string{"notes": "SQLite"}, target: "notes"},
		{name: "scaffolding file", files: map[string]string{"config.yaml": "c"}, target: "config.yaml"},
		{name: "scaffolding directory", files: map[string]string{"formulas/x.toml": "f"}, target: "formulas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBeadsTree(t, dir, tc.files)
			target := filepath.Join(dir, tc.target)
			err := RemoveDisposable(target)
			var refused *RefusedError
			if tc.refused {
				if !asRefused(err, &refused) {
					t.Fatalf("RemoveDisposable(%s) = %v, want a *RefusedError", tc.target, err)
				}
				if _, statErr := os.Lstat(target); statErr != nil {
					t.Fatalf("RemoveDisposable refused but %s is gone: %v", tc.target, statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RemoveDisposable(%s): %v", tc.target, err)
			}
			if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
				t.Fatalf("%s should have been removed (stat err %v)", tc.target, statErr)
			}
		})
	}

	// Symlinks are followed to classify, never to delete: a link to database
	// state is kept, and removing any other link leaves its target alone. A
	// link named for a sidecar beside its database is that database's
	// sidecar, wherever it points.
	links := []struct {
		name    string
		files   map[string]string
		link    string // the link's name; "" = "link"
		target  string // the link's target, relative to the temp dir; "" = dangling
		refused bool
	}{
		{name: "symlink to a directory holding a Dolt repository", files: map[string]string{"real/rig/.dolt/": ""}, target: "real", refused: true},
		{name: "symlink to a Dolt chunk store", files: doltChunkStore("real/"), target: "real", refused: true},
		{name: "symlink to a SQLite file", files: map[string]string{"real.bin": "SQLite format 3\x00rest"}, target: "real.bin", refused: true},
		{name: "symlink to scaffolding", files: map[string]string{"real/x.toml": "f"}, target: "real"},
		{name: "dangling symlink"},
		{name: "WAL symlink beside its database, to non-SQLite bytes", files: map[string]string{"beads.db": "x", "real/wal": walHeader}, link: "beads.db-wal", target: "real/wal", refused: true},
		{name: "SHM symlink beside its database", files: map[string]string{"beads.db": "x", "real/shm": "s"}, link: "beads.db-shm", target: "real/shm", refused: true},
		{name: "orphaned WAL symlink, to non-SQLite bytes", files: map[string]string{"real/wal": walHeader}, link: "beads.db-wal", target: "real/wal"},
	}
	for _, tc := range links {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("symlinks need privileges on Windows")
			}
			dir := t.TempDir()
			writeBeadsTree(t, dir, tc.files)
			before := snapshotBeadsTree(t, dir)
			target := filepath.Join(dir, "gone")
			if tc.target != "" {
				target = filepath.Join(dir, tc.target)
			}
			link := filepath.Join(dir, "link")
			if tc.link != "" {
				link = filepath.Join(dir, tc.link)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			err := RemoveDisposable(link)
			var refused *RefusedError
			_, statErr := os.Lstat(link)
			if tc.refused {
				if !asRefused(err, &refused) || statErr != nil {
					t.Fatalf("RemoveDisposable(link) = %v (link stat err %v), want a *RefusedError and the link kept", err, statErr)
				}
			} else if err != nil || !os.IsNotExist(statErr) {
				t.Fatalf("RemoveDisposable(link) = %v (link stat err %v), want the link removed", err, statErr)
			}
			_ = os.Remove(link)
			if after := snapshotBeadsTree(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("RemoveDisposable(link) reached its target:\n got %q\nwant %q", after, before)
			}
		})
	}
}

// doltManifest starts like the manifest of a Dolt chunk store.
const doltManifest = "5:__DOLT__:nqhvd2c4qe4m0hm9s3cqc5f6ilm1qv7a:abc"

// walHeader starts like a SQLite WAL file: the WAL magic number and format
// version 3007000, not the database header.
const walHeader = "\x37\x7f\x06\x82\x00\x2d\xe2\x18"

// nbsTableName is a Dolt NBS table file name: 32 base32 characters.
const nbsTableName = "0123456789abcdefghijklmnopqrstuv"

// doltChunkStore lays out, under prefix, a Dolt database in the raw
// chunk-store format a file:// Dolt backup or remote uses: no .dolt, just a
// manifest, archive table files, LOCK and oldgen/.
func doltChunkStore(prefix string) map[string]string {
	return map[string]string{
		prefix + "manifest":    doltManifest,
		prefix + "defbr.darc":  "archive",
		prefix + "LOCK":        "",
		prefix + "oldgen/LOCK": "",
	}
}

func asRefused(err error, target **RefusedError) bool {
	if err == nil {
		return false
	}
	r, ok := err.(*RefusedError)
	if ok {
		*target = r
	}
	return ok && strings.HasPrefix(r.Error(), "refusing to remove ")
}

// TestPlanArtifacts_ReviewFindingsNameWhatWasFound pins that a finding left
// for review says why: the entry and what isDatabaseState found in it, not a
// database the directory may not hold.
func TestPlanArtifacts_ReviewFindingsNameWhatWasFound(t *testing.T) {
	const sqliteDB = "SQLite format 3\x00rest"
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	// The kept WAL's reason names the resolved link target, so resolve the
	// root too: on macOS t.TempDir() is under /var, a symlink to /private/var.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(root, "polecats", "p1", ".beads")
	writeBeadsTree(t, stub, map[string]string{"redirect": "../../mayor/rig/.beads\n", "beads.db.bak": sqliteDB})
	dolt := filepath.Join(root, "sub", ".beads")
	writeBeadsTree(t, dolt, map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`})
	writeBeadsTree(t, root, map[string]string{"elsewhere/other.bin": sqliteDB})
	other := filepath.Join(root, "elsewhere", "other.bin")
	if err := os.Symlink(other, filepath.Join(dolt, "beads.db-wal")); err != nil {
		t.Fatal(err)
	}

	descriptions := func(beadsDir string) map[string]string {
		findings, _ := PlanArtifacts(beadsDir)
		out := map[string]string{}
		for _, f := range findings {
			out[filepath.Base(f.Path)] = f.Description
		}
		return out
	}
	want := map[string]string{
		".beads": "should be redirect-only but cannot be cleaned safely: beads.db.bak (a SQLite database); review manually",
	}
	if got := descriptions(stub); !reflect.DeepEqual(got, want) {
		t.Errorf("stub findings = %q, want %q", got, want)
	}
	want = map[string]string{
		"beads.db-wal": "SQLite WAL/SHM kept: it links to " + other + ": a SQLite database (Dolt is active backend)",
	}
	if got := descriptions(dolt); !reflect.DeepEqual(got, want) {
		t.Errorf("Dolt findings = %q, want %q", got, want)
	}
}

// TestMetadataNamesDolt_AnyDoltSettingAlone pins that metadata setting any
// one Dolt setting of configfile.Config, and nothing else, configures Dolt (a
// setting added later included): a .beads whose metadata records only its
// server host, port, socket or data directory is a workspace, not a stub's
// scaffolding. Metadata that sets none, such as a pre-Dolt metadata.json,
// configures nothing.
func TestMetadataNamesDolt_AnyDoltSettingAlone(t *testing.T) {
	found := map[string]bool{}
	typ := reflect.TypeOf(configfile.Config{})
	for i := 0; i < typ.NumField(); i++ {
		key, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if !strings.Contains(key, "dolt") {
			continue
		}
		found[key] = true
		var cfg configfile.Config
		switch field := reflect.ValueOf(&cfg).Elem().Field(i); field.Kind() {
		case reflect.String:
			field.SetString("x")
		case reflect.Int:
			field.SetInt(1)
		case reflect.Bool:
			field.SetBool(true)
		default:
			t.Fatalf("%s is a %s; set one in this test", key, field.Kind())
		}
		if !metadataNamesDolt(&cfg) {
			t.Errorf("metadata setting only %s does not configure Dolt", key)
		}
	}
	for _, key := range []string{"dolt_mode", "dolt_database", "dolt_server_host", "dolt_data_dir"} {
		if !found[key] {
			t.Errorf("configfile.Config has no %s setting for this test to cover", key)
		}
	}
	if !metadataNamesDolt(&configfile.Config{Backend: configfile.BackendDolt}) {
		t.Error("metadata naming backend dolt does not configure Dolt")
	}
	for name, cfg := range map[string]*configfile.Config{
		"no metadata":       nil,
		"pre-Dolt metadata": {Database: "beads.db"},
		"sqlite backend":    {Backend: "sqlite", SQLitePath: "issues.sqlite3"},
	} {
		if metadataNamesDolt(cfg) {
			t.Errorf("%s configures Dolt", name)
		}
	}
}
