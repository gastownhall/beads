package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCursorHooksFile(t *testing.T, repoDir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repoDir, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".cursor", "hooks.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCursor_HooksInstalled(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // isolate any real ~/.cursor
	t.Setenv("USERPROFILE", t.TempDir())
	writeCursorHooksFile(t, repo, `{"version":1,"hooks":{"sessionStart":[{"command":"bd cursor-hook sessionStart"}]}}`)

	dc := CheckCursor(repo)
	if dc.Status != StatusOK {
		t.Fatalf("status = %q, want ok (%#v)", dc.Status, dc)
	}
	if dc.Message != "Hooks installed" {
		t.Errorf("message = %q, want %q", dc.Message, "Hooks installed")
	}
}

func TestCheckCursor_PresentButNoHooks(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // ensure no global bd hooks
	t.Setenv("USERPROFILE", t.TempDir())

	// A .cursor/ dir signals Cursor is in use, but no bd hooks are installed.
	if err := os.MkdirAll(filepath.Join(repo, ".cursor", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}

	dc := CheckCursor(repo)
	if dc.Status != StatusWarning {
		t.Fatalf("status = %q, want warning (%#v)", dc.Status, dc)
	}
}

func TestCheckCursorSettingsHealth(t *testing.T) {
	t.Run("malformed is error", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())
		writeCursorHooksFile(t, repo, `{not valid json`)

		dc := CheckCursorSettingsHealth(repo)
		if dc.Status != StatusError {
			t.Fatalf("status = %q, want error (%#v)", dc.Status, dc)
		}
	})

	t.Run("valid is ok", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())
		writeCursorHooksFile(t, repo, `{"version":1,"hooks":{"sessionStart":[{"command":"bd cursor-hook sessionStart"}]}}`)

		dc := CheckCursorSettingsHealth(repo)
		if dc.Status != StatusOK {
			t.Fatalf("status = %q, want ok (%#v)", dc.Status, dc)
		}
	})

	t.Run("absent is ok", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())

		dc := CheckCursorSettingsHealth(repo)
		if dc.Status != StatusOK {
			t.Fatalf("status = %q, want ok (%#v)", dc.Status, dc)
		}
	})
}

func TestCheckCursorHookCompleteness(t *testing.T) {
	t.Run("all events present is ok", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())
		writeCursorHooksFile(t, repo, `{"version":1,"hooks":{
			"sessionStart":[{"command":"bd cursor-hook sessionStart"}],
			"preCompact":[{"command":"bd cursor-hook preCompact"}],
			"postToolUse":[{"command":"bd cursor-hook postToolUse"}]
		}}`)

		dc := CheckCursorHookCompleteness(repo)
		if dc.Status != StatusOK {
			t.Fatalf("status = %q, want ok (%#v)", dc.Status, dc)
		}
	})

	t.Run("partial install warns", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())
		// sessionStart only — recovery hooks missing.
		writeCursorHooksFile(t, repo, `{"version":1,"hooks":{"sessionStart":[{"command":"bd cursor-hook sessionStart"}]}}`)

		dc := CheckCursorHookCompleteness(repo)
		if dc.Status != StatusWarning {
			t.Fatalf("status = %q, want warning (%#v)", dc.Status, dc)
		}
	})

	t.Run("no hooks is N/A ok", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())

		dc := CheckCursorHookCompleteness(repo)
		if dc.Status != StatusOK {
			t.Fatalf("status = %q, want ok (%#v)", dc.Status, dc)
		}
	})
}

func TestHasBeadsCursorHooks(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, "hooks.json")

	cases := []struct {
		name string
		body string
		want bool
	}{
		{"managed sessionStart", `{"hooks":{"sessionStart":[{"command":"bd cursor-hook sessionStart"}]}}`, true},
		{"managed postToolUse", `{"hooks":{"postToolUse":[{"command":"bd cursor-hook postToolUse"}]}}`, true},
		{"user hook only", `{"hooks":{"afterFileEdit":[{"command":"./format.sh"}]}}`, false},
		{"empty", `{"version":1}`, false},
		{"malformed", `{not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(hooks, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := hasBeadsCursorHooks(hooks); got != tc.want {
				t.Errorf("hasBeadsCursorHooks = %v, want %v", got, tc.want)
			}
		})
	}
}
