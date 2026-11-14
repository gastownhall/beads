package configfile

import (
	"bufio"
	"fmt"
	"net"
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

	return readPasswordFromFile(credFile, CredentialsSectionKeys(host, port)...)
}

// CredentialsSectionKeys returns the [host:port] section names that hold the
// password for the server at host and port, in the order
// LookupCredentialsPassword tries them, most canonical first. An IPv6 literal
// host may be configured bare ("::1") or bracketed ("[::1]"), and both name
// the same server, so either spelling finds a section written as [[::1]:3307]
// (the net.JoinHostPort form) or as [::1:3307] (the form a bare host was
// looked up under before). Any other host has one key.
func CredentialsSectionKeys(host string, port int) []string {
	bare := UnbracketHost(host)
	joined := net.JoinHostPort(bare, fmt.Sprint(port))
	legacy := fmt.Sprintf("%s:%d", bare, port)
	if legacy == joined {
		return []string{joined}
	}
	return []string{joined, legacy}
}

// UnbracketHost returns host, trimmed of surrounding whitespace, without one
// pair of enclosing square brackets, which an IPv6 literal may be configured
// in ("[::1]" is "::1"), giving the form net.JoinHostPort takes. It strips
// them from any host, so "[db]" is "db" too. ClassifyDoltHost reads a host
// through it, so a host it classifies is dialed, and its credentials are
// looked up, at the address it classified.
func UnbracketHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}

// readPasswordFromFile parses an INI-style credentials file and returns the
// password of the first of sectionKeys, in order, whose [host:port] section
// has a password key. An empty value there is that section's password, as it
// is with one key: it is returned, not skipped for a later key's. The file is
// read, and its permissions checked, once however many keys are given.
// Returns empty string on any error.
func readPasswordFromFile(path string, sectionKeys ...string) string {
	f, err := os.Open(path) //nolint:gosec // path comes from env var or os.UserHomeDir, not user input
	if err != nil {
		return ""
	}
	defer f.Close()

	// Warn if file has overly permissive permissions (unix only)
	warnIfInsecurePermissions(path)

	wanted := make(map[string]bool, len(sectionKeys))
	for _, key := range sectionKeys {
		wanted[key] = true
	}
	found := make(map[string]string, len(sectionKeys))

	scanner := bufio.NewScanner(f)
	section := ""
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
			section = line[1 : len(line)-1]
			inSection = wanted[section]
			continue
		}

		// Key=value within one of our sections; its first password wins.
		if inSection {
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == "password" {
				if _, seen := found[section]; !seen {
					found[section] = strings.TrimSpace(value)
				}
			}
		}
	}

	for _, key := range sectionKeys {
		if pw, ok := found[key]; ok {
			return pw
		}
	}
	return ""
}
