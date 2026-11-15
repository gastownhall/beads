package configfile

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLookupCredentialsPassword(t *testing.T) {
	// Create a temp credentials file
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials")

	content := `# Test credentials file
[127.0.0.1:3307]
password=localPass

[beads.company.com:3307]
password=companyPass

[10.0.1.50:3308]
password=officePass
`
	if err := os.WriteFile(credFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}

	// Point BEADS_CREDENTIALS_FILE at our temp file
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)

	tests := []struct {
		name     string
		host     string
		port     int
		wantPass string
	}{
		{
			name:     "local server",
			host:     "127.0.0.1",
			port:     3307,
			wantPass: "localPass",
		},
		{
			name:     "company server",
			host:     "beads.company.com",
			port:     3307,
			wantPass: "companyPass",
		},
		{
			name:     "office server different port",
			host:     "10.0.1.50",
			port:     3308,
			wantPass: "officePass",
		},
		{
			name:     "unknown server returns empty",
			host:     "unknown.host",
			port:     3307,
			wantPass: "",
		},
		{
			name:     "right host wrong port returns empty",
			host:     "127.0.0.1",
			port:     9999,
			wantPass: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LookupCredentialsPassword(tt.host, tt.port)
			if got != tt.wantPass {
				t.Errorf("LookupCredentialsPassword(%q, %d) = %q, want %q",
					tt.host, tt.port, got, tt.wantPass)
			}
		})
	}
}

func TestLookupCredentialsPassword_MissingFile(t *testing.T) {
	t.Setenv("BEADS_CREDENTIALS_FILE", "/nonexistent/path/credentials")

	got := LookupCredentialsPassword("127.0.0.1", 3307)
	if got != "" {
		t.Errorf("expected empty string for missing file, got %q", got)
	}
}

func TestLookupCredentialsPassword_EnvVarTakesPrecedence(t *testing.T) {
	// Create a credentials file with one password
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials")
	if err := os.WriteFile(credFile, []byte("[127.0.0.1:3307]\npassword=filePass\n"), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)

	// Set BEADS_DOLT_PASSWORD env var — should win
	t.Setenv("BEADS_DOLT_PASSWORD", "envPass")

	cfg := DefaultConfig()
	got := cfg.GetDoltServerPassword()
	if got != "envPass" {
		t.Errorf("GetDoltServerPassword() = %q, want %q (env var should take precedence)", got, "envPass")
	}
}

func TestLookupCredentialsPassword_FallsBackToFile(t *testing.T) {
	// Create a credentials file
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials")
	if err := os.WriteFile(credFile, []byte("[127.0.0.1:3307]\npassword=filePass\n"), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)

	// Clear port and password env vars so defaults + file lookup kick in.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_PASSWORD", "")

	cfg := DefaultConfig()
	got := cfg.GetDoltServerPassword()
	if got != "filePass" {
		t.Errorf("GetDoltServerPassword() = %q, want %q (should fall back to credentials file)", got, "filePass")
	}
}

