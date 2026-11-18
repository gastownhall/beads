package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// macFixture is a temp home laid out like a Mac: the legacy Application
// Support files and the documented ~/.config targets.
type macFixture struct {
	home         string
	legacyConfig string
	legacyPrime  string
	documented   string // ~/.config/bd/config.yaml
	primeTarget  string // ~/.config/beads/PRIME.md
	statCalls    []string
	statErrs     map[string]error
}

func newMacFixture(t *testing.T) *macFixture {
	t.Helper()
	home := t.TempDir()
	return &macFixture{
		home:         home,
		legacyConfig: filepath.Join(home, "Library", "Application Support", "bd", "config.yaml"),
		legacyPrime:  filepath.Join(home, "Library", "Application Support", "beads", "PRIME.md"),
		documented:   filepath.Join(home, ".config", "bd", "config.yaml"),
		primeTarget:  filepath.Join(home, ".config", "beads", "PRIME.md"),
		statErrs:     map[string]error{},
	}
}

func (f *macFixture) stat(path string) (fs.FileInfo, error) {
	f.statCalls = append(f.statCalls, path)
	if err, ok := f.statErrs[path]; ok {
		return nil, err
	}
	return os.Stat(path)
}

func (f *macFixture) inputs() MacOSLegacyUserConfigInputs {
	return MacOSLegacyUserConfigInputs{
		GOOS:              "darwin",
		Home:              f.home,
		ActiveConfigPaths: []string{filepath.Join(f.home, ".beads", "config.yaml"), f.documented},
		ConfigTarget:      f.documented,
		PrimeTarget:       f.primeTarget,
		MetricsEnabled:    true,
		NodeID:            "",
		Stat:              f.stat,
	}
}

func writeFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runMacCheck(t *testing.T, in MacOSLegacyUserConfigInputs) DoctorCheck {
	t.Helper()
	check, ok := CheckMacOSLegacyUserConfig(in)
	if !ok {
		t.Fatal("check not applicable on darwin")
	}
	if check.Name != MacOSLegacyUserConfigCheckName || check.Category != CategoryData {
		t.Fatalf("identity = %q / %q", check.Name, check.Category)
	}
	if check.Status == StatusError {
		t.Fatalf("status error; the check is warning-class: %+v", check)
	}
	assertMacFixAdvice(t, check)
	return check
}

// assertMacFixAdvice: every Fix line is pasteable advice with no backtick, no
// bd doctor --fix, no rm, and Application Support paths single-quoted.
func assertMacFixAdvice(t *testing.T, check DoctorCheck) {
	t.Helper()
	for _, line := range strings.Split(check.Fix, "\n") {
		if strings.Contains(line, "`") || strings.Contains(line, "bd doctor --fix") || strings.Contains(line, "rm ") {
			t.Errorf("fix line %q has a backtick, bd doctor --fix or rm", line)
		}
		if strings.Contains(line, "Application Support") && !strings.Contains(line, "'") {
			t.Errorf("fix line %q names Application Support unquoted", line)
		}
	}
}

func TestCheckMacOSLegacyUserConfigNotApplicableOffDarwin(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "freebsd"} {
		f := newMacFixture(t)
		in := f.inputs()
		in.GOOS = goos
		if check, ok := CheckMacOSLegacyUserConfig(in); ok {
			t.Errorf("%s: check applies: %+v", goos, check)
		}
		if len(f.statCalls) != 0 {
			t.Errorf("%s: stat called %q", goos, f.statCalls)
		}
	}
	f := newMacFixture(t)
	in := f.inputs()
	in.GOOS = "ios"
	if _, ok := CheckMacOSLegacyUserConfig(in); !ok {
		t.Error("ios: check not applicable")
	}
}

func TestCheckMacOSLegacyUserConfigHomeUnresolvable(t *testing.T) {
	f := newMacFixture(t)
	in := f.inputs()
	in.HomeErr = errors.New("$HOME is not defined")
	check := runMacCheck(t, in)
	if check.Status != StatusOK || check.Message != "Not checked: $HOME is not defined" {
		t.Fatalf("got %+v", check)
	}
	in = f.inputs()
	in.Home = "relative"
	if check := runMacCheck(t, in); check.Status != StatusOK || !strings.HasPrefix(check.Message, "Not checked: ") {
		t.Fatalf("relative home: %+v", check)
	}
	if len(f.statCalls) != 0 {
		t.Fatalf("stat called without a home: %q", f.statCalls)
	}
}

