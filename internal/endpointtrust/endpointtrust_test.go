package endpointtrust

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatePins(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func TestOriginNormalizesOrigins(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"https://Example.com/path", "https://example.com:443"},
		{"https://example.com:443/other", "https://example.com:443"},
		{"http://[::1]/path", "http://[::1]:80"},
		{"mysql://[2001:DB8::1]:3306/db", "mysql://[2001:db8::1]:3306"},
		{"unix:/tmp/dolt.sock", "unix:/tmp/dolt.sock"},
	} {
		got, err := Origin(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Origin(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestCheckPinsAndRejectsChangedEndpoint(t *testing.T) {
	isolatePins(t)
	key := filepath.Join(t.TempDir(), ".beads")
	var stderr strings.Builder
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	if err := Check(key, "github", "https://one.example/api", false); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	os.Stderr = oldStderr
	_, _ = r.Read(make([]byte, 0))
	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	_ = r.Close()
	stderr.Write(buf[:n])
	if !strings.Contains(stderr.String(), "beads: trusting github endpoint https://one.example:443") {
		t.Fatalf("first-use notice = %q", stderr.String())
	}
	if err := Check(key, "github", "https://one.example/other", false); err != nil {
		t.Fatalf("same origin: %v", err)
	}
	err = Check(key, "github", "https://two.example/api", false)
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	for _, part := range []string{"github.url", "https://one.example:443", "https://two.example:443", "credential was not sent"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not contain %q", err, part)
		}
	}

	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "beads", pinsFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("pin file mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("pin directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
	lockInfo, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if lockInfo.Mode().Perm() != 0600 {
		t.Fatalf("lock file mode = %o, want 600", lockInfo.Mode().Perm())
	}
}

func TestCheckPrivateSourceRepinsAndLocalDoltDoesNotPin(t *testing.T) {
	isolatePins(t)
	key := filepath.Join(t.TempDir(), ".beads")
	if err := Check(key, "jira", "https://old.example", false); err != nil {
		t.Fatal(err)
	}
	if err := Check(key, "jira", "https://new.example", true); err != nil {
		t.Fatal(err)
	}
	if err := Check(key, "jira", "https://old.example", false); err == nil {
		t.Fatal("old pin was not replaced by private source")
	}
	for _, endpoint := range []string{"mysql://localhost:3306", "mysql://127.0.0.1:3306", "mysql://127.0.0.2:3306", "mysql://[::1]:3306", "unix:/tmp/dolt.sock"} {
		if err := Check(key, "dolt-server", endpoint, false); err != nil {
			t.Errorf("local endpoint %q: %v", endpoint, err)
		}
	}
	stored, err := readPins()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Workspaces[key]) != 1 {
		t.Fatalf("unexpected pins: %#v", stored.Workspaces[key])
	}
}

func TestPrivateSourceDoesNotRequireWritablePinStorage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only permission checks require a non-root user")
	}

	t.Run("matching pin", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		configDir := filepath.Join(home, ".config")
		t.Setenv("XDG_CONFIG_HOME", configDir)
		key := filepath.Join(t.TempDir(), ".beads")
		if err := Check(key, "github", "https://same.example", false); err != nil {
			t.Fatal(err)
		}

		pinDir := filepath.Join(configDir, "beads")
		if err := os.Chmod(pinDir, 0500); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(pinDir, 0700) }()

		if err := Check(key, "github", "https://same.example", true); err != nil {
			t.Fatalf("matching private endpoint: %v", err)
		}
		info, err := os.Stat(pinDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0500 {
			t.Fatalf("pin directory mode = %o, want unchanged 500", info.Mode().Perm())
		}
	})

	t.Run("no pin", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		configDir := filepath.Join(home, ".config")
		t.Setenv("XDG_CONFIG_HOME", configDir)
		if err := os.Mkdir(configDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(configDir, 0500); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(configDir, 0700) }()

		key := filepath.Join(t.TempDir(), ".beads")
		if err := Check(key, "github", "https://private.example", true); err != nil {
			t.Fatalf("private endpoint without writable pin storage: %v", err)
		}
		if _, err := os.Stat(filepath.Join(configDir, "beads")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected pin directory: err=%v", err)
		}
	})
}

func TestCheckDefaultAndCorruptPins(t *testing.T) {
	isolatePins(t)
	if err := Check("", "github", "", false); err != nil {
		t.Fatalf("empty endpoint: %v", err)
	}
	key := filepath.Join(t.TempDir(), ".beads")
	path, err := pinsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Check(key, "github", "https://example.com", false); err == nil {
		t.Fatal("corrupt pin file was overwritten")
	}
}

func TestCheckRequiresWorkspaceAndPinsJSON(t *testing.T) {
	isolatePins(t)
	if err := Check("", "github", "https://example.com", false); err == nil || !strings.Contains(err.Error(), "cannot determine workspace") {
		t.Fatalf("empty workspace error = %v", err)
	}
	if err := Check("", "github", "https://private.example", true); err != nil {
		t.Fatalf("private endpoint without workspace: %v", err)
	}
	key := filepath.Join(t.TempDir(), ".beads")
	if err := Check(key, "github", "https://example.com", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "beads", pinsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var got pins
	if err := json.Unmarshal(data, &got); err != nil || got.Version != 1 || got.Workspaces[key]["github"] != "https://example.com:443" {
		t.Fatalf("pins = %#v, err %v", got, err)
	}
}
