//go:build cgo

package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// freshInitEnv makes bdInit leave every workspace's schema to its own
// `bd init` instead of seeding it from the per-process schema template
// (seedEmbeddedSchema), to check that a failure does not depend on the seed.
const freshInitEnv = "BEADS_TEST_FRESH_INIT"

// schemaTemplateDatabase is the template's database directory name; each
// seed is renamed to the workspace's own database name.
const schemaTemplateDatabase = "schematemplate"

var (
	schemaTemplateOnce sync.Once
	schemaTemplateDir  string
	schemaTemplateErr  error
)

// schemaToolEnv names a non-race build of
// internal/storage/embeddeddolt/cmd (runfiles path) for embeddedSchemaTemplate
// to run instead of migrating in this process, which under -race takes ~20s.
const schemaToolEnv = "BEADS_TEST_EMBEDDED_SCHEMA_TOOL"

// embeddedSchemaTemplate creates one embedded Dolt database per test process
// with every schema migration applied and nothing else (embeddeddolt.Open on
// an empty directory, as bd init's own open does), under testTempRoot, and
// returns its database directory.
func embeddedSchemaTemplate() (string, error) {
	schemaTemplateOnce.Do(func() {
		beadsDir, err := testTempDir("bd-schema-template-*")
		if err != nil {
			schemaTemplateErr = fmt.Errorf("create schema template dir: %w", err)
			return
		}
		if err := migrateSchemaTemplate(beadsDir); err != nil {
			schemaTemplateErr = fmt.Errorf("migrate schema template in %s: %w", beadsDir, err)
			return
		}
		schemaTemplateDir = filepath.Join(beadsDir, "embeddeddolt", schemaTemplateDatabase)
	})
	return schemaTemplateDir, schemaTemplateErr
}

func migrateSchemaTemplate(beadsDir string) error {
	if os.Getenv(schemaToolEnv) == "" {
		store, err := embeddeddolt.Open(context.Background(), beadsDir, schemaTemplateDatabase, "main")
		if err != nil {
			return err
		}
		return store.Close()
	}
	tool, err := bazeltest.RunfileEnv(schemaToolEnv)
	if err != nil {
		return err
	}
	out, err := exec.Command(tool, "--dir", beadsDir, "--database", schemaTemplateDatabase).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", tool, err, out)
	}
	return nil
}

// seededInitDatabase returns the embedded database name `bd init --quiet
// <extraArgs>` creates, and false when bdInit should not seed it: an argument
// it does not know (it may pick another mode or location), no --prefix (the
// name then comes from the directory), or BEADS_TEST_FRESH_INIT=1.
func seededInitDatabase(extraArgs []string) (string, bool) {
	if os.Getenv(freshInitEnv) == "1" {
		return "", false
	}
	var prefix, database string
	for i := 0; i < len(extraArgs); i++ {
		switch extraArgs[i] {
		case "--prefix", "--database":
			if i+1 == len(extraArgs) {
				return "", false
			}
			if extraArgs[i] == "--prefix" {
				prefix = extraArgs[i+1]
			} else {
				database = extraArgs[i+1]
			}
			i++
		case "--quiet", "--skip-hooks", "--skip-agents", "--non-interactive":
		default:
			return "", false
		}
	}
	if prefix == "" {
		return "", false
	}
	if database != "" {
		return database, true
	}
	return dbNameFromPrefix(normalizeIssuePrefix(prefix)), true
}

// seedEmbeddedSchema copies the schema template into dir's .beads as database
// before bd init runs there. bd init then opens a database whose migrations
// are all applied, so it skips the migration chain (one Dolt commit per
// migration: most of an init's cost under -race) and does everything else
// itself: identity, issue prefix, config, hooks, agent files, the git commit.
// Seeded workspaces share the template's migration commits as their history.
func seedEmbeddedSchema(t *testing.T, dir, database string) {
	t.Helper()
	tmpl, err := embeddedSchemaTemplate()
	if err != nil {
		t.Fatalf("%v", err)
	}
	dst := filepath.Join(dir, ".beads", "embeddeddolt", database)
	if err := copyTree(tmpl, dst); err != nil {
		t.Fatalf("seed embedded schema %s into %s: %v", tmpl, dst, err)
	}
}

// requireSeededEmbeddedInit fails unless bd init kept the seeded database as
// the workspace's embedded database.
func requireSeededEmbeddedInit(t *testing.T, beadsDir, database string) {
	t.Helper()
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("bd init on a seeded schema: load %s/metadata.json: cfg=%v err=%v", beadsDir, cfg, err)
	}
	if cfg.DoltMode != configfile.DoltModeEmbedded || cfg.DoltDatabase != database {
		t.Fatalf("bd init on a seeded schema chose mode %q database %q, want embedded %q; teach seededInitDatabase this case or run with %s=1",
			cfg.DoltMode, cfg.DoltDatabase, database, freshInitEnv)
	}
}

// copyTree copies src's tree to dst, keeping file and directory modes.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm()|0o700)
		case d.Type().IsRegular():
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, b, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		default:
			return fmt.Errorf("unexpected file type %s at %s", d.Type(), path)
		}
	})
}

func TestSeededInitDatabase(t *testing.T) {
	t.Setenv(freshInitEnv, "")
	for _, tt := range []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{"--prefix", "gc"}, "gc", true},
		{[]string{"--prefix", "my-proj", "--skip-hooks", "--skip-agents"}, "my_proj", true},
		{[]string{"--prefix", "a.b"}, "a_b", true},
		{[]string{"--database", "shared_db", "--prefix", "alpha"}, "shared_db", true},
		{nil, "", false},
		{[]string{"--prefix"}, "", false},
		{[]string{"--prefix", "gc", "--server"}, "", false},
		{[]string{"--prefix", "gc", "--role", "contributor"}, "", false},
	} {
		got, ok := seededInitDatabase(tt.args)
		if got != tt.want || ok != tt.ok {
			t.Errorf("seededInitDatabase(%q) = %q, %v; want %q, %v", tt.args, got, ok, tt.want, tt.ok)
		}
	}
	t.Setenv(freshInitEnv, "1")
	if _, ok := seededInitDatabase([]string{"--prefix", "gc"}); ok {
		t.Errorf("seededInitDatabase seeds with %s=1", freshInitEnv)
	}
}

func TestCopyTreeKeepsModes(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(filepath.Join(src, "noms"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "noms", "manifest"), []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hook"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "noms", "manifest")); err != nil || string(b) != "m" {
		t.Fatalf("copied manifest = %q, %v", b, err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(dst, "noms"):             0o750,
		filepath.Join(dst, "noms", "manifest"): 0o600,
		filepath.Join(dst, "hook"):             0o755,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}
