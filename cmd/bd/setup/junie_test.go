package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestJunieGuidelinesTemplate(t *testing.T) {
	requiredContent := []string{
		"bd ready",
		"bd create",
		"bd update",
		"bd close",
		"bd dolt push",
		"mcp_beads_ready",
		"mcp_beads_list",
		"mcp_beads_create",
		"bug",
		"feature",
		"task",
		"epic",
	}

	for _, req := range requiredContent {
		if !strings.Contains(junieGuidelinesTemplate, req) {
			t.Errorf("junieGuidelinesTemplate missing required content: %q", req)
		}
	}
}

func TestJunieMCPConfig(t *testing.T) {
	config := junieMCPConfig()

	// Verify structure
	mcpServers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers key missing or wrong type")
	}

	beads, ok := mcpServers["beads"].(map[string]interface{})
	if !ok {
		t.Fatal("beads server config missing or wrong type")
	}

	// bd has no "mcp" subcommand (GH#5563): the server is beads-mcp.
	command, ok := beads["command"].(string)
	if !ok || command != "uvx" {
		t.Errorf("Expected command 'uvx', got %v", beads["command"])
	}

	args, ok := beads["args"].([]string)
	if !ok || len(args) != 1 || args[0] != "beads-mcp" {
		t.Errorf("Expected args ['beads-mcp'], got %v", beads["args"])
	}

	if len(mcpServers) != 1 || len(beads) != 2 {
		t.Errorf("Expected one beads server with only command and args, got %v", mcpServers)
	}

	// Verify it's valid JSON
	data, err := json.Marshal(config)
	if err != nil {
		t.Errorf("MCP config should be valid JSON: %v", err)
	}

	// Verify it can be unmarshaled back
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Errorf("MCP config JSON should be parseable: %v", err)
	}
}

func TestInstallJunie(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	if err := InstallJunie(); err != nil {
		t.Fatalf("InstallJunie: %v", err)
	}

	// Verify guidelines file was created
	guidelinesPath := ".junie/guidelines.md"
	if !FileExists(guidelinesPath) {
		t.Errorf("File was not created: %s", guidelinesPath)
	} else {
		data, err := os.ReadFile(guidelinesPath)
		if err != nil {
			t.Errorf("Failed to read %s: %v", guidelinesPath, err)
		} else if string(data) != junieGuidelinesTemplate {
			t.Errorf("File %s content doesn't match expected template", guidelinesPath)
		}
	}

	// Verify MCP config file was created
	mcpPath := ".junie/mcp/mcp.json"
	if !FileExists(mcpPath) {
		t.Errorf("File was not created: %s", mcpPath)
	} else {
		data, err := os.ReadFile(mcpPath)
		if err != nil {
			t.Errorf("Failed to read %s: %v", mcpPath, err)
		} else {
			// Verify it's valid JSON
			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Errorf("MCP config should be valid JSON: %v", err)
			}

			// Verify structure
			mcpServers, ok := parsed["mcpServers"].(map[string]interface{})
			if !ok {
				t.Error("mcpServers key missing or wrong type")
			} else if beads, ok := mcpServers["beads"].(map[string]interface{}); !ok {
				t.Error("beads server config missing")
			} else {
				// The written file must launch beads-mcp, never a bd
				// subcommand that does not exist (GH#5563).
				args, _ := beads["args"].([]interface{})
				if beads["command"] != "uvx" || len(args) != 1 || args[0] != "beads-mcp" {
					t.Errorf("MCP config should launch uvx beads-mcp, got command %v args %v", beads["command"], beads["args"])
				}
			}
			assertJunieMCPConfigFile(t, mcpPath)
		}
	}
}

// wantJunieMCPConfig is the MCP config bd setup junie must write, spelled out
// independently of junieMCPConfig so a wrong launch command cannot satisfy
// both sides of the comparison.
const wantJunieMCPConfig = `{"mcpServers":{"beads":{"command":"uvx","args":["beads-mcp"]}}}`