func TestCheckMacOSLegacyUserConfigAbsent(t *testing.T) {
	f := newMacFixture(t)
	check := runMacCheck(t, f.inputs())
	if check.Status != StatusOK || check.Message != "No legacy bd files in ~/Library/Application Support" || check.Fix != "" {
		t.Fatalf("got %+v", check)
	}
}

func TestCheckMacOSLegacyUserConfigPresentTargetAbsent(t *testing.T) {
	f := newMacFixture(t)
	writeFixtureFile(t, f.legacyConfig, "actor: alice\n")
	check := runMacCheck(t, f.inputs())
	if check.Status != StatusWarning || check.Message != "bd no longer reads ~/Library/Application Support/bd/config.yaml" {
		t.Fatalf("got %+v", check)
	}
	if check.Detail != "Settings there that differ from your current user config: actor" {
		t.Errorf("detail = %q", check.Detail)
	}
	want := "Move it to where bd reads it: mkdir -p " + filepath.Dir(f.documented) + " && mv '" + f.legacyConfig + "' " + f.documented
	if check.Fix != want {
		t.Errorf("fix = %q\nwant  %q", check.Fix, want)
	}
}

func TestCheckMacOSLegacyUserConfigPresentTargetExists(t *testing.T) {
	f := newMacFixture(t)
	writeFixtureFile(t, f.legacyConfig, "actor: alice\nmetrics:\n  disabled: false\n  endpoint: https://custom.example.com\n")
	writeFixtureFile(t, f.documented, "metrics:\n  disabled: false\n  endpoint: https://default.example.com\n")
	check := runMacCheck(t, f.inputs())
	if check.Detail != "Settings there that differ from your current user config: actor, metrics.endpoint" {
		t.Errorf("detail = %q", check.Detail)
	}
	if strings.Contains(check.Detail, "custom.example.com") || strings.Contains(check.Detail, "alice") {
		t.Errorf("detail leaks values: %q", check.Detail)
	}
	want := "Copy the settings you still want into " + f.documented + ", then retire the old file: mv '" +
		f.legacyConfig + "' '" + f.legacyConfig + ".bak'"
	if check.Fix != want {
		t.Errorf("fix = %q\nwant  %q", check.Fix, want)
	}

	t.Run("every setting matches", func(t *testing.T) {
		f := newMacFixture(t)
		// Quoted and unquoted scalars are the same setting to bd.
		writeFixtureFile(t, f.legacyConfig, "metrics:\n  disabled: \"false\"\nretries: 5\n")
		writeFixtureFile(t, f.documented, "metrics:\n  disabled: false\nretries: \"5\"\n")
		if check := runMacCheck(t, f.inputs()); check.Detail != "Every setting there already matches your current user config" {
			t.Errorf("detail = %q", check.Detail)
		}
	})
}

func TestCheckMacOSLegacyUserConfigKeyCap(t *testing.T) {
	f := newMacFixture(t)
	var body strings.Builder
	for i := 0; i < 11; i++ {
		fmt.Fprintf(&body, "key%02d: v\n", i)
	}
	writeFixtureFile(t, f.legacyConfig, body.String())
	check := runMacCheck(t, f.inputs())
	want := "Settings there that differ from your current user config: key00, key01, key02, key03, key04, key05, key06, key07, and 3 more"
	if check.Detail != want {
		t.Errorf("detail = %q", check.Detail)
	}
}

func TestCheckMacOSLegacyUserConfigMetricsLine(t *testing.T) {
	const line = "If you had turned metrics off, run: bd metrics off"
	for _, tc := range []struct {
		name    string
		legacy  string
		enabled bool
		want    bool
	}{
		{name: "opt-out and metrics enabled", legacy: "metrics:\n  disabled: true\n", enabled: true, want: true},
		{name: "flat opt-out", legacy: "metrics.disabled: \"TRUE\"\n", enabled: true, want: true},
		{name: "opt-out but metrics already off", legacy: "metrics:\n  disabled: true\n"},
		{name: "no opt-out", legacy: "metrics:\n  disabled: false\n", enabled: true},
		{name: "unparseable", legacy: "metrics:\n  disabled: maybe\n", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMacFixture(t)
			writeFixtureFile(t, f.legacyConfig, tc.legacy)
			in := f.inputs()
			in.MetricsEnabled = tc.enabled
			check := runMacCheck(t, in)
			got := strings.HasPrefix(check.Fix, line+"\n")
			if got != tc.want {
				t.Fatalf("metrics line shown = %v, want %v; fix %q", got, tc.want, check.Fix)
			}
		})
	}
}

