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

// TestIsRemoteURLRejectsUserlessDottedHost pins this package's side of the one
// intentional disagreement with doltremote.scpStyleGitURLPattern, whose first
// alternative is byte-identical to gitSSHPattern but which additionally accepts
// the user-less dotted-host form. doltremote.Normalize converts that form to
// git+ssh://; IsRemoteURL treats it as a local path. Changing this behavior
// means reconciling both files, not just this one.
func TestIsRemoteURLRejectsUserlessDottedHost(t *testing.T) {
	userless := []string{
		"github.com:org/repo.git",
		"myserver.com:beads/data",
	}
	for _, raw := range userless {
		t.Run(raw, func(t *testing.T) {
			if IsRemoteURL(raw) {
				t.Errorf("IsRemoteURL(%q) = true, want false", raw)
			}
		})
	}

	// The shared alternative: identical charset, and both packages accept it.
	withUser := []string{
		"git@github.com:org/repo.git",
		"deploy@myserver.com:beads/data",
	}
	for _, raw := range withUser {
		t.Run(raw, func(t *testing.T) {
			if !IsRemoteURL(raw) {
				t.Errorf("IsRemoteURL(%q) = false, want true", raw)
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
		{"aws bucket", "aws://my-bucket/beads", false, ""},
		{"gs bucket", "gs://my-bucket/beads", false, ""},
		{"az storage", "az://account.blob.core.windows.net/container/beads", false, ""},
		{"oci storage", "oci://namespace/bucket/path", false, ""},
		{"file URL", "file:///data/dolt-remote", false, ""},
		{"ssh URL", "ssh://git@github.com/org/repo", false, ""},
		{"git protocol URL", "git://github.com/org/repo", false, ""},
		{"git+ssh URL", "git+ssh://git@github.com/org/repo", false, ""},
		{"git+https URL", "git+https://github.com/org/repo", false, ""},
		{"git+http URL", "git+http://example.com/repo.git", false, ""},
		{"git+file URL", "git+file:///tmp/repo.git", false, ""},
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
		// Dolt's ssh:// transport hands "[user@]host" to ssh as an argument.
		{"ssh user option", "ssh://-oProxyCommand=touch%20%2Ftmp%2Fx@host/path", true, "user name in a remote URL must not start with a dash"},
		{"ssh encoded user option", "ssh://%2DoProxyCommand=x@host/path", true, "user name in a remote URL must not start with a dash"},
		{"ssh host option", "ssh://-oProxyCommand=x/path", true, "host in a remote URL must not start with a dash"},
		{"git+ssh user option", "git+ssh://-oProxyCommand=x@host/path", true, "user name in a remote URL must not start with a dash"},
		{"ssh inner dashes", "ssh://deploy-bot@my-host.example.com:2222/srv/beads", false, ""},

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
		{"git no host", "git:///path", true, "hostname"},
		{"git+ssh no host", "git+ssh:///path", true, "hostname"},
		{"git+https no host", "git+https:///path", true, "hostname"},
		{"git+http no host", "git+http:///path", true, "hostname"},
		{"s3 no bucket", "s3:///path", true, "bucket"},
		{"aws no bucket", "aws:///path", true, "bucket"},
		{"gs no bucket", "gs:///path", true, "bucket"},
		{"az no host", "az:///path", true, "hostname"},
		{"oci no host", "oci:///path", true, "host"},
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

// TestOptionShapedPart pins which remote URLs a program bd or Dolt hands them
// to would read as an option: git reads a leading "-" (also after git+), and
// Dolt's ssh:// transport runs `ssh [user@]host <command>`, so an ssh user
// name or host starting with "-" (percent-encoded too, as Dolt decodes it)
// would reach ssh as -oProxyCommand=<command>.
func TestOptionShapedPart(t *testing.T) {
	tests := []struct {
		remote string
		want   string
	}{
		// The whole URL.
		{"-origin", OptionShapedURL},
		{"--upload-pack=touch /tmp/x;false", OptionShapedURL},
		{"  -utouch /tmp/x", OptionShapedURL},
		{"git+--upload-pack=x", OptionShapedURL},
		{"GIT+ -ux", OptionShapedURL},
		{"-oProxyCommand=x@host:path", OptionShapedURL},

		// The user name of an ssh URL.
		{"ssh://-oProxyCommand=touch%20%2Ftmp%2Fx@host/path", OptionShapedUser},
		{"ssh://%2DoProxyCommand=x@host/path", OptionShapedUser},
		{"ssh://%20-oProxyCommand=x@host/path", OptionShapedUser},
		{"ssh://-oProxyCommand=x:secret@host/path", OptionShapedUser},
		{"SSH://-oProxyCommand=x@host/path", OptionShapedUser},
		{"git+ssh://-oProxyCommand=x@host/path.git", OptionShapedUser},
		{"Git+SSH://-oProxyCommand=x@host/path", OptionShapedUser},

		// The host of an ssh URL.
		{"ssh://-oProxyCommand=touch$IFS.x/path", OptionShapedHost},
		{"ssh://git@-oProxyCommand=x/path", OptionShapedHost},
		{"ssh://-host:2222/path", OptionShapedHost},
		{"ssh://[-oProxyCommand=x]/path", OptionShapedHost},
		{"git+ssh://-oProxyCommand=x/path", OptionShapedHost},

		// Nothing option-shaped.
		{"", ""},
		{"ssh://git@github.com/org/repo", ""},
		{"ssh://deploy-bot@my-host.example.com:2222/srv/beads", ""},
		{"ssh://user@host/-path", ""},
		{"ssh://user@host/path?x=-y#-z", ""},
		{"git+ssh://git@github.com/org/repo.git", ""},
		{"git@github.com:org/repo.git", ""},
		{"dolthub://org/repo", ""},
		{"file:///-local/path", ""},
		// An http(s) user name is a credential sent in a header, never an
		// argument, and a token may well start with "-".
		{"https://-token@github.com/org/repo", ""},
		{"git+https://-token@github.com/org/repo.git", ""},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			if got := OptionShapedPart(tt.remote); got != tt.want {
				t.Errorf("OptionShapedPart(%q) = %q, want %q", tt.remote, got, tt.want)
			}
		})
	}
}

func TestValidateRemoteURLSchemeErrorListsAcceptedSchemes(t *testing.T) {
	err := ValidateRemoteURL("ftp://server/path")
	if err == nil {
		t.Fatal("expected invalid scheme error")
	}
	msg := err.Error()
	for _, scheme := range []string{"aws", "oci", "git", "git+file"} {
		if !strings.Contains(msg, scheme) {
			t.Fatalf("invalid scheme error %q should include accepted scheme %q", msg, scheme)
		}
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
