package configfile

import (
	"os"
	"path/filepath"
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

	// Clear BEADS_DOLT_PASSWORD so file lookup kicks in
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
