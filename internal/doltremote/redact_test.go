package doltremote

import (
	"errors"
	"fmt"
	"testing"
)

// TestRedact pins the per-scheme rule: HTTP(S) userinfo is dropped whole, an
// SSH account is kept and only its password dropped, and host spelling, port
// and path are preserved. cmd/bd's TestRedactRemoteURL reaches the same rule
// through redactRemoteURL.
func TestRedact(t *testing.T) {
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
			name: "apostrophe in password is dropped",
			in:   "git+https://user:canary'secret@github.com/org/repo",
			want: "git+https://github.com/org/repo",
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
		{name: "file remote", in: "file:///tmp/beads-remote", want: "file:///tmp/beads-remote"},
		{name: "ssh without password and with port is unchanged", in: "ssh://git@github.com:2222/org/repo", want: "ssh://git@github.com:2222/org/repo"},
		{name: "empty", in: "", want: ""},
		// Cases from the contributor's table in PR #7644.
		{name: "pr: git+https token", in: "git+https://x-access-token:SECRET@github.com/o/r", want: "git+https://github.com/o/r"},
		{name: "pr: https user only", in: "https://SECRET@github.com/o/r.git", want: "https://github.com/o/r.git"},
		{name: "pr: ssh keeps account", in: "ssh://git:SECRET@host.example/o/r", want: "ssh://git@host.example/o/r"},
		{name: "pr: git+ssh keeps account", in: "git+ssh://git@host.example/o/r", want: "git+ssh://git@host.example/o/r"},
		{name: "pr: scp-style untouched", in: "git@github.com:o/r.git", want: "git@github.com:o/r.git"},
		{name: "pr: no userinfo untouched", in: "https://github.com/o/r@v1", want: "https://github.com/o/r@v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.in); got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRedactText covers credential-bearing URLs embedded in git or Dolt
// output, an operation or an error; cmd/bd's TestScrubURLCredentials reaches
// the same rule through scrubURLCredentials. Credential-free diagnostics must
// pass through untouched.
func TestRedactText(t *testing.T) {
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
			// The userinfo run used to stop at the quote, so this matched
			// nothing and the whole password was echoed.
			name: "apostrophe in a quoted URL's password",
			in:   "error: failed to push some refs to 'git+https://user:canary'secret@github.com/org/repo'",
			want: "error: failed to push some refs to 'git+https://github.com/org/repo'",
		},
		{
			name: "apostrophe in a quoted URL's password, no path",
			in:   "remote 'https://user:it's@github.com' could not be accessed",
			want: "remote 'https://github.com' could not be accessed",
		},
		{
			name: "double quote in a password",
			in:   `remote "https://user:pa"ss@github.com/org/repo" not reachable`,
			want: `remote "https://github.com/org/repo" not reachable`,
		},
		{
			name: "apostrophe in an ssh password, account kept",
			in:   "cannot reach 'ssh://alice:it's@dolt.example.com/org/db'",
			want: "cannot reach 'ssh://alice@dolt.example.com/org/db'",
		},
		{
			// Dolt's procedure errors: HandleInitRemoteStorageClientErr quotes
			// the stored URL, and a failed remote read names it after the
			// remote and an "@".
			name: "dolt remote access error",
			in:   "failed to get remote db; the remote: origin 'git+https://x-access-token:ghp_TESTTOKEN@127.0.0.1:9/org/repo' could not be accessed; exit status 128",
			want: "failed to get remote db; the remote: origin 'git+https://127.0.0.1:9/org/repo' could not be accessed; exit status 128",
		},
		{
			name: "dolt remote read error",
			in:   "failed to read latest version of remote database origin@git+https://x-access-token:ghp_TESTTOKEN@github.com/org/repo: EOF",
			want: "failed to read latest version of remote database origin@git+https://github.com/org/repo: EOF",
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
			// The userinfo run stops at the first "/", so a credential-free URL
			// cannot swallow the credentials of the URL that follows it.
			name: "credential URL after a credential-free one with no space between",
			in:   "'https://a.example.com/x','https://v:q@b.example.com/y'",
			want: "'https://a.example.com/x','https://b.example.com/y'",
		},
		{
			name: "credential URL after a credential-free one in a comma list",
			in:   "https://a.example.com/x,https://v:q@b.example.com/y",
			want: "https://a.example.com/x,https://b.example.com/y",
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
		{
			name: "two credential URLs on one line are both scrubbed",
			in:   "https://u:p@a.example.com/x and https://v:q@b.example.com/y",
			want: "https://a.example.com/x and https://b.example.com/y",
		},
		{
			name: "peer operation description is scrubbed",
			in:   "push to peer git+ssh://git:hunter2@dolt.example.com/org/db",
			want: "push to peer git+ssh://git@dolt.example.com/org/db",
		},
		{
			name: "multi-line credential-free output is unchanged",
			in:   "no urls here\nno urls here\nno urls here\n",
			want: "no urls here\nno urls here\nno urls here\n",
		},
		{name: "empty", in: "", want: ""},
		// Cases from the contributor's table in PR #7644.
		{name: "pr: quoted in prose", in: "failed to push some refs to 'https://u:SECRET@h.example/r'", want: "failed to push some refs to 'https://h.example/r'"},
		{name: "pr: two urls", in: "a https://u:S1@h1/x b https://u:S2@h2/y", want: "a https://h1/x b https://h2/y"},
		{name: "pr: at sign in path only is untouched", in: "https://github.com/o/r@v1", want: "https://github.com/o/r@v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactText(tt.in); got != tt.want {
				t.Errorf("RedactText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// stubCause is a typed cause for asserting that a redacted error still
// unwraps to it.
type stubCause struct{ msg string }

func (c *stubCause) Error() string { return c.msg }

// TestRedactError pins that an error quoting a credential-bearing URL reads
// redacted while errors.Is and errors.As still reach its cause, and that an
// error with nothing to redact comes back as itself.
func TestRedactError(t *testing.T) {
	if got := RedactError(nil); got != nil {
		t.Errorf("RedactError(nil) = %v, want nil", got)
	}

	clean := errors.New("push to origin/main: remote 'origin' not found")
	if got := RedactError(clean); got != clean {
		t.Errorf("RedactError(credential-free) = %#v, want the same error back", got)
	}

	cause := &stubCause{"the remote: origin 'git+https://x-access-token:ghp_TESTTOKEN@github.com/org/repo' could not be accessed"}
	err := RedactError(fmt.Errorf("push to origin/main: %w", cause))
	const want = "push to origin/main: the remote: origin 'git+https://github.com/org/repo' could not be accessed"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) = false: %v", err)
	}
	var unwrapped *stubCause
	if !errors.As(err, &unwrapped) || unwrapped != cause {
		t.Errorf("errors.As did not recover the cause: %v", err)
	}
	if wrapped := fmt.Errorf("push: %w", err); wrapped.Error() != "push: "+want {
		t.Errorf("an outer wrap reads %q, want the redacted text", wrapped)
	}
}