func TestCheckMacOSLegacyUserConfigNodeIDLine(t *testing.T) {
	for _, tc := range []struct {
		name, legacy, local, want string
	}{
		{name: "local empty", legacy: "node_id: mac-1\n", want: "Set this machine's node_id again: bd config set node_id mac-1"},
		{name: "local set", legacy: "node_id: mac-1\n", local: "mac-2"},
		{name: "unsafe value", legacy: "node_id: \"mac $(x)\\u001b\"\n",
			want: "Set this machine's node_id again: bd config set node_id <node_id>  (<node_id>: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMacFixture(t)
			writeFixtureFile(t, f.legacyConfig, tc.legacy)
			in := f.inputs()
			in.NodeID = tc.local
			check := runMacCheck(t, in)
			first := strings.SplitN(check.Fix, "\n", 2)[0]
			if tc.want == "" {
				if strings.Contains(check.Fix, "node_id") {
					t.Fatalf("node_id line shown with a local node_id: %q", check.Fix)
				}
				return
			}
			if !strings.HasPrefix(first, tc.want) {
				t.Fatalf("first fix line = %q, want prefix %q", first, tc.want)
			}
			if strings.ContainsRune(first, '\x1b') {
				t.Fatalf("unsafe value pasted raw: %q", first)
			}
		})
	}
}

func TestCheckMacOSLegacyUserConfigUnreadable(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		f := newMacFixture(t)
		if err := os.MkdirAll(f.legacyConfig, 0o755); err != nil {
			t.Fatal(err)
		}
		check := runMacCheck(t, f.inputs())
		if check.Status != StatusWarning || check.Detail != "Could not list its settings: not a regular file" {
			t.Fatalf("got %+v", check)
		}
	})
	t.Run("invalid yaml", func(t *testing.T) {
		f := newMacFixture(t)
		writeFixtureFile(t, f.legacyConfig, "metrics: [unclosed\n")
		check := runMacCheck(t, f.inputs())
		if check.Status != StatusWarning || !strings.HasPrefix(check.Detail, "Could not list its settings: ") {
			t.Fatalf("got %+v", check)
		}
		if strings.Contains(check.Detail, "\n") {
			t.Fatalf("detail spans lines: %q", check.Detail)
		}
	})
	t.Run("EPERM", func(t *testing.T) {
		f := newMacFixture(t)
		writeFixtureFile(t, f.legacyConfig, "actor: alice\n")
		f.statErrs[f.legacyConfig] = &fs.PathError{Op: "stat", Path: f.legacyConfig, Err: fs.ErrPermission}
		check := runMacCheck(t, f.inputs())
		if check.Status != StatusWarning || check.Message != "Could not check ~/Library/Application Support/bd/config.yaml" {
			t.Fatalf("got %+v", check)
		}
		if check.Detail != "permission denied; bd no longer reads this file, so check it yourself if you kept settings there" {
			t.Errorf("detail = %q", check.Detail)
		}
		if check.Fix != "If you kept settings there, copy them into "+f.documented {
			t.Errorf("fix = %q", check.Fix)
		}
	})
}

