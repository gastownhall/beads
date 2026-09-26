package gitenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestScrubRoutingForOSUsesHostKeySemantics(t *testing.T) {
	input := []string{
		"PATH=/trusted/bin",
		"HOME=/home/test",
		"GIT_AUTHOR_NAME=Test User",
		"GIT_DIR=/wrong",
		"git_work_tree=/wrong-case",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree",
		"git_config_value_0=/wrong-case",
		"GIT_OBJECT_DIRECTORY=/wrong-objects",
		"GIT_EXEC_PATH=/wrong-exec",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=1",
	}

	for _, test := range []struct {
		name string
		goos string
		want []string
	}{
		{
			name: "POSIX names are case-sensitive",
			goos: "linux",
			want: []string{"PATH=/trusted/bin", "HOME=/home/test", "GIT_AUTHOR_NAME=Test User", "git_work_tree=/wrong-case", "git_config_value_0=/wrong-case", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=1"},
		},
		{
			name: "Windows names are case-insensitive",
			goos: "windows",
			want: []string{"PATH=/trusted/bin", "HOME=/home/test", "GIT_AUTHOR_NAME=Test User", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=1"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ScrubRoutingForOS(input, test.goos); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ScrubRoutingForOS() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestScrubRoutingPreservesConfigSuppression(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, tc := range []struct {
			entry          string
			linux, windows bool
		}{
			{"GIT_CONFIG_NOSYSTEM=1", true, true},
			{"GIT_CONFIG_NOSYSTEM=false", true, true},
			{"GIT_CONFIG_NOSYSTEM=invalid", true, true},
			{"GIT_CONFIG_NOSYSTEM", false, false},
			{"GIT_CONFIG_GLOBAL=/dev/null", true, true},
			{"GIT_CONFIG_SYSTEM=/dev/null", true, true},
			{"GIT_CONFIG_GLOBAL=nUl", false, true},
			{"git_config_system=NUL", true, true}, // Lowercase is a distinct POSIX key.
			{"GIT_CONFIG_GLOBAL=", false, false},
			{"GIT_CONFIG_SYSTEM=custom.conf", false, false},
			{"GIT_CONFIG_GLOBAL=custom.conf", false, false},
			{"GIT_CONFIG_COUNT=1", false, false},
		} {
			t.Run(goos+"/"+tc.entry, func(t *testing.T) {
				input := []string{tc.entry, "GIT_DIR=decoy", "KEEP=value"}
				want := []string{"KEEP=value"}
				if (goos == "linux" && tc.linux) || (goos == "windows" && tc.windows) {
					want = append([]string{tc.entry}, want...)
				}
				if got := ScrubRoutingForOS(input, goos); !reflect.DeepEqual(got, want) || input[0] != tc.entry {
					t.Fatalf("filtered environment = %q, want %q; input %q", got, want, input)
				}
			})
		}
	}
}

func TestScrubRoutingGitConfigSuppressionEffects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("real Git config isolation test requires Git")
	}
	for _, entry := range os.Environ() {
		key := EntryKey(entry)
		if IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	custom := filepath.Join(home, "custom.config")
	for path, value := range map[string]string{filepath.Join(home, ".gitconfig"): "home", custom: "custom"} {
		if err := os.WriteFile(path, []byte("[beads-isolation-fixture]\n\tvalue = "+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	query := func(t *testing.T, env []string, want string) {
		t.Helper()
		cmd := exec.Command("git", "config", "--get", "beads-isolation-fixture.value")
		cmd.Dir, cmd.Env = home, env
		out, err := cmd.Output()
		if want == "" {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || len(out) != 0 {
				t.Fatalf("suppressed config = %q (%v), want absence", out, err)
			}
		} else if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("config value = %q (%v), want %q", out, err, want)
		}
	}
	base := ScrubRouting(os.Environ())
	t.Run("global_null", func(t *testing.T) {
		// Pin a null system file separately so the HOME observation is owned.
		query(t, append(base, "GIT_CONFIG_SYSTEM="+os.DevNull), "home")
		query(t, append(ScrubRouting(append(base, "GIT_CONFIG_GLOBAL="+os.DevNull)), "GIT_CONFIG_SYSTEM="+os.DevNull), "")
	})
	t.Run("custom_global_still_rejected", func(t *testing.T) {
		query(t, append(base, "GIT_CONFIG_GLOBAL="+custom, "GIT_CONFIG_SYSTEM="+os.DevNull), "custom")
		query(t, append(ScrubRouting(append(base, "GIT_CONFIG_GLOBAL="+custom)), "GIT_CONFIG_SYSTEM="+os.DevNull), "home")
	})
	t.Run("nosystem", func(t *testing.T) {
		// An owned system-file override models system config after filtering;
		// only NOSYSTEM's preservation is under test in this real Git query.
		query(t, append(base, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+custom, "GIT_CONFIG_NOSYSTEM=0"), "custom")
		query(t, append(ScrubRouting(append(base, "GIT_CONFIG_NOSYSTEM=1")), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+custom), "")
	})
}

func TestRoutingUnicodeKeysFollowSubprocessIdentity(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, tc := range []struct {
			key           string
			unix, windows bool
		}{
			{"GIT_DIR", true, true},
			{"GIT_CONFIG_COUNT", true, true},
			{"git_dir", false, true},
			{"GİT_DİR", false, true},
			{"GIT_WORK_TREE", false, true},
			{"gİt_config_count", false, true},
			{"GIT_ſHALLOW_FILE", false, false},
			{"GıT_DIR", false, false},
		} {
			t.Run(goos+"/"+tc.key, func(t *testing.T) {
				input := []string{tc.key + "=value", "KEEP=first", "KEEP=second", `=C:=C:\work`}
				blocked := tc.unix
				if goos == "windows" {
					blocked = tc.windows
				}
				want := input
				if blocked {
					want = input[1:]
				}
				if got := ScrubRoutingForOS(input, goos); !reflect.DeepEqual(got, want) {
					t.Fatalf("routing environment = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestEntryKeyUsesSharedSplit(t *testing.T) {
	for _, tc := range []struct{ entry, want string }{
		{"KEEP=value=more", "KEEP"},
		{`=C:=C:\work`, "=C:"},
		{"GIT_CONFIG", "GIT_CONFIG"},
		{"", ""},
	} {
		if got := EntryKey(tc.entry); got != tc.want {
			t.Errorf("EntryKey(%q) = %q, want %q", tc.entry, got, tc.want)
		}
	}
}

func TestScrubRoutingUsesHostKeySemantics(t *testing.T) {
	input := []string{
		"GIT_DIR=canonical", "git_dir=mixed", "GİT_DİR=conservative",
		"GIT_WORK_TREE=lookup-alias", "gİt_config_count=conservative",
		"GıT_DIR=distinct", "GIT_ſHALLOW_FILE=distinct", "GIT_CONFIG",
		"KEEP=first", "KEEP=second", "KEEP=GIT_DIR=value",
		"GIT_OPTIONAL_LOCKS=1", "GIT_NO_REPLACE_OBJECTS=1", "MALFORMED", `=C:=C:\work`,
	}
	original := append([]string(nil), input...)
	want := []string{
		"GıT_DIR=distinct", "GIT_ſHALLOW_FILE=distinct",
		"KEEP=first", "KEEP=second", "KEEP=GIT_DIR=value",
		"GIT_OPTIONAL_LOCKS=1", "GIT_NO_REPLACE_OBJECTS=1", "MALFORMED", `=C:=C:\work`,
	}
	if runtime.GOOS != "windows" {
		want = append([]string{"git_dir=mixed", "GİT_DİR=conservative",
			"GIT_WORK_TREE=lookup-alias", "gİt_config_count=conservative"}, want...)
	}
	if got := ScrubRouting(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("native routing environment = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("input environment mutated: %q", input)
	}
}

func TestClearRoutingPreservesNonRoutingGitControls(t *testing.T) {
	type envEntry struct {
		key   string
		value string
	}
	var inherited []envEntry
	for _, entry := range os.Environ() {
		key := EntryKey(entry)
		if !IsRoutingKeyForOS(key, runtime.GOOS) {
			continue
		}
		value, _ := os.LookupEnv(key)
		inherited = append(inherited, envEntry{key: key, value: value})
	}
	t.Cleanup(func() {
		for _, entry := range os.Environ() {
			key := EntryKey(entry)
			if IsRoutingKeyForOS(key, runtime.GOOS) {
				if err := os.Unsetenv(key); err != nil {
					t.Errorf("unset %s during cleanup: %v", key, err)
				}
			}
		}
		for _, entry := range inherited {
			if err := os.Setenv(entry.key, entry.value); err != nil {
				t.Errorf("restore %s during cleanup: %v", entry.key, err)
			}
		}
	})

	t.Setenv("GIT_DIR", "/wrong")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "1")
	for key, value := range map[string]string{"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull} {
		t.Setenv(key, value)
	}

	removed, err := ClearRouting()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("ClearRouting() did not report removing routing entries")
	}
	for _, key := range []string{"GIT_DIR", "GIT_CONFIG_COUNT"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Fatalf("%s remains set", key)
		}
	}
	for _, key := range []string{"GIT_OPTIONAL_LOCKS", "GIT_NO_REPLACE_OBJECTS"} {
		if value := os.Getenv(key); value != "1" {
			t.Fatalf("%s = %q, want 1", key, value)
		}
	}
	for key, want := range map[string]string{"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull} {
		if got := os.Getenv(key); got != want {
			t.Errorf("config suppression %s = %q, want %q", key, got, want)
		}
	}
}
