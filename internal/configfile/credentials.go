package configfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultCredentialsPath returns the platform-appropriate default credentials file path.
// Linux/macOS: ~/.config/beads/credentials
// Windows: %APPDATA%\beads\credentials
func DefaultCredentialsPath() string {
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "beads", "credentials")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "beads", "credentials")
}

// LookupCredentialsPassword reads a password from an INI-style credentials file,
// keyed by [host:port] section. Returns empty string if not found or on any error.
//
// File format:
//
//	[127.0.0.1:3307]
//	password=localDevPassword
//
//	[beads.company.com:3307]
//	password=teamServerPassword
//
// The file path is determined by:
//  1. BEADS_CREDENTIALS_FILE env var (if set)
//  2. Default platform path (see DefaultCredentialsPath)
func LookupCredentialsPassword(host string, port int) string {
	credFile := os.Getenv("BEADS_CREDENTIALS_FILE")
	if credFile == "" {
		credFile = DefaultCredentialsPath()
	}
	if credFile == "" {
		return ""
	}

	return readPasswordFromFile(credFile, fmt.Sprintf("%s:%d", host, port))
}

// readPasswordFromFile parses an INI-style credentials file and returns the
// password for the given [host:port] section. Returns empty string on any error.
func readPasswordFromFile(path string, sectionKey string) string {
	f, err := os.Open(path) //nolint:gosec // path comes from env var or os.UserHomeDir, not user input
	if err != nil {
		return ""
	}
	defer f.Close()

	// Warn if file has overly permissive permissions (unix only)
	warnIfInsecurePermissions(path)

	scanner := bufio.NewScanner(f)
	inSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Strip inline comments
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		// Section header: [host:port]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := line[1 : len(line)-1]
			if section == sectionKey {
				inSection = true
			} else if inSection {
				// We've left our section without finding a password
				break
			}
			continue
		}

		// Key=value within our section
		if inSection {
			key, value, found := strings.Cut(line, "=")
			if found && strings.TrimSpace(key) == "password" {
				return strings.TrimSpace(value)
			}
		}
	}

	return ""
}
