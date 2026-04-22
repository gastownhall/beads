package remotecache

import (
	"strings"
	"testing"
)

func TestIsRemoteURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Remote URLs — should return true
		{"dolthub://org/backend", true},
		{"dolthub://myorg/myrepo", true},
		{"https://doltremoteapi.dolthub.com/org/repo", true},
		{"http://localhost:50051/mydb", true},
		{"s3://my-bucket/beads", true},
		{"gs://my-bucket/beads", true},
		{"az://account.blob.core.windows.net/container/beads", true},
		{"file:///tmp/dolt-remote", true},
		{"ssh://git@github.com/org/repo", true},
		{"git+ssh://git@github.com/org/repo", true},
		{"git+https://github.com/org/repo", true},
		{"git+http://example.com/repo.git", true},
		{"git@github.com:org/repo.git", true},
		{"deploy@myserver.com:beads/data", true},

		// Local paths — should return false
		{".", false},
		{"..", false},
		{"~/beads-planning", false},
		{"/absolute/path/to/repo", false},
		{"../relative/path", false},
		{"relative/path", false},
		{"", false},
		{"/", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsRemoteURL(tt.input)
			if got != tt.want {
				t.Errorf("IsRemoteURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateRemoteURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string // substring expected in error message
	}{
		// === Valid URLs (should pass) ===
		{"dolthub basic", "dolthub://org/repo", false, ""},
		{"dolthub with dash", "dolthub://my-org/my-repo", false, ""},
		{"https dolthub", "https://doltremoteapi.dolthub.com/org/repo", false, ""},
		{"http localhost", "http://localhost:50051/mydb", false, ""},
		{"s3 bucket", "s3://my-bucket/beads", false, ""},
		{"gs bucket", "gs://my-bucket/beads", false, ""},
		{"az storage", "az://account.blob.core.windows.net/container/beads", false, ""},
		{"file URL", "file:///data/dolt-remote", false, ""},
		{"ssh URL", "ssh://git@github.com/org/repo", false, ""},
		{"git+ssh URL", "git+ssh://git@github.com/org/repo", false, ""},
		{"git+https URL", "git+https://github.com/org/repo", false, ""},
		{"git+http URL", "git+http://example.com/repo.git", false, ""},
		{"SCP-style git", "git@github.com:org/repo.git", false, ""},
		{"SCP-style deploy", "deploy@myserver.com:beads/data", false, ""},
		{"https with port", "https://example.com:8443/repo", false, ""},
		{"https with path", "https://github.com/user/repo/path", false, ""},

		// === Empty / missing ===
		{"empty string", "", true, "cannot be empty"},

		// === Control character injection ===
		{"null byte", "dolthub://org/repo\x00malicious", true, "control character"},
		{"null in middle", "dolthub://org\x00/repo", true, "control character"},
		{"newline injection", "dolthub://org/repo\nmalicious", true, "control character"},
		{"carriage return", "dolthub://org/repo\rmalicious", true, "control character"},
		{"tab character", "dolthub://org/repo\tmalicious", true, "control character"},
		{"bell character", "dolthub://org/repo\x07", true, "control character"},
		{"escape character", "dolthub://org/repo\x1b[31m", true, "control character"},
		{"DEL character", "dolthub://org/repo\x7f", true, "control character"},

		// === CLI flag injection ===
		{"leading dash", "-origin", true, "must not start with a dash"},
		{"double dash", "--force", true, "must not start with a dash"},
		{"dash flag URL", "-https://evil.com", true, "must not start with a dash"},

		// === Invalid schemes ===
		{"ftp scheme", "ftp://server/path", true, "not allowed"},
		{"javascript scheme", "javascript://alert(1)", true, "not allowed"},
		{"data scheme", "data:text/html,<h1>hi</h1>", true, "no scheme"},
		{"no scheme", "github.com/user/repo", true, "no scheme"},
		{"just path", "/path/to/repo", true, "no scheme"},

		// === Structural validation ===
		{"dolthub no repo", "dolthub://orgonly", true, "org/repo"},
		{"dolthub empty org", "dolthub:///repo", true, "org/repo"},
		{"https no host", "https:///path", true, "hostname"},
		{"ssh no host", "ssh:///path", true, "hostname"},
		{"git+ssh no host", "git+ssh:///path", true, "hostname"},
		{"git+https no host", "git+https:///path", true, "hostname"},
		{"git+http no host", "git+http:///path", true, "hostname"},
		{"s3 no bucket", "s3:///path", true, "bucket"},
		{"gs no bucket", "gs:///path", true, "bucket"},
		{"az no host", "az:///path", true, "hostname"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRemoteURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateRemoteURL(%q) = nil, want error containing %q", tt.url, tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateRemoteURL(%q) error = %q, want error containing %q", tt.url, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateRemoteURL(%q) = %v, want nil", tt.url, err)
				}
			}
		})
	}
}

