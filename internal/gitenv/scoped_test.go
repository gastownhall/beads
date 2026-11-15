package gitenv

import (
	"os"
	"testing"
)

func TestWithRoutingScrubbedRemovesRoutingEnvInsideAndRestoresAfter(t *testing.T) {
	t.Setenv("GIT_DIR", "/caller/repo/.git")
	t.Setenv("GIT_WORK_TREE", "/caller/repo")
	t.Setenv("GIT_INDEX_FILE", "/caller/repo/.git/index")

	inside := map[string]string{}
	if err := WithRoutingScrubbed(func() error {
		for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
			if value, ok := os.LookupEnv(key); ok {
				inside[key] = value
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("WithRoutingScrubbed: %v", err)
	}

	if len(inside) != 0 {
		t.Errorf("routing keys visible inside the guard: %v", inside)
	}
	for key, want := range map[string]string{
		"GIT_DIR":        "/caller/repo/.git",
		"GIT_WORK_TREE":  "/caller/repo",
		"GIT_INDEX_FILE": "/caller/repo/.git/index",
	} {
		if got, ok := os.LookupEnv(key); !ok || got != want {
			t.Errorf("%s = %q (present=%t), want restored %q", key, got, ok, want)
		}
	}
}

func TestWithRoutingScrubbedKeepsAbsentKeysAbsentAndSuppressionPresent(t *testing.T) {
	t.Setenv("GIT_DIR", "/caller/repo/.git")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Cleanup(func() { os.Unsetenv("GIT_WORK_TREE") })

	if err := WithRoutingScrubbed(func() error {
		if _, ok := os.LookupEnv("GIT_WORK_TREE"); ok {
			t.Errorf("GIT_WORK_TREE present inside the guard, want absent")
		}
		if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
			t.Errorf("GIT_CONFIG_NOSYSTEM = %q inside the guard, want preserved", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithRoutingScrubbed: %v", err)
	}

	if _, ok := os.LookupEnv("GIT_WORK_TREE"); ok {
		t.Errorf("GIT_WORK_TREE appeared after the guard, want still absent")
	}
	if got := os.Getenv("GIT_DIR"); got != "/caller/repo/.git" {
		t.Errorf("GIT_DIR = %q after the guard, want restored", got)
	}
}

func TestWithRoutingScrubbedNestsRestoringExactlyOnce(t *testing.T) {
	t.Setenv("GIT_DIR", "/outer/.git")

	innerErr := WithRoutingScrubbed(func() error {
		return WithRoutingScrubbed(func() error {
			if _, ok := os.LookupEnv("GIT_DIR"); ok {
				t.Errorf("GIT_DIR visible inside the nested guard, want absent")
			}
			return nil
		})
	})
	if innerErr != nil {
		t.Fatalf("nested WithRoutingScrubbed: %v", innerErr)
	}
	if got := os.Getenv("GIT_DIR"); got != "/outer/.git" {
		t.Errorf("GIT_DIR = %q after the nested guard, want restored exactly once", got)
	}
}

func TestWithRoutingScrubbedPropagatesFnError(t *testing.T) {
	wantErr := os.ErrPermission
	if err := WithRoutingScrubbed(func() error { return wantErr }); err != wantErr {
		t.Errorf("WithRoutingScrubbed error = %v, want the fn error %v unchanged", err, wantErr)
	}
}