// assertJunieMCPConfigFile checks that path decodes to exactly
// wantJunieMCPConfig. It compares decoded JSON, not bytes, so indentation and
// trailing newlines stay free to change.
func assertJunieMCPConfigFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	var got, want interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MCP config is not JSON: %v\n%s", err, data)
	}
	if err := json.Unmarshal([]byte(wantJunieMCPConfig), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MCP config = %s, want %s", data, wantJunieMCPConfig)
	}
}

func TestInstallJunie_ExistingDirectory(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Pre-create the directories
	if err := os.MkdirAll(".junie/mcp", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Should not fail
	InstallJunie()

	// Verify files were created
	if !FileExists(".junie/guidelines.md") {
		t.Error("guidelines.md not created")
	}
	if !FileExists(".junie/mcp/mcp.json") {
		t.Error("mcp.json not created")
	}
}

func TestInstallJunieIdempotent(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Run twice
	InstallJunie()
	firstGuidelines, _ := os.ReadFile(".junie/guidelines.md")
	firstMCP, _ := os.ReadFile(".junie/mcp/mcp.json")

	InstallJunie()
	secondGuidelines, _ := os.ReadFile(".junie/guidelines.md")
	secondMCP, _ := os.ReadFile(".junie/mcp/mcp.json")

	if string(firstGuidelines) != string(secondGuidelines) {
		t.Error("InstallJunie should be idempotent for guidelines")
	}
	if string(firstMCP) != string(secondMCP) {
		t.Error("InstallJunie should be idempotent for MCP config")
	}
}

func TestRemoveJunie(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Install first
	InstallJunie()

	// Verify files exist
	files := []string{".junie/guidelines.md", ".junie/mcp/mcp.json"}
	for _, f := range files {
		if !FileExists(f) {
			t.Fatalf("File should exist before removal: %s", f)
		}
	}

	// Remove
	RemoveJunie()

	// Verify files are gone
	for _, f := range files {
		if FileExists(f) {
			t.Errorf("File should have been removed: %s", f)
		}
	}
}

func TestRemoveJunie_NoFiles(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Should not panic when files don't exist
	RemoveJunie()
}

func TestRemoveJunie_PartialFiles(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Create only the guidelines file
	if err := os.MkdirAll(".junie", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(".junie/guidelines.md", []byte(junieGuidelinesTemplate), 0644); err != nil {
		t.Fatalf("failed to create guidelines file: %v", err)
	}

	// Should not panic
	RemoveJunie()

	// Guidelines should be removed
	if FileExists(".junie/guidelines.md") {
		t.Error("Guidelines file should have been removed")
	}
}

func TestRemoveJunie_DirectoryCleanup(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Install
	InstallJunie()

	// Remove
	RemoveJunie()

	// Directories should be cleaned up if empty
	if DirExists(".junie/mcp") {
		t.Error(".junie/mcp directory should be removed when empty")
	}
	if DirExists(".junie") {
		t.Error(".junie directory should be removed when empty")
	}
}

func TestRemoveJunie_DirectoryWithOtherFiles(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Install
	InstallJunie()

	// Add another file to .junie directory
	if err := os.WriteFile(".junie/other.txt", []byte("keep me"), 0644); err != nil {
		t.Fatalf("failed to create other file: %v", err)
	}

	// Remove
	RemoveJunie()

	// Directory should still exist (has other files)
	if !DirExists(".junie") {
		t.Error("Directory should not be removed when it has other files")
	}

	// Other file should still exist
	if !FileExists(".junie/other.txt") {
		t.Error("Other files should be preserved")
	}
}

func TestCheckJunie_NotInstalled(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	if err := CheckJunie(); err == nil {
		t.Fatal("CheckJunie should return error when not installed")
	}
}

func TestCheckJunie_Installed(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Install first
	InstallJunie()

	// Should not panic or exit
	CheckJunie()
}

func TestCheckJunie_PartialInstall_GuidelinesOnly(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Create only guidelines
	if err := os.MkdirAll(".junie", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(".junie/guidelines.md", []byte(junieGuidelinesTemplate), 0644); err != nil {
		t.Fatalf("failed to create guidelines file: %v", err)
	}

	if err := CheckJunie(); err == nil {
		t.Fatal("CheckJunie should return error for partial installation")
	}
}

func TestCheckJunie_PartialInstall_MCPOnly(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Create only MCP config
	if err := os.MkdirAll(".junie/mcp", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	mcpConfig := junieMCPConfig()
	mcpData, _ := json.MarshalIndent(mcpConfig, "", "  ")
	if err := os.WriteFile(".junie/mcp/mcp.json", mcpData, 0644); err != nil {
		t.Fatalf("failed to create MCP config file: %v", err)
	}

	if err := CheckJunie(); err == nil {
		t.Fatal("CheckJunie should return error for partial installation")
	}
}

func TestJunieFilePaths(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	InstallJunie()

	// Check expected file paths
	expectedPaths := []string{
		".junie/guidelines.md",
		".junie/mcp/mcp.json",
	}

	for _, path := range expectedPaths {
		if !FileExists(path) {
			t.Errorf("Expected file at %s", path)
		}
	}
}

func TestJunieGuidelinesWorkflowPattern(t *testing.T) {
	// Verify guidelines contain the workflow patterns Junie users need
	guidelines := junieGuidelinesTemplate

	// Should mention core workflow commands
	if !strings.Contains(guidelines, "bd ready") {
		t.Error("Should mention bd ready")
	}
	if !strings.Contains(guidelines, "bd dolt push") {
		t.Error("Should mention bd dolt push")
	}

	// Should explain MCP tools
	if !strings.Contains(guidelines, "MCP Tools Available") {
		t.Error("Should have MCP Tools section")
	}
}

// Remove takes bd's files out of .junie/mcp and .junie and then removes each
// directory if they were all it held, but it never removes one that is a
// symlink, since that unlinks it whatever its target holds, nor one it took
// nothing from.
func TestRemoveJunieKeepsDirsItDidNotEmpty(t *testing.T) {
	junie := ".junie"
	mcp := filepath.Join(junie, "mcp")
	for _, tt := range []struct {
		name    string
		link    string // the directory that is a symlink to one beside the workspace
		mkdir   string // a real directory the user made
		install bool   // bd setup junie ran first
		drop    string // a file or directory of bd's the user then deleted by hand
		want    map[string]string
	}{
		{name: "linked .junie bd installed its files through", link: junie, install: true,
			want: map[string]string{junie: "link", mcp: "gone"}},
		{name: "linked .junie bd installed nothing in", link: junie,
			want: map[string]string{junie: "link"}},
		{name: "linked .junie/mcp bd installed its config through", link: mcp, install: true,
			want: map[string]string{junie: "dir", mcp: "link"}},
		{name: "linked .junie/mcp bd installed nothing in", link: mcp,
			want: map[string]string{junie: "dir", mcp: "link"}},
		{name: "real .junie bd installed nothing in", mkdir: junie,
			want: map[string]string{junie: "dir"}},
		{name: "real .junie/mcp bd installed nothing in", mkdir: mcp,
			want: map[string]string{junie: "dir", mcp: "dir"}},
		{name: "real .junie and .junie/mcp holding only bd's files", install: true,
			want: map[string]string{junie: "gone"}},
		{name: "real empty .junie/mcp bd took no file from", install: true,
			drop: filepath.Join(mcp, "mcp.json"),
			want: map[string]string{junie: "dir", mcp: "dir"}},
		{name: "real .junie, .junie/mcp deleted by hand", install: true,
			drop: mcp,
			want: map[string]string{junie: "gone"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			ws := filepath.Join(root, "ws")
			if err := os.MkdirAll(ws, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.link != "" {
				link := filepath.Join(ws, tt.link)
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				symlinkDir(t, filepath.Join(root, "shared"), link)
			}
			if tt.mkdir != "" {
				if err := os.MkdirAll(filepath.Join(ws, tt.mkdir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(ws)
			if tt.install {
				if err := InstallJunie(); err != nil {
					t.Fatalf("InstallJunie: %v", err)
				}
			}
			if tt.drop != "" {
				if err := os.RemoveAll(filepath.Join(ws, tt.drop)); err != nil {
					t.Fatal(err)
				}
			}

			if err := RemoveJunie(); err != nil {
				t.Fatalf("RemoveJunie: %v", err)
			}
			requireGone(t, filepath.Join(ws, junie, "guidelines.md"))
			requireGone(t, filepath.Join(ws, mcp, "mcp.json"))
			for rel, want := range tt.want {
				requireState(t, filepath.Join(ws, rel), want)
			}
		})
	}
}

// The Junie MCP entry must launch the beads MCP server (beads-mcp), not a bd
// subcommand: bd has no `mcp` command, so `bd mcp` exits "unknown command".
func TestJunieMCPConfigLaunchesBeadsMCP(t *testing.T) {
	beads := junieMCPConfig()["mcpServers"].(map[string]interface{})["beads"].(map[string]interface{})
	command, _ := beads["command"].(string)
	args, _ := beads["args"].([]string)
	argv := append([]string{command}, args...)
	launchesBeadsMCP := false
	for _, a := range argv {
		if a == "beads-mcp" {
			launchesBeadsMCP = true
		}
	}
	if !launchesBeadsMCP {
		t.Fatalf("Junie MCP server argv %q does not launch beads-mcp", argv)
	}
}

// junieGuidelinesSHA256 pins the bytes bd setup junie writes to
// .junie/guidelines.md. GH#5563 changed only the command the MCP config
// launches, so the guidelines file must come out byte-identical to what bd
// wrote before that fix. An intended guidelines edit updates this hash in the
// same change.
const junieGuidelinesSHA256 = "15998dff7549a774df8cd7eb838f997f098fffff4a66efc22dd063a0ba66c382"

func junieSHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// No-change proof for the paths GH#5563 does not touch: a fresh install, and
// a re-run over the config an older bd wrote (one launching the nonexistent
// "bd mcp"), both leave .junie/guidelines.md at the pinned bytes and leave a
// file bd does not own under .junie as it was. Only the MCP config changes.
func TestInstallJunieLeavesUntouchedPathsUnchanged(t *testing.T) {
	t.Run("fresh install", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := InstallJunie(); err != nil {
			t.Fatalf("InstallJunie: %v", err)
		}
		got, err := os.ReadFile(".junie/guidelines.md")
		if err != nil {
			t.Fatalf("read guidelines: %v", err)
		}
		if h := junieSHA256Hex(got); h != junieGuidelinesSHA256 {
			t.Errorf("guidelines.md sha256 = %s, want unchanged %s", h, junieGuidelinesSHA256)
		}
	})

	t.Run("over a stale bd mcp config", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.MkdirAll(".junie/mcp", 0o755); err != nil {
			t.Fatal(err)
		}
		stale := `{"mcpServers":{"beads":{"command":"bd","args":["mcp"]}}}`
		userNote := "kept by the user\n"
		for path, data := range map[string]string{
			".junie/guidelines.md": junieGuidelinesTemplate,
			".junie/mcp/mcp.json":  stale,
			".junie/notes.md":      userNote,
		} {
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		if err := InstallJunie(); err != nil {
			t.Fatalf("InstallJunie: %v", err)
		}

		got, err := os.ReadFile(".junie/guidelines.md")
		if err != nil {
			t.Fatalf("read guidelines: %v", err)
		}
		if h := junieSHA256Hex(got); h != junieGuidelinesSHA256 {
			t.Errorf("guidelines.md sha256 = %s, want unchanged %s", h, junieGuidelinesSHA256)
		}
		if note, err := os.ReadFile(".junie/notes.md"); err != nil || string(note) != userNote {
			t.Errorf(".junie/notes.md = %q, %v; want it untouched", note, err)
		}
		mcp, err := os.ReadFile(".junie/mcp/mcp.json")
		if err != nil {
			t.Fatalf("read MCP config: %v", err)
		}
		if string(mcp) == stale || strings.Contains(string(mcp), `"mcp"`) {
			t.Errorf("stale bd mcp config was not replaced: %s", mcp)
		}
	})
}

// Identity and fingerprint invariant: GH#5563 changes what the beads MCP
// entry launches, not which entry it is. The file stays at
// .junie/mcp/mcp.json, holds exactly one server named "beads" (the name an
// existing Junie setup knows it by), that entry carries only command and
// args, the file decodes to exactly what junieMCPConfig renders, and its
// sha256 is the same on every run, so re-running bd setup junie converges on
// one file.
func TestJunieMCPConfigIdentityAndFingerprintInvariant(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := InstallJunie(); err != nil {
		t.Fatalf("first InstallJunie: %v", err)
	}
	first, err := os.ReadFile(".junie/mcp/mcp.json")
	if err != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	if err := InstallJunie(); err != nil {
		t.Fatalf("second InstallJunie: %v", err)
	}
	second, err := os.ReadFile(".junie/mcp/mcp.json")
	if err != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	if a, b := junieSHA256Hex(first), junieSHA256Hex(second); a != b {
		t.Errorf("MCP config fingerprint changed across runs: %s then %s", a, b)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(first, &got); err != nil {
		t.Fatalf("MCP config is not JSON: %v", err)
	}
	servers, _ := got["mcpServers"].(map[string]interface{})
	if keys := sortedJunieKeys(servers); !reflect.DeepEqual(keys, []string{"beads"}) {
		t.Errorf("mcpServers names = %v, want exactly [beads]", keys)
	}
	entry, _ := servers["beads"].(map[string]interface{})
	if keys := sortedJunieKeys(entry); !reflect.DeepEqual(keys, []string{"args", "command"}) {
		t.Errorf("beads entry keys = %v, want exactly [args command]", keys)
	}

	rendered, err := json.Marshal(junieMCPConfig())
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]interface{}
	if err := json.Unmarshal(rendered, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("written MCP config = %v, want what junieMCPConfig renders %v", got, want)
	}
}

func sortedJunieKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// An older bd wrote a config launching the nonexistent "bd mcp" (GH#5563).
// Re-running setup over it must repair the config, leave the guidelines at the
// template, and converge: a further run writes the same bytes.
func TestInstallJunieRewritesStaleBdMCPConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".junie/mcp", 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"mcpServers":{"beads":{"command":"bd","args":["mcp"]}}}`
	if err := os.WriteFile(".junie/mcp/mcp.json", []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".junie/guidelines.md", []byte(junieGuidelinesTemplate), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallJunie(); err != nil {
		t.Fatalf("InstallJunie over stale config: %v", err)
	}
	assertJunieMCPConfigFile(t, ".junie/mcp/mcp.json")
	if got, err := os.ReadFile(".junie/guidelines.md"); err != nil || string(got) != junieGuidelinesTemplate {
		t.Errorf("guidelines.md changed by the repair (err %v)", err)
	}

	repaired, err := os.ReadFile(".junie/mcp/mcp.json")
	if err != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	if err := InstallJunie(); err != nil {
		t.Fatalf("second InstallJunie: %v", err)
	}
	again, err := os.ReadFile(".junie/mcp/mcp.json")
	if err != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	if string(again) != string(repaired) {
		t.Errorf("MCP config changed on re-run:\nfirst:  %s\nsecond: %s", repaired, again)
	}
}
