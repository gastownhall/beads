package notion

import (
	"context"
	"os"
	"path/filepath"
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

// TestResolveAuth_FallsBackToLegacyDatabaseToken verifies that a token written
// to the database by an older bd still authenticates, ahead of NOTION_TOKEN.
func TestResolveAuth_FallsBackToLegacyDatabaseToken(t *testing.T) {
	loadNotionYamlConfig(t, "")
	t.Setenv("NOTION_TOKEN", "env-token")

	auth, err := ResolveAuth(context.Background(), mapConfigReader{configKeyToken: "db-token"})
	if err != nil {
		t.Fatalf("ResolveAuth returned error: %v", err)
	}
	if auth == nil || auth.Token != "db-token" || auth.Source != AuthSourceConfigToken {
		t.Fatalf("auth = %+v, want db-token from %q", auth, AuthSourceConfigToken)
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
