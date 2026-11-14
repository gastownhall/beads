package remotecache

import (
	"errors"
	"strings"
	"testing"
)

// GH#6233: an absent or empty allowlist is no policy at all. CheckRemotePolicy
// must refuse nothing, so every remote a workspace without patterns could use
// before stays usable, while the structural checks those call sites already
// run (ValidateRemoteURL) keep refusing what they refused.
func TestCheckRemotePolicyEmptyListIsNoPolicy(t *testing.T) {
	for _, patterns := range [][]string{nil, {}} {
		for _, u := range []string{
			"dolthub://org/repo",
			"https://github.com/org/repo",
			"git@github.com:org/repo.git",
			"file:///tmp/remote",
			"",
		} {
			if err := CheckRemotePolicy(RemoteSubject("origin"), u, patterns); err != nil {
				t.Errorf("CheckRemotePolicy(%q, %#v) = %v, want nil (no allowlist)", u, patterns, err)
			}
		}
	}
	// Structural validation is unchanged and still refuses option-shaped URLs.
	if err := ValidateRemoteURL("--upload-pack=touch /tmp/x"); err == nil {
		t.Fatal("ValidateRemoteURL accepted an option-shaped URL; structural validation must stay intact")
	}
}

func TestCheckRemotePolicyTable(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		patterns []string
		allowed  bool
	}{
		{"native match", "dolthub://myorg/beads", []string{"dolthub://myorg/*"}, true},
		{"native mismatch", "dolthub://other/beads", []string{"dolthub://myorg/*"}, false},
		{"file match", "file:///srv/remotes/beads", []string{"file:///srv/remotes/*"}, true},
		{"star does not cross slash", "dolthub://myorg/team/beads", []string{"dolthub://myorg/*"}, false},
		{"second entry matches", "dolthub://myorg/beads", []string{"s3://*", "dolthub://myorg/*"}, true},
		{"malformed entry matches nothing", "dolthub://myorg/beads", []string{"dolthub://myorg/["}, false},
		{"malformed entry beside a valid one", "dolthub://myorg/beads", []string{"dolthub://myorg/[", "dolthub://myorg/*"}, true},
		{"blank-only list denies all", "dolthub://myorg/beads", []string{""}, false},
		{"typed forge URL is evaluated in git+ form", "https://github.com/org/repo", []string{"git+https://github.com/org/*"}, true},
		{"https pattern matches the forge destination without git+", "https://github.com/org/repo", []string{"https://github.com/org/*"}, true},
		{"https pattern matches a registered git+ forge remote", "git+https://github.com/org/repo", []string{"https://github.com/org/*"}, true},
		{"https pattern still bounds the org", "git+https://github.com/evil/repo", []string{"https://github.com/org/*"}, false},
		{"registered git+ form", "git+https://github.com/org/repo", []string{"git+https://github.com/org/*"}, true},
		{"other org refused", "git+https://github.com/evil/repo", []string{"git+https://github.com/org/*"}, false},
		{"scp form normalized", "git@github.com:org/repo.git", []string{"git+ssh://git@github.com/org/*"}, true},
		{"ssh form normalized", "ssh://git@github.com/org/repo.git", []string{"git+ssh://git@github.com/org/*"}, true},
		{"ssh pattern matches an scp remote", "git@github.com:org/repo.git", []string{"ssh://git@github.com/org/*"}, true},
		// GH#3339: bd registers a Dolt remotesapi endpoint unchanged, so a
		// pattern written the way `bd dolt remote list` shows it must match.
		{"remotesapi https as registered", "https://doltremoteapi.dolthub.com/myorg/beads", []string{"https://doltremoteapi.dolthub.com/myorg/*"}, true},
		{"remotesapi https other org refused", "https://doltremoteapi.dolthub.com/evil/beads", []string{"https://doltremoteapi.dolthub.com/myorg/*"}, false},
		{"sql-server http as registered", "http://myserver:7007/mydb", []string{"http://myserver:7007/*"}, true},
		{"http pattern does not match https", "https://myserver:7007/mydb", []string{"http://myserver:7007/*"}, false},
		{"git+file matches a file pattern", "git+file:///srv/approved/repo", []string{"file:///srv/approved/*"}, true},
		// A glob does not bound a query, a fragment or a ".." segment: only an
		// entry equal to the whole URL allows one.
		{"query routes an s3 remote past a glob", "s3://corp-bucket/beads?endpoint=https%3A%2F%2Fattacker.example", []string{"s3://corp-bucket/*"}, false},
		{"query allowed by an entry equal to the URL", "s3://corp-bucket/beads?endpoint=https://minio.corp.example", []string{"s3://corp-bucket/beads?endpoint=https://minio.corp.example"}, true},
		{"query refused by a glob over its value", "s3://corp-bucket/beads?endpoint=https://minio.corp.example", []string{"s3://corp-bucket/beads?endpoint=*"}, false},
		{"empty query refused by a glob", "s3://corp-bucket/beads?", []string{"s3://corp-bucket/*"}, false},
		{"fragment refused by a glob", "file:///srv/approved/beads#x", []string{"file:///srv/approved/*"}, false},
		{"dot-dot escapes the prefix", "file:///srv/approved/..", []string{"file:///srv/approved/*"}, false},
		{"dot-dot under dolthub", "dolthub://myorg/..", []string{"dolthub://myorg/*"}, false},
		{"encoded dot-dot", "file:///srv/approved/%2e%2e", []string{"file:///srv/approved/*"}, false},
		{"twice-encoded dot-dot", "file:///srv/approved/%252e%252e", []string{"file:///srv/approved/*"}, false},
		{"encoded slashes around dot-dot", "file:///srv/approved/x%2F..%2F..", []string{"file:///srv/approved/*"}, false},
		{"backslashes around dot-dot", `file:///srv/approved/x\..\..`, []string{"file:///srv/approved/*"}, false},
		{"dot-dot allowed by an entry equal to the URL", "file:///srv/approved/..", []string{"file:///srv/approved/.."}, true},
		{"dots inside a name are a name", "file:///srv/approved/beads..v2", []string{"file:///srv/approved/*"}, true},
		// A bracketed host is glob syntax to path.Match. Dolt's
		// aws://[bucket:table] host is not a URL net/url accepts since Go
		// 1.25.2, but it is the one Dolt dials: an entry equal to the URL or
		// a glob that escapes the brackets allows it.
		{"aws remote allowed by an entry equal to it", "aws://[tbl:bucket]/db", []string{"aws://[tbl:bucket]/db"}, true},
		{"aws remote allowed by an escaped glob", "aws://[tbl:bucket]/db", []string{`aws://\[tbl:bucket\]/*`}, true},
		{"aws remote allowed by a glob over any host", "aws://[tbl:bucket]/db", []string{"aws://*/*"}, true},
		{"aws remote under another table refused", "aws://[tbl:other]/db", []string{`aws://\[tbl:bucket\]/*`}, false},
		{"ipv6 literal allowed by an entry equal to it", "http://[::1]:7007/db", []string{"http://[::1]:7007/db"}, true},
		{"ipv6 literal allowed by an escaped glob", "http://[::1]:7007/db", []string{`http://\[::1\]:7007/*`}, true},
		{"unescaped brackets in a glob are a character class", "http://[::1]:7007/db", []string{"http://[::1]:7007/*"}, false},
		// A wildcard does not reach past the '@' that ends user info: the
		// host after it is where Dolt sends the data.
		{"host glob does not reach past user info", "gs://corp-x@evil/db", []string{"gs://corp-*/*"}, false},
		{"https host glob does not reach past user info", "https://git.corp.example@evil.example/org/repo", []string{"https://git.corp*/org/*"}, false},
		{"suffix host glob refuses user info", "gs://x@evil/db", []string{"gs://*.corp/*"}, false},
		{"question mark does not match the at sign", "gs://corp@evil/db", []string{"gs://corp?evil/*"}, false},
		{"glob without a scheme separator does not reach past user info", "gs://x@evil/db", []string{"gs:/*/*/*"}, false},
		{"aws bucket glob does not reach past user info", "aws://[corp-x@evil:tbl]/db", []string{`aws://\[corp-*:tbl\]/*`}, false},
		{"host glob still allows a bucket without user info", "gs://corp-x/db", []string{"gs://corp-*/*"}, true},
		{"pattern spelling the user allows user info", "https://x-access-token:tok@github.com/org/repo", []string{"https://*@github.com/org/*"}, true},
		{"escaped at sign spells the user", "https://deploy@github.com/org/repo", []string{`https://deploy\@github.com/org/*`}, true},
		{"user glob does not stretch over a second at sign", "https://u@git.corp.example@evil.example/org/repo", []string{"https://*@git.corp*/org/*"}, false},
		{"user info allowed by an entry equal to the URL", "gs://corp-x@evil/db", []string{"gs://corp-x@evil/db"}, true},
		{"at sign in a path is a name", "file:///srv/approved/a@b", []string{"file:///srv/approved/*"}, true},
		{"unresolved URL refused", "", []string{"dolthub://*"}, false},
		{"structurally invalid refused", "dolthub://\x00evil", []string{"*"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRemotePolicy(RemoteSubject("origin"), tt.url, tt.patterns)
			if tt.allowed {
				if err != nil {
					t.Fatalf("CheckRemotePolicy(%q, %q) = %v, want allowed", tt.url, tt.patterns, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckRemotePolicy(%q, %q) = nil, want a refusal", tt.url, tt.patterns)
			}
			if !errors.Is(err, ErrRemoteNotAllowed) {
				t.Fatalf("refusal %v does not wrap ErrRemoteNotAllowed", err)
			}
		})
	}
}

// A refusal names the subject and the key, never the URL (which can carry a
// token) or the patterns.
func TestCheckRemotePolicyDiagnosticIsSecretFree(t *testing.T) {
	for _, tc := range []struct {
		name, url, pattern string
		leaks              []string
	}{
		{"no pattern matches", "https://x-access-token:s3cr3t@github.com/evil/repo", "git+https://github.com/private-org/*", []string{"s3cr3t", "x-access-token", "evil/repo", "private-org"}},
		{"query under a glob", "s3://corp-bucket/beads?endpoint=https%3A%2F%2Fs3cr3t.example", "s3://corp-bucket/*", []string{"s3cr3t", "corp-bucket", "endpoint"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckRemotePolicy(RemoteSubject("origin"), tc.url, []string{tc.pattern})
			if err == nil {
				t.Fatal("want a refusal")
			}
			msg := err.Error()
			for _, leak := range tc.leaks {
				if strings.Contains(msg, leak) {
					t.Errorf("refusal %q leaks %q", msg, leak)
				}
			}
			for _, want := range []string{`remote "origin"`, AllowedRemotePatternsKey} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not name %q", msg, want)
				}
			}
		})
	}
}
