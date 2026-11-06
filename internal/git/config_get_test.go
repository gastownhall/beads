package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// harvestConfigGetError runs the real `git config --get <key>` in a fixture
// repository and returns the error it produced, with the global and system
// scopes closed off. *exec.ExitError cannot be synthesized (its ProcessState
// is opaque), so tests that need a real exit code and stderr harvest one.
func harvestConfigGetError(t *testing.T, key, configContent string) error {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1")
	init := exec.Command("git", "init", "--quiet")
	init.Dir, init.Env = dir, env
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("fixture git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("git", "config", "--get", key)
	probe.Dir, probe.Env = dir, env
	_, err := probe.Output()
	return err
}

func TestClassifyConfigGet(t *testing.T) {
	malformed := harvestConfigGetError(t, "beads.role", "[beads\n\trole = maintainer\n")
	absent := harvestConfigGetError(t, "beads.role", "")
	invalidKey := harvestConfigGetError(t, "bad key", "")

	for _, tc := range []struct {
		name       string
		out        []byte
		err        error
		wantValue  string
		wantHave   bool
		wantErr    bool
		wantInErr  string
		wantExitAs bool // error unwraps to *exec.ExitError
	}{
		{
			name:      "success with value",
			out:       []byte("maintainer\n"),
			wantValue: "maintainer",
			wantHave:  true,
		},
		{
			name:     "success with explicitly empty value stays present",
			out:      []byte("\n"),
			wantHave: true,
		},
		{
			name:     "silent exit 1 is absence",
			err:      absent,
			wantHave: false,
		},
		{
			name:       "diagnostic exit 1 is a read failure",
			err:        invalidKey,
			wantErr:    true,
			wantInErr:  "key does not contain a section",
			wantExitAs: true,
		},
		{
			name:       "config parse failure is a read failure",
			err:        malformed,
			wantErr:    true,
			wantInErr:  "bad config",
			wantExitAs: true,
		},
		{
			name:      "execution failure passes through",
			err:       context.Canceled,
			wantErr:   true,
			wantInErr: context.Canceled.Error(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, present, readErr := ClassifyConfigGet(tc.out, tc.err)
			if tc.wantErr {
				if readErr == nil {
					t.Fatalf("ClassifyConfigGet() = %q, %v, nil; want an error", value, present)
				}
				if !strings.Contains(readErr.Error(), tc.wantInErr) {
					t.Errorf("error %q does not contain %q", readErr, tc.wantInErr)
				}
				if !errors.Is(readErr, tc.err) {
					t.Errorf("error %q does not retain the underlying error via %%w", readErr)
				}
				if tc.wantExitAs {
					var exitErr *exec.ExitError
					if !errors.As(readErr, &exitErr) {
						t.Errorf("error %q does not unwrap to *exec.ExitError", readErr)
					}
				}
				if value != "" || present {
					t.Errorf("failed read = (%q, %v); want empty and absent", value, present)
				}
				return
			}
			if readErr != nil {
				t.Fatalf("ClassifyConfigGet() error = %v; want nil", readErr)
			}
			if value != tc.wantValue || present != tc.wantHave {
				t.Errorf("ClassifyConfigGet() = (%q, %v); want (%q, %v)", value, present, tc.wantValue, tc.wantHave)
			}
		})
	}
}