func TestValidateRemoteName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid names
		{"simple", "origin", false, ""},
		{"with-hyphen", "my-remote", false, ""},
		{"with-underscore", "my_remote", false, ""},
		{"alphanumeric", "remote1", false, ""},
		{"single letter", "a", false, ""},
		{"mixed case", "MyRemote", false, ""},

		// Invalid names
		{"empty", "", true, "cannot be empty"},
		{"starts with digit", "1remote", true, "must start with a letter"},
		{"starts with dash", "-remote", true, "must not start with a dash"},
		{"starts with underscore", "_remote", true, "must start with a letter"},
		{"has dot", "my.remote", true, "must start with a letter"},
		{"has space", "my remote", true, "must start with a letter"},
		{"has semicolon", "remote;cmd", true, "must start with a letter"},
		{"has pipe", "remote|cmd", true, "must start with a letter"},
		{"too long", strings.Repeat("a", 65), true, "too long"},
		{"max length OK", strings.Repeat("a", 64), false, ""},
		{"null byte in name", "origin\x00evil", true, "must start with a letter"},
		{"newline in name", "origin\nevil", true, "must start with a letter"},
		{"backtick in name", "origin`whoami`", true, "must start with a letter"},
		{"dollar in name", "origin$HOME", true, "must start with a letter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRemoteName(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateRemoteName(%q) = nil, want error containing %q", tt.input, tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateRemoteName(%q) error = %q, want error containing %q", tt.input, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateRemoteName(%q) = %v, want nil", tt.input, err)
				}
			}
		})
	}
}

func TestMatchesRemotePattern(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		pattern string
		want    bool
	}{
		{"exact match", "dolthub://myorg/myrepo", "dolthub://myorg/myrepo", true},
		{"wildcard repo", "dolthub://myorg/anyrepo", "dolthub://myorg/*", true},
		{"wildcard no match", "dolthub://other/repo", "dolthub://myorg/*", false},
		{"az wildcard", "az://acct.blob.core.windows.net/container/beads", "az://*.blob.core.windows.net/*/*", true},
		{"scheme mismatch", "https://github.com/org/repo", "dolthub://*", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesRemotePattern(tt.url, tt.pattern)
			if got != tt.want {
				t.Errorf("MatchesRemotePattern(%q, %q) = %v, want %v", tt.url, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestValidateRemoteURLWithPatterns(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		patterns []string
		wantErr  bool
		errMsg   string
	}{
		{"no patterns allows any", "dolthub://org/repo", nil, false, ""},
		{"empty patterns allows any", "dolthub://org/repo", []string{}, false, ""},
		{"matches one pattern", "dolthub://myorg/repo", []string{"dolthub://myorg/*"}, false, ""},
		{"matches second pattern", "az://acct.blob.core.windows.net/c/p", []string{"dolthub://myorg/*", "az://acct.blob.core.windows.net/*/*"}, false, ""},
		{"no pattern match", "https://evil.com/data", []string{"dolthub://myorg/*"}, true, "does not match"},
		{"invalid URL fails before pattern check", "dolthub://\x00evil", []string{"dolthub://*"}, true, "control character"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRemoteURLWithPatterns(tt.url, tt.patterns)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateRemoteURLWithPatterns(%q, %v) = nil, want error", tt.url, tt.patterns)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateRemoteURLWithPatterns(%q, %v) error = %q, want %q", tt.url, tt.patterns, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateRemoteURLWithPatterns(%q, %v) = %v, want nil", tt.url, tt.patterns, err)
				}
			}
		})
	}
}

func TestCacheKey(t *testing.T) {
	// Deterministic
	k1 := CacheKey("dolthub://org/backend")
	k2 := CacheKey("dolthub://org/backend")
	if k1 != k2 {
		t.Errorf("CacheKey not deterministic: %q != %q", k1, k2)
	}

	// Different URLs produce different keys
	k3 := CacheKey("dolthub://org/frontend")
	if k1 == k3 {
		t.Errorf("CacheKey collision: %q and %q both produce %q", "dolthub://org/backend", "dolthub://org/frontend", k1)
	}

	// Length is 16 hex chars
	if len(k1) != 16 {
		t.Errorf("CacheKey length = %d, want 16", len(k1))
	}
}
