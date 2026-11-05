package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
)

// gtRootDotenvFixture is a workspace whose .beads/.env names a GT_ROOT
// elsewhere on disk, with a JSON formula only that GT_ROOT holds.
type gtRootDotenvFixture struct {
	workspace  string // checkout root; cwd for the test
	cityRoot   string // value of GT_ROOT
	cityDir    string // $GT_ROOT/.beads/formulas
	cityJSON   string // JSON formula under cityDir
	cityTOML   string // where a conversion of cityJSON would land
	projectDir string // <workspace>/.beads/formulas
}

const gtRootDotenvFormulaJSON = `{
  "formula": "%s",
  "description": "fixture",
  "version": 1,
  "steps": [{"id": "s1", "title": "Step 1"}]
}
`

func writeGTRootDotenvJSONFormula(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".formula.json")
	if err := os.WriteFile(path, []byte(strings.Replace(gtRootDotenvFormulaJSON, "%s", name, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resetGTRootDotenvCaches() {
	beads.ResetCaches()
	git.ResetCaches()
}

// newGTRootDotenvFixture builds the fixture, isolates HOME/BEADS_DIR/cwd, and
// clears GT_ROOT and its provenance marker. It does not load the .env; tests
// choose whether GT_ROOT comes from the shell or the file.
func newGTRootDotenvFixture(t *testing.T) *gtRootDotenvFixture {
	t.Helper()
	root := t.TempDir()
	f := &gtRootDotenvFixture{
		workspace: filepath.Join(root, "repo"),
		cityRoot:  filepath.Join(root, "elsewhere"),
	}
	f.cityDir = filepath.Join(f.cityRoot, ".beads", "formulas")
	f.projectDir = filepath.Join(f.workspace, ".beads", "formulas")
	f.cityJSON = writeGTRootDotenvJSONFormula(t, f.cityDir, "cityonly")
	f.cityTOML = filepath.Join(f.cityDir, "cityonly.formula.toml")
	if err := os.MkdirAll(filepath.Join(f.workspace, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspace, ".beads", ".env"), []byte("GT_ROOT="+f.cityRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("GT_ROOT", "")
	os.Unsetenv("GT_ROOT")
	t.Setenv(config.GTRootFromProjectEnvMarker, "")
	os.Unsetenv(config.GTRootFromProjectEnvMarker)
	t.Chdir(f.workspace)
	resetGTRootDotenvCaches()
	t.Cleanup(resetGTRootDotenvCaches)
	return f
}

func (f *gtRootDotenvFixture) loadEnv(t *testing.T) {
	t.Helper()
	loadBeadsEnvFile(filepath.Join(f.workspace, ".beads"))
	if got := os.Getenv("GT_ROOT"); got != f.cityRoot {
		t.Fatalf("GT_ROOT = %q, want %q", got, f.cityRoot)
	}
}

func setFormulaConvertFlags(t *testing.T, all, del, stdout bool) {
	t.Helper()
	oldAll, oldDel, oldStdout := convertAll, convertDelete, convertStdout
	convertAll, convertDelete, convertStdout = all, del, stdout
	t.Cleanup(func() { convertAll, convertDelete, convertStdout = oldAll, oldDel, oldStdout })
}

// captureFormulaOutput runs fn with stdout and stderr redirected and returns both.
func captureFormulaOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var outBuf, errBuf bytes.Buffer
	outDone, errDone := make(chan struct{}), make(chan struct{})
	go func() { _, _ = io.Copy(&outBuf, outR); close(outDone) }()
	go func() { _, _ = io.Copy(&errBuf, errR); close(errDone) }()
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		_ = outW.Close()
		_ = errW.Close()
		os.Stdout, os.Stderr = oldOut, oldErr
		<-outDone
		<-errDone
		_ = outR.Close()
		_ = errR.Close()
		stdout, stderr = outBuf.String(), errBuf.String()
	}()
	fn()
	return
}

func assertExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if got := err == nil; got != want {
		t.Errorf("%s exists = %v, want %v (stat err: %v)", path, got, want, err)
	}
}

// TestFormulaConvertAllSkipsGTRootFromDotenv reproduces the report on #8122:
// with GT_ROOT set only in the repo's .beads/.env, `bd formula convert --all
// --delete` converted and deleted a formula in a directory outside the repo.
// It must now leave that directory untouched and say why, while still
// converting the project's own formulas.
func TestFormulaConvertAllSkipsGTRootFromDotenv(t *testing.T) {
	f := newGTRootDotenvFixture(t)
	projectJSON := writeGTRootDotenvJSONFormula(t, f.projectDir, "local")
	f.loadEnv(t)
	if !config.GTRootFromProjectEnv() {
		t.Fatal("GTRootFromProjectEnv() = false after importing GT_ROOT from .beads/.env")
	}
	setFormulaConvertFlags(t, true, true, false)

	_, stderr := captureFormulaOutput(t, convertAllFormulas)

	assertExists(t, f.cityJSON, true)
	assertExists(t, f.cityTOML, false)
	if want := f.cityDir + " is under GT_ROOT from .beads/.env"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	// The project's own formula directory is still converted.
	assertExists(t, projectJSON, false)
	assertExists(t, filepath.Join(f.projectDir, "local.formula.toml"), true)
}

// TestFormulaConvertAllWritesShellExportedGTRoot pins that a GT_ROOT the
// caller exports keeps today's behavior: --all converts there.
func TestFormulaConvertAllWritesShellExportedGTRoot(t *testing.T) {
	f := newGTRootDotenvFixture(t)
	t.Setenv("GT_ROOT", f.cityRoot)
	f.loadEnv(t)
	if config.GTRootFromProjectEnv() {
		t.Fatal("GTRootFromProjectEnv() = true for a shell-exported GT_ROOT")
	}
	setFormulaConvertFlags(t, true, true, false)

	_, stderr := captureFormulaOutput(t, convertAllFormulas)

	assertExists(t, f.cityJSON, false)
	assertExists(t, f.cityTOML, true)
	if strings.Contains(stderr, "GT_ROOT from .beads/.env") {
		t.Errorf("unexpected skip note for shell GT_ROOT: %q", stderr)
	}
}

// TestFormulaConvertAllInheritedDotenvGTRoot pins that a bd started by a bd
// that imported GT_ROOT from .beads/.env (so GT_ROOT arrives already exported,
// with the marker) still treats the directory as read-only.
func TestFormulaConvertAllInheritedDotenvGTRoot(t *testing.T) {
	f := newGTRootDotenvFixture(t)
	t.Setenv("GT_ROOT", f.cityRoot)
	t.Setenv(config.GTRootFromProjectEnvMarker, f.cityRoot)
	f.loadEnv(t)
	setFormulaConvertFlags(t, true, true, false)

	captureFormulaOutput(t, convertAllFormulas)

	assertExists(t, f.cityJSON, true)
	assertExists(t, f.cityTOML, false)
}

// TestFormulaConvertByNameRefusesGTRootFromDotenv covers a single named
// formula found only under a .env-provided GT_ROOT: convert refuses (and
// --delete removes nothing), while --stdout, which writes no file, still works.
func TestFormulaConvertByNameRefusesGTRootFromDotenv(t *testing.T) {
	f := newGTRootDotenvFixture(t)
	f.loadEnv(t)
	setFormulaConvertFlags(t, false, true, false)

	var runErr error
	_, stderr := captureFormulaOutput(t, func() { runErr = runFormulaConvert(formulaConvertCmd, []string{"cityonly"}) })
	if runErr == nil {
		t.Fatal("runFormulaConvert succeeded; want refusal")
	}
	assertExists(t, f.cityJSON, true)
	assertExists(t, f.cityTOML, false)
	if want := "not converting " + f.cityJSON + ": " + f.cityDir + " is under GT_ROOT from .beads/.env"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}

	setFormulaConvertFlags(t, false, false, true)
	stdout, _ := captureFormulaOutput(t, func() { runErr = runFormulaConvert(formulaConvertCmd, []string{"cityonly"}) })
	if runErr != nil {
		t.Fatalf("--stdout: %v", runErr)
	}
	if !strings.Contains(stdout, `formula = "cityonly"`) {
		t.Errorf("--stdout output = %q, want the TOML", stdout)
	}
	assertExists(t, f.cityTOML, false)

	// Exported in the shell, the same command converts in place.
	t.Setenv(config.GTRootFromProjectEnvMarker, "")
	setFormulaConvertFlags(t, false, false, false)
	captureFormulaOutput(t, func() { runErr = runFormulaConvert(formulaConvertCmd, []string{"cityonly"}) })
	if runErr != nil {
		t.Fatalf("shell GT_ROOT convert: %v", runErr)
	}
	assertExists(t, f.cityTOML, true)
}

// TestFormulaConvertByNameRefusesNestedGTRootFromDotenv pins that the refusal
// keys on the search path a name resolved under, not on the file's parent
// directory: a name with a subdirectory or ".." in it still resolves through
// $GT_ROOT/.beads/formulas, so it must not convert or delete anything there.
func TestFormulaConvertByNameRefusesNestedGTRootFromDotenv(t *testing.T) {
	f := newGTRootDotenvFixture(t)
	nestedJSON := writeGTRootDotenvJSONFormula(t, filepath.Join(f.cityDir, "sub"), "nested")
	besideJSON := writeGTRootDotenvJSONFormula(t, f.cityRoot, "beside")
	f.loadEnv(t)
	setFormulaConvertFlags(t, false, true, false)

	cases := []struct{ name, json string }{
		{filepath.Join("sub", "nested"), nestedJSON},
		{filepath.Join("..", "..", "beside"), besideJSON},
	}
	for _, tc := range cases {
		var runErr error
		_, stderr := captureFormulaOutput(t, func() { runErr = runFormulaConvert(formulaConvertCmd, []string{tc.name}) })
		if runErr == nil {
			t.Errorf("convert %q succeeded; want refusal", tc.name)
		}
		assertExists(t, tc.json, true)
		assertExists(t, strings.TrimSuffix(tc.json, ".formula.json")+".formula.toml", false)
		if want := f.cityDir + " is under GT_ROOT from .beads/.env"; !strings.Contains(stderr, want) {
			t.Errorf("convert %q: stderr = %q, want it to contain %q", tc.name, stderr, want)
		}
	}

	// Exported in the shell, the same names convert in place.
	t.Setenv(config.GTRootFromProjectEnvMarker, "")
	setFormulaConvertFlags(t, false, false, false)
	for _, tc := range cases {
		var runErr error
		captureFormulaOutput(t, func() { runErr = runFormulaConvert(formulaConvertCmd, []string{tc.name}) })
		if runErr != nil {
			t.Errorf("shell GT_ROOT convert %q: %v", tc.name, runErr)
		}
		assertExists(t, strings.TrimSuffix(tc.json, ".formula.json")+".formula.toml", true)
	}
}

// TestFindWritableFormulaDirSkipsGTRootFromDotenv covers mol distill's
// destination choice: with every earlier search path unwritable, it must not
// fall through to (or probe) a .env-provided GT_ROOT; a shell-exported one is
// still used.
func TestFindWritableFormulaDirSkipsGTRootFromDotenv(t *testing.T) {
	f := newGTRootDotenvFixture(t)
	// Block the checkout and user formula directories with plain files so
	// MkdirAll fails regardless of the uid running the test.
	if err := os.WriteFile(f.projectDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	homeBeads := filepath.Join(os.Getenv("HOME"), ".beads")
	if err := os.MkdirAll(homeBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeBeads, "formulas"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.loadEnv(t)

	if got := findWritableFormulaDir("distilled"); got != "" {
		t.Errorf("findWritableFormulaDir = %q, want \"\" (GT_ROOT from .beads/.env is read-only)", got)
	}
	assertExists(t, filepath.Join(f.cityDir, ".write-test"), false)

	t.Setenv(config.GTRootFromProjectEnvMarker, "")
	want := filepath.Join(f.cityDir, "distilled.formula.json")
	if got := findWritableFormulaDir("distilled"); got != want {
		t.Errorf("shell GT_ROOT: findWritableFormulaDir = %q, want %q", got, want)
	}
}

// TestFindWritableFormulaDirPrefersProjectWithDotenvGTRoot pins that a
// writable project directory is still chosen first, unchanged.
func TestFindWritableFormulaDirPrefersProjectWithDotenvGTRoot(t *testing.T) {
	f := newGTRootDotenvFixture(t)
	f.loadEnv(t)
	want := filepath.Join(f.projectDir, "distilled.formula.json")
	if got := findWritableFormulaDir("distilled"); got != want {
		t.Errorf("findWritableFormulaDir = %q, want %q", got, want)
	}
}
