package notion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// mapConfigReader stands in for the Dolt config table.
type mapConfigReader map[string]string

func (m mapConfigReader) GetConfig(_ context.Context, key string) (string, error) {
	return m[key], nil
}

// loadNotionYamlConfig points the global config at a temp workspace whose
// .beads/config.yaml holds yamlBody, isolated from the developer's own config.
func loadNotionYamlConfig(t *testing.T, yamlBody string) {
	t.Helper()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	t.Setenv("HOME", filepath.Join(tmpDir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	t.Chdir(tmpDir)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
}

// TestResolveAuth_ReadsTokenFromConfigYaml verifies that notion.token is read
// from config.yaml, where `bd config set notion.token` now writes it, ahead of
// both a legacy database value and NOTION_TOKEN (GH#6676).
func TestResolveAuth_ReadsTokenFromConfigYaml(t *testing.T) {
	loadNotionYamlConfig(t, "notion.token: \"yaml-token\"\n")
	t.Setenv("NOTION_TOKEN", "env-token")

	auth, err := ResolveAuth(context.Background(), mapConfigReader{configKeyToken: "db-token"})
	if err != nil {
		t.Fatalf("ResolveAuth returned error: %v", err)
	}
	if auth == nil || auth.Token != "yaml-token" || auth.Source != AuthSourceConfigToken {
		t.Fatalf("auth = %+v, want yaml-token from %q", auth, AuthSourceConfigToken)
	}
}

// TestResolveAuth_ReadsTheShapeConfigSetWrites closes the end-to-end gap the
// flat-fixture tests leave open: they assert `notion.token: x`, but that is not
// what `bd config set notion.token` puts on disk. SetYamlConfig routes through
// updateYamlKey, which hands any dotted key to updateNestedYamlKey — and that
// succeeds even on an empty file — so the bytes written are `notion:\n
// token: x`. Nothing pinned that the reader resolves the written shape, which
// is the PR's headline claim: set writes config.yaml, ResolveAuth reads it
// back.
//
// It drives the real writer rather than a second hand-written fixture on
// purpose: a fixture encodes today's shape and would keep passing if the writer
// changed, which is precisely the regression this is here to catch.
func TestResolveAuth_ReadsTheShapeConfigSetWrites(t *testing.T) {
	loadNotionYamlConfig(t, "")

	// The exact call the yaml-only branch of `bd config set` makes.
	if err := config.SetYamlConfig(configKeyToken, "written-token"); err != nil {
		t.Fatalf("SetYamlConfig: %v", err)
	}

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize after write: %v", err)
	}

	// nil reader: no database is involved, so a pass can only come from the
	// file. NOTION_TOKEN is deliberately left unset — with a fallback in place
	// a reader bug would still return a token and the test would not notice.
	auth, err := ResolveAuth(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveAuth returned error: %v", err)
	}
	if auth == nil || auth.Token != "written-token" || auth.Source != AuthSourceConfigToken {
		t.Fatalf("auth = %+v, want written-token from %q", auth, AuthSourceConfigToken)
	}

	// Non-vacuity guard, not a formatting preference: if the writer ever emits
	// the flat key, this test silently becomes a duplicate of
	// TestResolveAuth_ReadsTokenFromConfigYaml and stops covering the nested
	// path. Failing here says the premise moved, not that the behavior broke.
	onDisk, err := os.ReadFile(filepath.Join(".beads", "config.yaml"))
	if err != nil {
		t.Fatalf("read back config.yaml: %v", err)
	}
	if !strings.Contains(string(onDisk), "notion:") {
		t.Errorf("config.yaml is not the nested shape this test exists to cover; writer output:\n%s", onDisk)
	}
}

// TestResolveAuth_FallsBackToLegacyDatabaseToken verifies that a token written
// to the database by an older bd still authenticates, ahead of NOTION_TOKEN.
//
// It also pins the source as AuthSourceDatabaseLegacy rather than
// AuthSourceConfigToken. That distinction is the whole reportable difference
// between a workspace that is clean and one whose token was pushed to its
// remotes, so a regression collapsing the two would silently remove the only
// signal `bd notion status` has to tell the second kind to rotate.
func TestResolveAuth_FallsBackToLegacyDatabaseToken(t *testing.T) {
	loadNotionYamlConfig(t, "")
	t.Setenv("NOTION_TOKEN", "env-token")

	auth, err := ResolveAuth(context.Background(), mapConfigReader{configKeyToken: "db-token"})
	if err != nil {
		t.Fatalf("ResolveAuth returned error: %v", err)
	}
	if auth == nil || auth.Token != "db-token" || auth.Source != AuthSourceDatabaseLegacy {
		t.Fatalf("auth = %+v, want db-token from %q", auth, AuthSourceDatabaseLegacy)
	}
}

// TestResolveAuth_FallsBackToEnv verifies NOTION_TOKEN is used when neither
// config.yaml nor the database holds a token, including with no reader at all.
func TestResolveAuth_FallsBackToEnv(t *testing.T) {
	loadNotionYamlConfig(t, "")
	t.Setenv("NOTION_TOKEN", "env-token")

	for name, reader := range map[string]ConfigReader{
		"empty reader": mapConfigReader{},
		"nil reader":   nil,
	} {
		t.Run(name, func(t *testing.T) {
			auth, err := ResolveAuth(context.Background(), reader)
			if err != nil {
				t.Fatalf("ResolveAuth returned error: %v", err)
			}
			if auth == nil || auth.Token != "env-token" || auth.Source != AuthSourceEnv {
				t.Fatalf("auth = %+v, want env-token from %q", auth, AuthSourceEnv)
			}
		})
	}
}
