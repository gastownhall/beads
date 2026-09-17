package gitenv

import (
	"os"
	"reflect"
	"runtime"
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
}