func TestLookupCredentialsPassword_MultipleServers(t *testing.T) {
	// Verify different projects connecting to different servers get the right password
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials")
	content := `[127.0.0.1:3307]
password=personalDevPass

[beads.work.internal:3307]
password=workServerPass
`
	if err := os.WriteFile(credFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_PASSWORD", "")

	// Personal project uses localhost
	personal := DefaultConfig()
	personal.DoltServerHost = "127.0.0.1"
	personal.DoltServerPort = 3307
	got := personal.GetDoltServerPassword()
	if got != "personalDevPass" {
		t.Errorf("personal project password = %q, want %q", got, "personalDevPass")
	}

	// Work project uses corporate server
	work := DefaultConfig()
	work.DoltServerHost = "beads.work.internal"
	work.DoltServerPort = 3307
	got = work.GetDoltServerPassword()
	if got != "workServerPass" {
		t.Errorf("work project password = %q, want %q", got, "workServerPass")
	}
}

func TestGetDoltServerPasswordForPort_OverridesConfigPort(t *testing.T) {
	// Simulates the tunnel scenario: metadata.json has port 3308 (tunnel)
	// but the doltserver port file resolves to 3307 (local). The credentials
	// file has the password under [127.0.0.1:3307]. GetDoltServerPassword()
	// would look up [127.0.0.1:3308] and miss. GetDoltServerPasswordForPort(3307)
	// should find it.
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials")
	content := `[127.0.0.1:3307]
password=localPass

[127.0.0.1:3308]
password=tunnelPass
`
	if err := os.WriteFile(credFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_PASSWORD", "")

	cfg := DefaultConfig()
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = 3308 // metadata.json says tunnel port

	// GetDoltServerPassword uses config port (3308) → tunnelPass
	got := cfg.GetDoltServerPassword()
	if got != "tunnelPass" {
		t.Errorf("GetDoltServerPassword() = %q, want %q", got, "tunnelPass")
	}

	// GetDoltServerPasswordForPort with resolved runtime port (3307) → localPass
	got = cfg.GetDoltServerPasswordForPort(3307)
	if got != "localPass" {
		t.Errorf("GetDoltServerPasswordForPort(3307) = %q, want %q", got, "localPass")
	}

	// GetDoltServerPasswordForPort with tunnel port (3308) → tunnelPass
	got = cfg.GetDoltServerPasswordForPort(3308)
	if got != "tunnelPass" {
		t.Errorf("GetDoltServerPasswordForPort(3308) = %q, want %q", got, "tunnelPass")
	}
}

func TestReadPasswordFromFile_InlineComments(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials")
	content := `# Full line comment
[127.0.0.1:3307]
password=myPass # this is an inline comment
`
	if err := os.WriteFile(credFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}

	got := readPasswordFromFile(credFile, "127.0.0.1:3307")
	if got != "myPass" {
		t.Errorf("readPasswordFromFile() = %q, want %q (should strip inline comments)", got, "myPass")
	}
}

func TestReadPasswordFromFile_WhitespaceHandling(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials")
	content := `[ 127.0.0.1:3307 ]
password = spaced

[10.0.0.1:3307]
  password  =  padded
`
	if err := os.WriteFile(credFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}

	// Section keys are not trimmed internally — the brackets are stripped but
	// inner whitespace is part of the key. Standard INI behavior.
	got := readPasswordFromFile(credFile, "10.0.0.1:3307")
	if got != "padded" {
		t.Errorf("readPasswordFromFile() = %q, want %q", got, "padded")
	}
}

func TestDefaultCredentialsPath(t *testing.T) {
	path := DefaultCredentialsPath()
	if path == "" {
		t.Skip("could not determine home directory")
	}
	// Should end with the expected suffix
	if !filepath.IsAbs(path) {
		t.Errorf("DefaultCredentialsPath() = %q, want absolute path", path)
	}
}

// An IPv6 Dolt host may be configured bare ("::1") or bracketed ("[::1]"), and
// both name the same server. The password lookup has to agree, whichever of the
// two section spellings the credentials file uses: [[::1]:3307], the
// net.JoinHostPort form, or [::1:3307], the form a bare host was looked up under
// before. On main each host spelling found only its own section spelling. A
// host with surrounding whitespace finds them too, as it is classified and
// dialed trimmed.
func TestLookupCredentialsPassword_IPv6HostEitherSpelling(t *testing.T) {
	for _, tc := range []struct{ addr, section string }{
		{"::1", "[[::1]:3307]"},
		{"::1", "[::1:3307]"},
		{"2001:db8::1", "[[2001:db8::1]:3307]"},
		{"2001:db8::1", "[2001:db8::1:3307]"},
	} {
		t.Run(tc.section, func(t *testing.T) {
			credFile := filepath.Join(t.TempDir(), "credentials")
			if err := os.WriteFile(credFile, []byte(tc.section+"\npassword=ipv6Pass\n"), 0600); err != nil {
				t.Fatalf("failed to write credentials file: %v", err)
			}
			t.Setenv("BEADS_CREDENTIALS_FILE", credFile)

			for _, host := range []string{tc.addr, "[" + tc.addr + "]", " [" + tc.addr + "] "} {
				if got := LookupCredentialsPassword(host, 3307); got != "ipv6Pass" {
					t.Errorf("LookupCredentialsPassword(%q, 3307) = %q, want %q", host, got, "ipv6Pass")
				}
			}
			if got := LookupCredentialsPassword(tc.addr, 3308); got != "" {
				t.Errorf("LookupCredentialsPassword(%q, 3308) = %q, want no password for another port", tc.addr, got)
			}
		})
	}
}

// An IPv6 host's [[::1]:3307] section is read before [::1:3307], and a
// password key there decides, even with an empty value: `password=` clears the
// password, as it does for a host with one section, rather than falling
// through to the other section's old one. Only a first section with no
// password key falls through.
func TestLookupCredentialsPassword_IPv6HostPreferredSectionDecides(t *testing.T) {
	for _, tc := range []struct{ name, preferred, want string }{
		{"empty password", "password=\n", ""},
		{"no password key", "user=admin\n", "legacyPass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credFile := filepath.Join(t.TempDir(), "credentials")
			content := "[[::1]:3307]\n" + tc.preferred + "\n[::1:3307]\npassword=legacyPass\n"
			if err := os.WriteFile(credFile, []byte(content), 0600); err != nil {
				t.Fatalf("failed to write credentials file: %v", err)
			}
			t.Setenv("BEADS_CREDENTIALS_FILE", credFile)

			for _, host := range []string{"::1", "[::1]"} {
				if got := LookupCredentialsPassword(host, 3307); got != tc.want {
					t.Errorf("LookupCredentialsPassword(%q, 3307) = %q, want %q", host, got, tc.want)
				}
			}
		})
	}
}

// An IPv6 host has two candidate sections, but the lookup reads the credentials
// file once, so an overly permissive file is warned about once per lookup, as
// it is for any other host, not once per candidate section.
func TestLookupCredentialsPassword_IPv6HostWarnsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credentials file permissions are not checked on Windows")
	}
	credFile := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credFile, []byte("[::1:3307]\npassword=legacyPass\n"), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}
	if err := os.Chmod(credFile, 0644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)

	for _, tc := range []struct{ host, want string }{
		{"::1", "legacyPass"},   // found under the second, legacy section
		{"[::1]", "legacyPass"}, // likewise
		{"2001:db8::1", ""},     // no section: both candidates are tried
	} {
		t.Run(tc.host, func(t *testing.T) {
			var got string
			stderr := captureStderr(t, func() { got = LookupCredentialsPassword(tc.host, 3307) })
			if got != tc.want {
				t.Errorf("LookupCredentialsPassword(%q, 3307) = %q, want %q", tc.host, got, tc.want)
			}
			if n := strings.Count(stderr, "overly permissive permissions"); n != 1 {
				t.Errorf("permissions warning printed %d times, want once; stderr:\n%s", n, stderr)
			}
		})
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}
