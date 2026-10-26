package endpointtrust

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
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
		{"https://Example.com", "https://example.com:443"},
		{"https://Example.com/path", "https://example.com:443/path"},
		{"https://example.com:443/other", "https://example.com:443/other"},
		{"https://example.com/", "https://example.com:443"},
		{"https://example.com/api/", "https://example.com:443/api"},
		{"https://example.com/api/../v2", "https://example.com:443/v2"},
		{"https://example.com/%2e%2e/v2", "https://example.com:443/v2"},
		{"https://user:pass@example.com/graphql", "https://example.com:443/graphql"},
		{"http://[::1]/path", "http://[::1]:80/path"},
		{"mysql://[2001:DB8::1]:3306/db", "mysql://[2001:db8::1]:3306/db"},
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
	// The pin is established by a private source; a shared endpoint then
	// matches it exactly or is refused.
	if err := Check(key, "github", "https://one.example/api", true); err != nil {
		t.Fatal(err)
	}
	if err := Check(key, "github", "https://one.example/api/", false); err != nil {
		t.Fatalf("same origin after path normalization: %v", err)
	}
	err := Check(key, "github", "https://one.example/other", false)
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("changed path mismatch error = %v", err)
	}
	err = Check(key, "github", "https://two.example/api", false)
	if !errors.As(err, &mismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	for _, part := range []string{"github.url", "https://one.example:443/api", "https://two.example:443/api", "credential was not sent"} {
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

func TestCheckPrivateSourceRepinsAndLocalDoltPinsSentinel(t *testing.T) {
	isolatePins(t)
	key := filepath.Join(t.TempDir(), ".beads")
	if err := Check(key, "jira", "https://old.example", true); err != nil {
		t.Fatal(err)
	}
	if err := Check(key, "jira", "https://new.example", true); err != nil {
		t.Fatal(err)
	}
	if err := Check(key, "jira", "https://old.example", false); err == nil {
		t.Fatal("old pin was not replaced by private source")
	}
	sock := filepath.Join(t.TempDir(), "dolt.sock")
	if err := os.WriteFile(sock, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"mysql://localhost:3306", "mysql://127.0.0.1:3306", "mysql://127.0.0.2:3306", "mysql://[::1]:3306", "unix:" + sock} {
		if err := Check(key, "dolt-server", endpoint, false); err != nil {
			t.Errorf("local endpoint %q: %v", endpoint, err)
		}
	}
	stored, err := readPins()
	if err != nil {
		t.Fatal(err)
	}
	// Loopback endpoints record the sentinel so a later shared remote host is
	// a pin change; a unix socket is ownership-checked but never pinned.
	want := map[string]string{"jira": "https://new.example:443", "dolt-server": localDoltOrigin}
	if len(stored.Workspaces[key]) != len(want) {
		t.Fatalf("unexpected pins: %#v", stored.Workspaces[key])
	}
	for kind, origin := range want {
		if stored.Workspaces[key][kind] != origin {
			t.Errorf("pin[%s] = %q, want %q", kind, stored.Workspaces[key][kind], origin)
		}
	}
}

func TestCheckDoltServerPinsRemoteAndRejectsSharedChange(t *testing.T) {
	isolatePins(t)
	key := filepath.Join(t.TempDir(), ".beads")
	if err := Check(key, "dolt-server", "mysql://localhost:3306", false); err != nil {
		t.Fatal(err)
	}
	// A remote host shared via committed config replaces the local sentinel
	// only when a private source confirms it.
	err := Check(key, "dolt-server", "mysql://db.example:3306", false)
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("local-to-remote change = %v, want MismatchError", err)
	}
	if err := Check(key, "dolt-server", "mysql://db.example:3306", true); err != nil {
		t.Fatalf("private remote confirmation: %v", err)
	}
	err = Check(key, "dolt-server", "mysql://other.example:3306", false)
	if !errors.As(err, &mismatch) {
		t.Fatalf("remote-to-remote change = %v, want MismatchError", err)
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
		if err := Check(key, "github", "https://same.example", true); err != nil {
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
	// A shared first use is refused, then a private confirmation writes the pin.
	err := Check(key, "github", "https://example.com", false)
	var firstUse *FirstUseError
	if !errors.As(err, &firstUse) {
		t.Fatalf("shared first use = %v, want FirstUseError", err)
	}
	if err := Check(key, "github", "https://example.com", true); err != nil {
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

// TestCheckSharedFirstUseRefusesUntilPrivatelyConfirmed pins the remaining
// half of the committed-endpoint scenario: a workspace that has no pin at
// all must not silently pin whatever shared config points at on first use,
// or a fresh clone carrying a committed custom endpoint would send the
// credential on the first bd command. The pin still comes from a private
// confirmation, and the shared endpoint is then admitted.
func TestCheckSharedFirstUseRefusesUntilPrivatelyConfirmed(t *testing.T) {
	isolatePins(t)
	key := filepath.Join(t.TempDir(), ".beads")

	err := Check(key, "github", "https://evil.example", false)
	var firstUse *FirstUseError
	if !errors.As(err, &firstUse) {
		t.Fatalf("shared first use = %v, want FirstUseError", err)
	}
	for _, part := range []string{"github.url", "https://evil.example:443", "credential was not sent", "GITHUB_API_URL"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not contain %q", err, part)
		}
	}
	stored, err := readPins()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Workspaces[key]["github"] != "" {
		t.Fatalf("refused first use still wrote a pin: %#v", stored.Workspaces[key])
	}

	if err := Check(key, "github", "https://evil.example", true); err != nil {
		t.Fatalf("private confirmation: %v", err)
	}
	if err := Check(key, "github", "https://evil.example", false); err != nil {
		t.Fatalf("shared endpoint matching the confirmed pin: %v", err)
	}
}

// TestCheckDefaultEndpointPinsAndBlocksSharedCustom pins the fix for the
// default-to-custom bypass: a workspace on the vendor default records a pin,
// so a custom endpoint arriving later through shared config is a pin change
// that refuses the credential, not a silent first use.
func TestCheckDefaultEndpointPinsAndBlocksSharedCustom(t *testing.T) {
	isolatePins(t)
	key := filepath.Join(t.TempDir(), ".beads")
	if err := Check(key, "github", "", false); err != nil {
		t.Fatalf("default endpoint: %v", err)
	}
	stored, err := readPins()
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Workspaces[key]["github"]; got != guidance["github"].defaultOrigin {
		t.Fatalf("default pin = %q, want %q", got, guidance["github"].defaultOrigin)
	}
	err = Check(key, "github", "https://evil.example", false)
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("shared custom after default = %v, want MismatchError", err)
	}
	if err := Check(key, "github", "https://evil.example", true); err != nil {
		t.Fatalf("private confirmation of the new endpoint: %v", err)
	}
}

// TestCheckDoltServerRejectsForeignOwnedSocket pins the shared-host check: a
// unix socket owned by another uid must never receive the Dolt credential.
func TestCheckDoltServerRejectsForeignOwnedSocket(t *testing.T) {
	if os.Geteuid() < 0 {
		t.Skip("no euid on this platform")
	}
	isolatePins(t)
	sock := filepath.Join(t.TempDir(), "dolt.sock")
	if err := os.WriteFile(sock, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), ".beads")
	if err := Check(key, "dolt-server", "unix:"+sock, false); err != nil {
		t.Fatalf("own socket: %v", err)
	}
	orig := currentEUIDFn
	currentEUIDFn = func() int { return os.Geteuid() + 1 }
	defer func() { currentEUIDFn = orig }()
	err := Check(key, "dolt-server", "unix:"+sock, false)
	if err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("foreign-owned socket error = %v", err)
	}
}

// TestPrivateMatchIgnoresDotenvValues pins that a .beads/.env-imported
// variable is shared configuration and cannot privately confirm an endpoint.
func TestPrivateMatchIgnoresDotenvValues(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "https://same.example")
	config.MarkDotenvValue("GITHUB_API_URL", "https://same.example")
	t.Cleanup(config.ResetForTesting)
	if PrivateMatch("github.url", "GITHUB_API_URL", "https://same.example") {
		t.Fatal("dotenv-imported GITHUB_API_URL privately confirmed the endpoint")
	}
}

// TestWorkspaceKeyPrefersDiscoveredWorkspaceAndIgnoresDotenvBeadsDir pins the
// workspace key to the .beads directory bd discovered before applying
// .beads/.env, so a committed BEADS_DIR cannot re-key the pins.
func TestWorkspaceKeyPrefersDiscoveredWorkspaceAndIgnoresDotenvBeadsDir(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	discovered := t.TempDir()
	dotenv := t.TempDir()
	t.Setenv("BEADS_DIR", dotenv)
	config.MarkDotenvValue("BEADS_DIR", dotenv)
	config.RecordWorkspaceBeadsDir(discovered)
	want, err := cleanWorkspaceKey(discovered)
	if err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceKey(); got != want {
		t.Fatalf("WorkspaceKey = %q, want the discovered workspace %q", got, want)
	}
}
