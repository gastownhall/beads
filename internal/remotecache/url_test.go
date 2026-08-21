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

func TestMatchesRemotePatternWithQuery(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		pattern string
		want    bool
	}{
		{
			"query candidate rejects queryless pattern",
			"s3://approved/db?endpoint=https://unapproved.example",
			"s3://approved/db",
			false,
		},
		{
			"query candidate rejects queryless wildcard pattern",
			"s3://approved/db?region=auto",
			"s3://approved/*",
			false,
		},
		{
			"query keys are order independent",
			"s3://approved/db?region=auto&path-style=true&endpoint=https://approved.example",
			"s3://approved/db?endpoint=https://approved.example&path-style=true&region=auto",
			true,
		},
		{
			"endpoint does not glob unless requested",
			"s3://approved/db?endpoint=https://unapproved.example",
			"s3://approved/db?endpoint=https://approved.example",
			false,
		},
		{
			"endpoint glob matches when requested",
			"s3://approved/db?endpoint=https://unapproved.example",
			"s3://approved/db?endpoint=https://*.example",
			true,
		},
		{
			"region matches exactly",
			"s3://approved/db?endpoint=https://approved.example&region=other",
			"s3://approved/db?endpoint=https://approved.example&region=auto",
			false,
		},
		{
			"path style matches exactly",
			"s3://approved/db?endpoint=https://approved.example&path-style=false",
			"s3://approved/db?endpoint=https://approved.example&path-style=true",
			false,
		},
		{
			"unlisted query key is rejected",
			"s3://approved/db?endpoint=https://approved.example&region=auto&extra=true",
			"s3://approved/db?endpoint=https://approved.example&region=auto",
			false,
		},
		{
			"outer userinfo is rejected",
			"s3://user@approved/db?endpoint=https://approved.example",
			"s3://*/*?endpoint=https://approved.example",
			false,
		},
		{
			"endpoint userinfo is rejected",
			"s3://approved/db?endpoint=https://user@approved.example",
			"s3://approved/db?endpoint=https://*@approved.example",
			false,
		},
		{
			"malformed endpoint is rejected",
			"s3://approved/db?endpoint=not-a-url",
			"s3://approved/db?endpoint=*",
			false,
		},
		{
			"non HTTP endpoint is rejected",
			"s3://approved/db?endpoint=ftp://approved.example",
			"s3://approved/db?endpoint=*",
			false,
		},
		{
			"queryless exact match is unchanged",
			"s3://approved/db",
			"s3://approved/db",
			true,
		},
		{
			"queryless wildcard match is unchanged",
			"s3://approved/db",
			"s3://approved/*",
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesRemotePattern(tt.url, tt.pattern); got != tt.want {
				t.Errorf("MatchesRemotePattern(%q, %q) = %t, want %t", tt.url, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchesRemotePatternLegacyAndEdgeCases(t *testing.T) {
	// Queryless-vs-queryless rows pin the legacy path.Match behaviour for the
	// forms ValidateRemoteURL accepts without url.Parse (SCP-style, bracketed
	// aws). The query-bearing rows pin the boundaries of the query-aware mode:
	// raw pre-query location, strict query parsing, fragment rejection,
	// decoded-key comparison and the endpoint key casing rule.
	tests := []struct {
		name    string
		url     string
		pattern string
		want    bool
	}{
		{"scp literal unchanged", "git@github.com:myorg/myrepo", "git@github.com:myorg/myrepo", true},
		{"scp wildcard unchanged", "git@github.com:myorg/myrepo", "git@github.com:myorg/*", true},
		{"scp candidate vs url pattern", "git@github.com:myorg/myrepo", "dolthub://myorg/*", false},
		{"scp candidate with a query fails closed", "git@github.com:myorg/myrepo?endpoint=https://evil.example", "git@github.com:myorg/*", false},
		{"bracketed aws wildcard unchanged", "aws://[dynamo:bucket]/db", "aws://*/db", true},
		{"bracketed aws literal does not match its own unescaped brackets (as on main)", "aws://[dynamo:bucket]/db", "aws://[dynamo:bucket]/db", false},
		{"pattern question mark is a query, not a glob", "dolthub://myorg/repoX", "dolthub://myorg/repo?", false},
		{"force-query candidate rejects queryless pattern", "s3://approved/db?", "s3://approved/db", false},
		{"fragment with a question mark rejects against queryless pattern", "s3://approved/db#frag?endpoint=https://evil.example", "s3://approved/*", false},
		{"fragment rejected in query-aware mode", "s3://approved/db?endpoint=https://ok.example#frag", "s3://approved/db?endpoint=https://ok.example", false},
		{"encoded question mark is path, not query", "s3://approved/db%3Fendpoint%3Dhttps%3A%2F%2Fevil.example", "s3://approved/*", true},
		{"encoded slash is not normalised for the location", "s3://approved/db%2Fchild?endpoint=https://ok.example", "s3://approved/db/child?endpoint=https://ok.example", false},
		{"scheme case is not normalised for the location", "S3://approved/db?endpoint=https://ok.example", "s3://approved/db?endpoint=https://ok.example", false},
		{"malformed query rejects", "s3://approved/db?endpoint=https://ok.example&bad=%zz", "s3://approved/db?endpoint=https://ok.example&bad=*", false},
		{"semicolon query rejects", "s3://approved/db?endpoint=https://ok.example;region=auto", "s3://approved/db?endpoint=https://ok.example&region=auto", false},
		{"endpoint key casing rejects", "s3://approved/db?Endpoint=https://evil.example", "s3://approved/db?Endpoint=*", false},
		{"bare star never matches an http endpoint (slash)", "s3://approved/db?endpoint=https://ok.example", "s3://approved/db?endpoint=*", false},
		{"any-endpoint pattern is *://*", "s3://approved/db?endpoint=https://ok.example", "s3://approved/db?endpoint=*://*", true},
		{"repeated endpoint key rejects against a single-valued pattern", "s3://approved/db?endpoint=https://ok.example&endpoint=https://evil.example", "s3://approved/db?endpoint=https://ok.example", false},
		{"query keys are exact, never globbed", "s3://approved/db?endpoint=https://evil.example", "s3://approved/db?*=*", false},
		{"second question mark stays in the query and is an unknown key", "s3://bucket/a?b?endpoint=https://x.example", "s3://bucket/*?endpoint=*://*", false},
		{"force-query candidate matches force-query pattern", "s3://approved/db?", "s3://approved/db?", true},
		{"pattern fragment rejected in query-aware mode", "s3://approved/db?endpoint=https://ok.example", "s3://approved/db?endpoint=https://ok.example#frag", false},
		{"query-bearing bracketed aws fails closed", "aws://[dynamo:bucket]/db?region=auto", "aws://*/db?region=auto", false},
		{"percent-encoded lowercase endpoint key is endpoint", "s3://approved/db?endpo%69nt=https://ok.example", "s3://approved/db?endpoint=https://ok.example", true},
		{"percent-encoded uppercase endpoint casing rejects", "s3://approved/db?%45ndpoint=https://evil.example", "s3://approved/db?%45ndpoint=*", false},
		{"decoded NUL in a query key rejects", "s3://approved/db?endpoint%00=https://ok.example", "s3://approved/db?endpoint%00=*://*", false},
		{"repeated endpoint values match as a multiset", "s3://approved/db?endpoint=https://b.example&endpoint=https://a.example", "s3://approved/db?endpoint=https://a.example&endpoint=https://b.example", true},
		{"pattern host character class needs no url.Parse", "s3://bucket1/db?endpoint=https://ok.example", "s3://bucket[0-9]/db?endpoint=https://ok.example", true},
		{"pattern userinfo rejected by the raw scan", "s3://approved/db?endpoint=https://ok.example", "s3://*@approved/db?endpoint=*://*", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesRemotePattern(tt.url, tt.pattern); got != tt.want {
				t.Errorf("MatchesRemotePattern(%q, %q) = %t, want %t", tt.url, tt.pattern, got, tt.want)
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
		{"scp-style remote matches scp-style wildcard", "git@github.com:myorg/myrepo", []string{"git@github.com:myorg/*"}, false, ""},
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

func TestValidateRemoteURLWithPatternsRejectsQueryCandidateForQuerylessPattern(t *testing.T) {
	err := ValidateRemoteURLWithPatterns(
		"s3://approved/db?region=auto",
		[]string{"s3://approved/*"},
	)
	if err == nil {
		t.Fatal("ValidateRemoteURLWithPatterns() = nil, want query-bearing URL to be rejected")
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
