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
