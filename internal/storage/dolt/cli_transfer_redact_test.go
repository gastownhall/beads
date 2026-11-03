package dolt

import (
	"strings"
	"testing"
)

// TestRedactURLCredentials pins the per-scheme rule applied to whole remote
// URLs, matching the redactRemoteURL table in cmd/bd/sync_remote_test.go:
// http(s) userinfo is transport credentials and is dropped whole, ssh keeps
// the account and drops only a password, and everything else about the URL
// (host spelling, port, path) is preserved.
func TestRedactURLCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "https token is dropped whole",
			in:   "https://x-access-token:ghp_TESTTOKEN@github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			name: "git+https token is dropped whole",
			in:   "git+https://user:pass@github.com/org/repo.git",
			want: "git+https://github.com/org/repo.git",
		},
		{
			name: "token as username is dropped",
			in:   "https://ghp_TESTTOKEN@github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			name: "http basic auth is dropped whole",
			in:   "http://user:pass@myserver:7007/mydb",
			want: "http://myserver:7007/mydb",
		},
		{
			name: "port is kept",
			in:   "git+https://user:pass@github.com:5432/org/repo",
			want: "git+https://github.com:5432/org/repo",
		},
		{
			name: "mixed-case host spelling is preserved",
			in:   "https://user:pass@GitHub.COM/org/repo",
			want: "https://GitHub.COM/org/repo",
		},
		{
			name: "unencoded @ inside the password",
			in:   "https://user:p@ss@github.com/org/repo",
			want: "https://github.com/org/repo",
		},
		{
			// The conventional SSH account selector is not a secret; a hint
			// naming the remote is useless without it.
			name: "ssh user is kept",
			in:   "git+ssh://git@github.com/org/repo.git",
			want: "git+ssh://git@github.com/org/repo.git",
		},
		{
			name: "ssh password is dropped, user kept",
			in:   "ssh://alice:hunter2@my-dolt.example.com/org/db",
			want: "ssh://alice@my-dolt.example.com/org/db",
		},
		{
			name: "ssh without password and with port is unchanged",
			in:   "ssh://git@github.com:2222/org/repo",
			want: "ssh://git@github.com:2222/org/repo",
		},
		{
			name: "scp style is untouched",
			in:   "git@github.com:org/repo.git",
			want: "git@github.com:org/repo.git",
		},
		{name: "plain remote name", in: "origin", want: "origin"},
		{name: "no userinfo", in: "https://github.com/org/repo.git", want: "https://github.com/org/repo.git"},
		{name: "dolt native", in: "dolthub://myorg/mydb", want: "dolthub://myorg/mydb"},
		{name: "file remote", in: "file:///tmp/beads-remote", want: "file:///tmp/beads-remote"},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactURLCredentials(tt.in); got != tt.want {
				t.Errorf("redactURLCredentials(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestScrubTransferText covers the redaction of credential-bearing URLs that
// arrive embedded in free-form text — most importantly dolt's combined
// push/pull output, which echoes the tokenized remote. Credential-free
// diagnostics must pass through byte-identical so the error keeps dolt's
// actual complaint.
func TestScrubTransferText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "tokenized dolt output is scrubbed, host and path kept",
			in:   "error: failed to push some refs to 'git+https://x-access-token:ghp_TESTTOKEN@github.com/org/repo'",
			want: "error: failed to push some refs to 'git+https://github.com/org/repo'",
		},
		{
			name: "double-quoted URL is scrubbed",
			in:   `fatal: unable to access "https://user:pass@github.com/org/repo": 403`,
			want: `fatal: unable to access "https://github.com/org/repo": 403`,
		},
		{
			name: "two credential URLs in one line are both scrubbed",
			in:   "https://u:p@a.example.com/x and https://v:q@b.example.com/y",
			want: "https://a.example.com/x and https://b.example.com/y",
		},
		{
			name: "ssh password embedded in text is dropped, user kept",
			in:   "note: not pulling ssh://alice:hunter2@dolt.example.com/org/db right now",
			want: "note: not pulling ssh://alice@dolt.example.com/org/db right now",
		},
		{
			name: "peer operation description is scrubbed",
			in:   "push to peer git+ssh://git:hunter2@dolt.example.com/org/db",
			want: "push to peer git+ssh://git@dolt.example.com/org/db",
		},
		{
			// The common case: dolt already prints host-only URLs, and DNS
			// or auth errors carry no userinfo. Nothing must be altered.
			name: "credential-free dolt output is unchanged",
			in:   "fatal: repository 'https://github.com/org/repo.git/' not found",
			want: "fatal: repository 'https://github.com/org/repo.git/' not found",
		},
		{
			name: "scheme without userinfo is unchanged",
			in:   "everything up to date on https://github.com/org/repo",
			want: "everything up to date on https://github.com/org/repo",
		},
		{
			name: "ordinary diagnostic without a URL is unchanged",
			in:   "error: working set has uncommitted changes",
			want: "error: working set has uncommitted changes",
		},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scrubTransferText(tt.in); got != tt.want {
				t.Errorf("scrubTransferText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	if got := scrubTransferText(strings.Repeat("no urls here\n", 3)); got != strings.Repeat("no urls here\n", 3) {
		t.Errorf("multi-line credential-free output altered: %q", got)
	}
}