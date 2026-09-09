package versioncheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryReleaseVersionsMatch(t *testing.T) {
	root := filepath.Join("..", "..")
	report, err := Check(root)
	if err != nil {
		t.Fatalf("repository release metadata is inconsistent: %v", err)
	}
	if report.CheckedSources != 6 {
		t.Fatalf("checked sources = %d, want 6", report.CheckedSources)
	}
	hookEntries, err := os.ReadDir(filepath.Join(root, ".githooks"))
	if err != nil {
		t.Fatal(err)
	}
	expectedHookMarkers := 0
	for _, entry := range hookEntries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, ".githooks", entry.Name())
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().IsRegular() {
			expectedHookMarkers += 2
		}
	}
	if report.CheckedHookMarkers != expectedHookMarkers {
		t.Fatalf(
			"checked hook markers = %d, want %d",
			report.CheckedHookMarkers,
			expectedHookMarkers,
		)
	}
	if len(report.Sources) != 7+expectedHookMarkers {
		t.Fatalf(
			"reported sources = %d, want %d (six release files, uv.lock, and tracked hook markers)",
			len(report.Sources),
			7+expectedHookMarkers,
		)
	}
}

func TestCheckCoversEveryReleaseSource(t *testing.T) {
	expected := map[string]string{
		"integrations/beads-mcp/pyproject.toml":            "MCP pyproject.toml",
		"integrations/beads-mcp/src/beads_mcp/__init__.py": "MCP __init__.py",
		"plugins/beads/.claude-plugin/plugin.json":         "Claude plugin.json",
		"plugins/beads/.codex-plugin/plugin.json":          "Codex plugin.json",
		".claude-plugin/marketplace.json":                  "Claude marketplace.json",
		"npm-package/package.json":                         "npm package.json",
	}
	if len(releaseSources) != len(expected) {
		t.Fatalf("release source count = %d, want %d", len(releaseSources), len(expected))
	}

	for _, item := range releaseSources {
		description, ok := expected[item.path]
		if !ok {
			t.Fatalf("unexpected release source %q", item.path)
		}
		if item.description != description {
			t.Fatalf(
				"description for %q = %q, want %q",
				item.path,
				item.description,
				description,
			)
		}
		delete(expected, item.path)

		t.Run(item.description, func(t *testing.T) {
			root := writeFixture(t, "1.1.0")
			path := filepath.Join(root, filepath.FromSlash(item.path))
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.ReplaceAll(string(content), "1.1.0", "9.9.9"))
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err = Check(root)
			if err == nil {
				t.Fatal("mismatch unexpectedly passed")
			}
			want := item.description + ": 9.9.9 (expected 1.1.0)"
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
	if len(expected) != 0 {
		t.Fatalf("missing release sources: %v", expected)
	}
}

func TestCheckValidatesTrackedHookMarkerVersions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "begin mismatch",
			content: "# --- BEGIN BEADS INTEGRATION v9.9.9 ---\n" +
				"body\n# --- END BEADS INTEGRATION v1.1.0 ---\n",
			want: ".githooks/pre-push BEGIN marker: 9.9.9 (expected 1.1.0)",
		},
		{
			name: "end mismatch",
			content: "# --- BEGIN BEADS INTEGRATION v1.1.0 ---\n" +
				"body\n# --- END BEADS INTEGRATION v9.9.9 ---\n",
			want: ".githooks/pre-push END marker: 9.9.9 (expected 1.1.0)",
		},
		{
			name:    "missing begin",
			content: "body\n# --- END BEADS INTEGRATION v1.1.0 ---\n",
			want:    ".githooks/pre-push BEGIN marker: no 'BEGIN BEADS INTEGRATION' marker found",
		},
		{
			name: "missing end",
			content: "# --- BEGIN BEADS INTEGRATION v1.1.0 ---\n" +
				"body\n",
			want: ".githooks/pre-push END marker: no 'END BEADS INTEGRATION' marker found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeFixture(t, "1.1.0")
			path := filepath.Join(root, ".githooks", "pre-push")
			if err := os.WriteFile(path, []byte(test.content), 0o755); err != nil {
				t.Fatal(err)
			}

			_, err := Check(root)
			if err == nil {
				t.Fatal("invalid tracked hook marker unexpectedly passed")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestCheckDiscoversAdditionalTrackedHookFiles(t *testing.T) {
	root := writeFixture(t, "1.1.0")
	path := filepath.Join(root, ".githooks", "post-merge")
	if err := os.WriteFile(
		path,
		[]byte(
			"# --- BEGIN BEADS INTEGRATION v1.1.0 ---\n"+
				"body\n# --- END BEADS INTEGRATION v1.1.0 ---\n",
		),
		0o755,
	); err != nil {
		t.Fatal(err)
	}

	report, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.CheckedHookMarkers != 4 {
		t.Fatalf("checked hook markers = %d, want 4", report.CheckedHookMarkers)
	}
	want := map[string]bool{
		".githooks/post-merge BEGIN marker": false,
		".githooks/post-merge END marker":   false,
	}
	for _, result := range report.Sources {
		if _, ok := want[result.Description]; ok {
			want[result.Description] = true
		}
	}
	for description, found := range want {
		if !found {
			t.Fatalf("additional tracked hook result %q not reported", description)
		}
	}
}

func TestCheckTreatsRepositoryRootAsLiteralPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "[repo]")
	writeFixtureAt(t, root, "1.1.0")

	report, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.CheckedHookMarkers != 2 {
		t.Fatalf("checked hook markers = %d, want 2", report.CheckedHookMarkers)
	}
}

func TestCheckIgnoresDotfilesInTrackedHooks(t *testing.T) {
	root := writeFixture(t, "1.1.0")
	if err := os.WriteFile(
		filepath.Join(root, ".githooks", ".DS_Store"),
		[]byte("not a managed hook\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	report, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.CheckedHookMarkers != 2 {
		t.Fatalf("checked hook markers = %d, want 2", report.CheckedHookMarkers)
	}
}

func TestCheckRejectsMissingMalformedOrAmbiguousMetadata(t *testing.T) {
	tests := []struct {
		name string
		path string
		data []byte
		want string
	}{
		{
			name: "missing metadata",
			path: "integrations/beads-mcp/src/beads_mcp/__init__.py",
			data: nil,
			want: "MCP __init__.py:",
		},
		{
			name: "duplicate project TOML field",
			path: "integrations/beads-mcp/pyproject.toml",
			data: []byte("[project]\nversion = \"1.1.0\"\nversion = \"1.1.0\"\n"),
			want: "MCP pyproject.toml: parse TOML:",
		},
		{
			name: "version in wrong TOML table",
			path: "integrations/beads-mcp/pyproject.toml",
			data: []byte("[tool.example]\nversion = \"1.1.0\"\n"),
			want: "MCP pyproject.toml: non-empty string field [project].version not found",
		},
		{
			name: "wrong-case TOML project table",
			path: "integrations/beads-mcp/pyproject.toml",
			data: []byte("[Project]\nversion = \"1.1.0\"\n"),
			want: `MCP pyproject.toml: field [project] is ambiguous or uses non-exact casing: "Project"`,
		},
		{
			name: "wrong-case TOML version field",
			path: "integrations/beads-mcp/pyproject.toml",
			data: []byte("[project]\nVersion = \"1.1.0\"\n"),
			want: `MCP pyproject.toml: field [project].version is ambiguous or uses non-exact casing: "Version"`,
		},
		{
			name: "case-alias TOML project tables",
			path: "integrations/beads-mcp/pyproject.toml",
			data: []byte(
				"[project]\nversion = \"1.1.0\"\n" +
					"[Project]\nversion = \"9.9.9\"\n",
			),
			want: `MCP pyproject.toml: field [project] is ambiguous or uses non-exact casing: "Project", "project"`,
		},
		{
			name: "case-alias TOML version fields",
			path: "integrations/beads-mcp/pyproject.toml",
			data: []byte(
				"[project]\nversion = \"1.1.0\"\nVersion = \"9.9.9\"\n",
			),
			want: `MCP pyproject.toml: field [project].version is ambiguous or uses non-exact casing: "Version", "version"`,
		},
		{
			name: "nested Python assignment",
			path: "integrations/beads-mcp/src/beads_mcp/__init__.py",
			data: []byte("def f():\n    __version__ = \"1.1.0\"\n"),
			want: "MCP __init__.py: found 0 module-level __version__",
		},
		{
			name: "Python assignment inside multiline string",
			path: "integrations/beads-mcp/src/beads_mcp/__init__.py",
			data: []byte("payload = \"\"\"\n__version__ = \"1.1.0\"\n\"\"\"\n"),
			want: "MCP __init__.py: found 0 module-level __version__",
		},
		{
			name: "malformed JSON",
			path: "plugins/beads/.codex-plugin/plugin.json",
			data: []byte("{"),
			want: "Codex plugin.json: parse JSON:",
		},
		{
			name: "wrong-case JSON field",
			path: "npm-package/package.json",
			data: []byte(`{"Version":"1.1.0"}`),
			want: "npm package.json: field .version not found",
		},
		{
			name: "duplicate JSON field",
			path: "plugins/beads/.codex-plugin/plugin.json",
			data: []byte(`{"version":"1.1.0","version":"1.1.0"}`),
			want: "Codex plugin.json: field .version appears more than once",
		},
		{
			name: "trailing JSON document",
			path: "npm-package/package.json",
			data: []byte(`{"version":"1.1.0"} {}`),
			want: "npm package.json: parse JSON: unexpected trailing value",
		},
		{
			name: "duplicate marketplace plugins field",
			path: ".claude-plugin/marketplace.json",
			data: []byte(
				`{"plugins":[{"version":"1.1.0"}],"plugins":[{"version":"1.1.0"}]}`,
			),
			want: "Claude marketplace.json: field .plugins appears more than once",
		},
		{
			name: "missing MCP lockfile",
			path: "integrations/beads-mcp/uv.lock",
			data: nil,
			want: "MCP uv.lock (beads-mcp pin):",
		},
		{
			name: "duplicate MCP lock package",
			path: "integrations/beads-mcp/uv.lock",
			data: []byte(
				"[[package]]\nname = \"beads-mcp\"\nversion = \"1.1.0\"\n" +
					"[[package]]\nname = \"beads-mcp\"\nversion = \"1.1.0\"\n",
			),
			want: "MCP uv.lock (beads-mcp pin): found 2 beads-mcp package entries, want exactly one",
		},
		{
			name: "wrong-case MCP lock name field",
			path: "integrations/beads-mcp/uv.lock",
			data: []byte("[[package]]\nName = \"beads-mcp\"\nversion = \"1.1.0\"\n"),
			want: `MCP uv.lock (beads-mcp pin): field [[package]][0].name is ambiguous or uses non-exact casing: "Name"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeFixture(t, "1.1.0")
			path := filepath.Join(root, filepath.FromSlash(test.path))
			var err error
			if test.data == nil {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, test.data, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}

			_, err = Check(root)
			if err == nil {
				t.Fatal("invalid metadata unexpectedly passed")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestCheckValidatesUVLockVersion(t *testing.T) {
	root := writeFixture(t, "1.1.0")
	lockPath := filepath.Join(root, "integrations", "beads-mcp", "uv.lock")
	if err := os.WriteFile(
		lockPath,
		[]byte("[[package]]\nname = \"beads-mcp\"\nversion = \"9.9.9\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	_, err := Check(root)
	if err == nil {
		t.Fatal("stale MCP lock version unexpectedly passed")
	}
	if want := "MCP uv.lock (beads-mcp pin): 9.9.9 (expected 1.1.0)"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want %q", err, want)
	}

	root = writeFixture(t, "1.1.0-rc.1")
	if _, err := Check(root); err != nil {
		t.Fatalf("PEP 440-normalized release-candidate lock version failed: %v", err)
	}
}

func TestCheckUsesSemanticGoAndPythonScopes(t *testing.T) {
	root := writeFixture(t, "1.1.0")
	if err := os.WriteFile(
		filepath.Join(root, "cmd", "bd", "version.go"),
		[]byte(
			"package main\n\n"+
				"const text = `Version = \"1.1.0\"`\n"+
				"func f() { Version := \"1.1.0\"; _ = Version }\n",
		),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	_, err := Check(root)
	if err == nil {
		t.Fatal("declaration-shaped text and local Go variable unexpectedly passed")
	}
	if !strings.Contains(err.Error(), "found 0 package-level Version string assignments") {
		t.Fatalf("unexpected Go scope error: %v", err)
	}

	root = writeFixture(t, "1.1.0")
	if err := os.WriteFile(
		filepath.Join(root, "integrations", "beads-mcp", "src", "beads_mcp", "__init__.py"),
		[]byte("__version__ = '1.1.0' # release version\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(root); err != nil {
		t.Fatalf("module-level Python assignment with trailing comment failed: %v", err)
	}
}

func TestFindRootFromSubdirectory(t *testing.T) {
	root := writeFixture(t, "1.1.0")
	subdirectory := filepath.Join(root, "cmd", "bd", "nested")
	if err := os.MkdirAll(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	got, found, err := FindRoot(subdirectory)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("FindRoot = (%q, %v), want canonical root (%q, true)", got, found, want)
	}
}

func TestFindRootResolvesAliasedStart(t *testing.T) {
	root := writeFixture(t, "1.1.0")
	realStart := filepath.Join(root, "cmd", "bd")
	alias := filepath.Join(t.TempDir(), "beads-command")
	if err := os.Symlink(realStart, alias); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}

	got, found, err := FindRoot(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("aliased Beads subdirectory was not identified")
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, rootInfo) {
		t.Fatalf("FindRoot = %q, want directory identity %q", got, root)
	}
}

func TestFindRootFailsClosedForAmbiguousBeadsIdentity(t *testing.T) {
	tests := []struct {
		name  string
		goMod *string
		want  string
	}{
		{
			name: "missing go.mod",
			want: "is missing go.mod",
		},
		{
			name:  "malformed go.mod",
			goMod: stringPointer("go 1.26\n"),
			want:  "module directive not found",
		},
		{
			name: "module text inside malformed directive block",
			goMod: stringPointer(
				"require (\nmodule github.com/steveyegge/beads\n)\n",
			),
			want: "parse go.mod",
		},
		{
			name:  "conflicting module",
			goMod: stringPointer("module example.com/not-beads\n"),
			want:  `declares module "example.com/not-beads"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeFixture(t, "1.1.0")
			goModPath := filepath.Join(root, "go.mod")
			var err error
			if test.goMod == nil {
				err = os.Remove(goModPath)
			} else {
				err = os.WriteFile(goModPath, []byte(*test.goMod), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}

			_, found, err := FindRoot(filepath.Join(root, "cmd", "bd"))
			if err == nil {
				t.Fatal("ambiguous Beads identity unexpectedly passed")
			}
			if found {
				t.Fatal("ambiguous Beads identity was reported as found")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestFindRootLeavesUnrelatedModuleUnclaimed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module example.com/other\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	_, found, err := FindRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("unrelated module was identified as Beads")
	}
}

func writeFixture(t *testing.T, version string) string {
	t.Helper()

	root := t.TempDir()
	writeFixtureAt(t, root, version)
	return root
}

func writeFixtureAt(t *testing.T, root, version string) {
	t.Helper()

	files := map[string]string{
		"go.mod":                     "module " + ModulePath + "\n\ngo 1.26\n",
		"cmd/bd/version.go":          "package main\n\nvar Version = \"" + version + "\"\n",
		"scripts/update-versions.sh": "#!/bin/sh\n",
		"integrations/beads-mcp/pyproject.toml": "[project]\nversion = \"" +
			version + "\"\n",
		"integrations/beads-mcp/src/beads_mcp/__init__.py": "__version__ = \"" +
			version + "\"\n",
		"plugins/beads/.claude-plugin/plugin.json": `{"version":"` + version + `"}`,
		"plugins/beads/.codex-plugin/plugin.json":  `{"version":"` + version + `"}`,
		".claude-plugin/marketplace.json": `{"plugins":[{"version":"` +
			version + `"}]}`,
		"npm-package/package.json": `{"version":"` + version + `"}`,
		"integrations/beads-mcp/uv.lock": "version = 1\nrevision = 3\n\n" +
			"[[package]]\nname = \"beads-mcp\"\nversion = \"" +
			normalizePythonVersion(version) + "\"\n",
		".githooks/pre-push": "# --- BEGIN BEADS INTEGRATION v" + version + " ---\n" +
			"body\n# --- END BEADS INTEGRATION v" + version + " ---\n",
	}
	for relativePath, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", relativePath, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relativePath, err)
		}
	}
}

func stringPointer(value string) *string {
	return &value
}