func TestCheckMacOSLegacyUserConfigPrime(t *testing.T) {
	t.Run("target absent", func(t *testing.T) {
		f := newMacFixture(t)
		writeFixtureFile(t, f.legacyPrime, "# custom\n")
		check := runMacCheck(t, f.inputs())
		if check.Message != "bd no longer reads ~/Library/Application Support/beads/PRIME.md" ||
			check.Detail != "Where a workspace has no PRIME.md of its own, bd prime uses its built-in template until "+f.primeTarget+" exists" {
			t.Fatalf("got %+v", check)
		}
		want := "Move it to where bd reads it: mkdir -p " + filepath.Dir(f.primeTarget) + " && mv '" + f.legacyPrime + "' " + f.primeTarget
		if check.Fix != want {
			t.Errorf("fix = %q\nwant  %q", check.Fix, want)
		}
	})
	t.Run("target exists", func(t *testing.T) {
		f := newMacFixture(t)
		writeFixtureFile(t, f.legacyPrime, "# custom\n")
		writeFixtureFile(t, f.primeTarget, "# current\n")
		check := runMacCheck(t, f.inputs())
		if check.Detail != "Where a workspace has no PRIME.md of its own, bd prime uses "+f.primeTarget+" instead" {
			t.Fatalf("detail = %q", check.Detail)
		}
		want := "Merge anything you still want into " + f.primeTarget + ", then retire the old file: mv '" +
			f.legacyPrime + "' '" + f.legacyPrime + ".bak'"
		if check.Fix != want {
			t.Errorf("fix = %q\nwant  %q", check.Fix, want)
		}
	})
	t.Run("EPERM", func(t *testing.T) {
		f := newMacFixture(t)
		f.statErrs[f.legacyPrime] = fs.ErrPermission
		check := runMacCheck(t, f.inputs())
		if check.Status != StatusWarning || check.Message != "Could not check ~/Library/Application Support/beads/PRIME.md" {
			t.Fatalf("got %+v", check)
		}
	})
}

func TestCheckMacOSLegacyUserConfigBothFiles(t *testing.T) {
	f := newMacFixture(t)
	writeFixtureFile(t, f.legacyConfig, "metrics:\n  disabled: true\n")
	writeFixtureFile(t, f.legacyPrime, "# custom\n")
	check := runMacCheck(t, f.inputs())
	if check.Message != "bd no longer reads 2 files in ~/Library/Application Support" {
		t.Fatalf("message = %q", check.Message)
	}
	if !strings.HasPrefix(check.Detail, "bd/config.yaml: Settings there that differ") ||
		!strings.Contains(check.Detail, "; beads/PRIME.md: Where a workspace has no PRIME.md of its own, bd prime uses its built-in template") {
		t.Errorf("detail = %q", check.Detail)
	}
	lines := strings.Split(check.Fix, "\n")
	if len(lines) != 3 || lines[0] != "If you had turned metrics off, run: bd metrics off" ||
		!strings.Contains(lines[1], "bd/config.yaml") || !strings.Contains(lines[2], "PRIME.md") {
		t.Errorf("fix lines = %q", lines)
	}

	t.Run("both stat errors", func(t *testing.T) {
		f := newMacFixture(t)
		f.statErrs[f.legacyConfig] = fs.ErrPermission
		f.statErrs[f.legacyPrime] = fs.ErrPermission
		if check := runMacCheck(t, f.inputs()); check.Message != "Could not check 2 files in ~/Library/Application Support" {
			t.Fatalf("message = %q", check.Message)
		}
	})
}

func TestCheckMacOSLegacyUserConfigSkipsFilesBdReads(t *testing.T) {
	t.Run("XDG_CONFIG_HOME is Application Support", func(t *testing.T) {
		f := newMacFixture(t)
		writeFixtureFile(t, f.legacyConfig, "actor: alice\n")
		writeFixtureFile(t, f.legacyPrime, "# custom\n")
		in := f.inputs()
		in.ActiveConfigPaths = append(in.ActiveConfigPaths, f.legacyConfig)
		in.ConfigTarget = f.legacyConfig
		in.PrimeTarget = f.legacyPrime
		check := runMacCheck(t, in)
		if check.Status != StatusOK {
			t.Fatalf("got %+v", check)
		}
		for _, p := range f.statCalls {
			if p == f.legacyConfig || p == f.legacyPrime {
				t.Errorf("stat of a file bd reads: %q", p)
			}
		}
	})
	t.Run("symlinked to the documented file", func(t *testing.T) {
		f := newMacFixture(t)
		writeFixtureFile(t, f.documented, "actor: alice\n")
		if err := os.MkdirAll(filepath.Dir(f.legacyConfig), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(f.documented, f.legacyConfig); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if check := runMacCheck(t, f.inputs()); check.Status != StatusOK {
			t.Fatalf("got %+v", check)
		}
	})
}
