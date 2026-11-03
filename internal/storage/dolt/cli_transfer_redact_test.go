package dolt

import "testing"

// TestRedactURLCredentials pins the per-scheme rule, kept in step with
// TestRedactRemoteURL in cmd/bd: HTTP(S) userinfo is dropped whole, an SSH
// account is kept and only its password dropped, and host spelling, port and
// path are preserved.
func TestRedactURLCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "https token userinfo is dropped whole",
			in:   "https://x-access-token:ghp_TESTTOKEN@github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			name: "git+https token userinfo is dropped whole",
			in:   "git+https://x-access-token:ghp_TESTTOKEN@github.com/org/repo",
			want: "git+https://github.com/org/repo",
		},
		{
			// A token can be the username alone.
			name: "token as username is dropped",
			in:   "https://ghp_TESTTOKEN@github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			name: "http basic auth is dropped, port kept",
			in:   "http://user:pass@myserver:7007/mydb",
			want: "http://myserver:7007/mydb",
		},
		{
			// The authority splits at its last "@", so an unencoded "@" in the
			// password does not leave a password fragment behind.
			name: "unencoded at sign in password is dropped",
			in:   "https://user:p@ss@github.com/org/repo",
			want: "https://github.com/org/repo",
		},
		{
			name: "host case is preserved",
			in:   "https://u:p@GitHub.COM/Org/Repo",
			want: "https://GitHub.COM/Org/Repo",
		},
		{
			name: "no path",
			in:   "https://u:p@github.com",
			want: "https://github.com",
		},
		{
			name: "git+ssh account is kept",
			in:   "git+ssh://git@github.com/org/repo.git",
			want: "git+ssh://git@github.com/org/repo.git",
		},
		{
			name: "ssh password is dropped, account and port kept",
			in:   "ssh://alice:hunter2@my-dolt.example.com:2222/org/db",
			want: "ssh://alice@my-dolt.example.com:2222/org/db",
		},
		{
			name: "git+ssh password is dropped, account kept",
			in:   "git+ssh://git:hunter2@github.com/org/repo.git",
			want: "git+ssh://git@github.com/org/repo.git",
		},
		{name: "scp style is untouched", in: "git@github.com:org/repo.git", want: "git@github.com:org/repo.git"},
		{name: "plain remote name", in: "origin", want: "origin"},
		{name: "no userinfo", in: "https://github.com/org/repo.git", want: "https://github.com/org/repo.git"},
		{name: "at sign in path only", in: "https://github.com/org/repo@v1", want: "https://github.com/org/repo@v1"},
		{name: "dolt native", in: "dolthub://myorg/mydb", want: "dolthub://myorg/mydb"},
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

// TestScrubTransferText covers credential-bearing URLs embedded in dolt's
// output or an operation, kept in step with TestScrubURLCredentials in cmd/bd.
// Credential-free diagnostics must pass through untouched.
func TestScrubTransferText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single quoted rejection URL",
			in:   "error: failed to push some refs to 'git+https://x-access-token:ghp_TESTTOKEN@github.com/org/repo'",
			want: "error: failed to push some refs to 'git+https://github.com/org/repo'",
		},
		{
			name: "double quoted URL",
			in:   `remote "https://u:p@github.com/org/repo" not reachable`,
			want: `remote "https://github.com/org/repo" not reachable`,
		},
		{
			name: "ssh password embedded in text is dropped, account kept",
			in:   "note: not cloning ssh://alice:hunter2@dolt.example.com/org/db right now",
			want: "note: not cloning ssh://alice@dolt.example.com/org/db right now",
		},
		{
			name: "two credential URLs in one text are both scrubbed",
			in:   "To https://u:p@a.example.com:8443/x\nfrom git+ssh://git:q@b.example.com/y",
			want: "To https://a.example.com:8443/x\nfrom git+ssh://git@b.example.com/y",
		},
		{
			name: "credential-free rejection is unchanged",
			in:   "To git+https://github.com/org/repo\n ! [rejected]        main -> main (non-fast-forward)",
			want: "To git+https://github.com/org/repo\n ! [rejected]        main -> main (non-fast-forward)",
		},
		{
			name: "scp style in text is unchanged",
			in:   "fatal: git@github.com:org/repo.git does not appear to be a git repository",
			want: "fatal: git@github.com:org/repo.git does not appear to be a git repository",
		},
		{
			name: "plain diagnostic without a URL is unchanged",
			in:   "fatal: Could not read from remote repository.",
			want: "fatal: Could not read from remote repository.",
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
}
