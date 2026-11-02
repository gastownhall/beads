package peercred

import "testing"

// AC-4: the live remote passes only when it is the stored destination or the
// URL Dolt's DOLT_REMOTE('add') records for it; every other difference fails.
func TestRemoteURLMatches(t *testing.T) {
	cases := []struct {
		name, stored, live string
		want               bool
	}{
		{"identical https", "https://peer.example/db", "https://peer.example/db", true},
		{"identical native scheme", "dolthub://org/repo", "dolthub://org/repo", true},
		{"https .git read-back", "https://peer.example/org/repo.git", "git+https://peer.example/org/repo.git", true},
		{"http .git read-back", "http://peer.example/org/repo.git", "git+http://peer.example/org/repo.git", true},
		{"ssh .git read-back", "ssh://git@peer.example:2222/org/repo.git", "git+ssh://git@peer.example:2222/org/repo.git", true},
		{"file .git read-back", "file:///srv/peer/repo.git", "git+file:///srv/peer/repo.git", true},
		{"scp relative read-back", "git@peer.example:org/repo.git", "git+ssh://git@peer.example/./org/repo.git", true},
		{"scp absolute read-back", "git@peer.example:/srv/repo.git", "git+ssh://git@peer.example/srv/repo.git", true},
		{"scp dotted host read-back", "peer.example:org/repo.git", "git+ssh://peer.example/./org/repo.git", true},
		{"native owner/repo read-back", "org/repo", "https://doltremoteapi.dolthub.com/org/repo", true},
		{"schemeless sql-server read-back", "192.168.1.100:3306/beads", "https://192.168.1.100:3306/beads", true},
		{"schemeless dotted host read-back", "peer.example/db", "https://peer.example/db", true},
		{"schemeless localhost port read-back", "localhost:50051/beads", "https://localhost:50051/beads", true},
		{"schemeless localhost is a host, not an owner", "localhost/repo", "https://localhost/repo", true},

		{"empty stored", "", "", false},
		{"empty live", "https://peer.example/db", "", false},
		{"different host", "https://peer.example/db", "https://evil.example/db", false},
		{"different path", "https://peer.example/db", "https://peer.example/db2", false},
		{"different port", "https://peer.example/db", "https://peer.example:8443/db", false},
		{"different scheme", "https://peer.example/db", "http://peer.example/db", false},
		{"host case", "https://peer.example/db", "https://PEER.example/db", false},
		{"path case", "https://peer.example/db", "https://peer.example/DB", false},
		{"added query", "https://peer.example/db", "https://peer.example/db?x=1", false},
		{"added fragment", "https://peer.example/db", "https://peer.example/db#x", false},
		{"trailing slash", "https://peer.example/db", "https://peer.example/db/", false},
		{".git added", "https://peer.example/org/repo", "https://peer.example/org/repo.git", false},
		{".git dropped", "https://peer.example/org/repo.git", "git+https://peer.example/org/repo", false},
		{".git read-back to another host", "https://peer.example/org/repo.git", "git+https://evil.example/org/repo.git", false},
		{"git+ prefix without .git", "https://peer.example/db", "git+https://peer.example/db", false},
		{"scp account case", "git@peer.example:org/repo.git", "git+ssh://GIT@peer.example/./org/repo.git", false},
		{"scp other account", "git@peer.example:org/repo.git", "git+ssh://evil@peer.example/./org/repo.git", false},
		{"scp relative as absolute", "git@peer.example:org/repo.git", "git+ssh://git@peer.example/org/repo.git", false},
		{"scp undotted host", "git@peer:org/repo.git", "git+ssh://git@peer/./org/repo.git", true},
		{"scp host without dot or account", "peer:org/repo.git", "git+ssh://peer/./org/repo.git", false},
		{"native owner case", "org/repo", "https://doltremoteapi.dolthub.com/Org/repo", false},
		{"native other remotesapi host", "org/repo", "https://remotes.example/org/repo", false},
		{"dotted schemeless host is not native", "peer.example/repo", "https://doltremoteapi.dolthub.com/peer.example/repo", false},
		{"schemeless localhost is not native", "localhost/repo", "https://doltremoteapi.dolthub.com/localhost/repo", false},
		{"schemeless read-back to another host", "192.168.1.100:3306/beads", "https://192.168.1.101:3306/beads", false},
		{"schemeless read-back to another port", "192.168.1.100:3306/beads", "https://192.168.1.100:3307/beads", false},
		{"schemeless read-back over http", "peer.example/db", "http://peer.example/db", false},
		{"schemeless read-back with a trailing slash", "peer.example/db", "https://peer.example/db/", false},
		{"schemeless port out of range not proven", "peer.example:65536/db", "https://peer.example:65536/db", false},
		{"schemeless account not proven", "user@peer.example/db", "https://user@peer.example/db", false},
		{"schemeless query not proven", "peer.example/db?x=1", "https://peer.example/db?x=1", false},
		{"schemeless undotted host is native, not a host", "peer/db", "https://peer/db", false},
		{"uppercase scheme not proven", "HTTPS://peer.example/org/repo.git", "git+https://peer.example/org/repo.git", false},
		{"surrounding space not proven", " https://peer.example/org/repo.git", "git+https://peer.example/org/repo.git", false},
		{"relative file path not proven", "./peer/repo.git", "git+file:///work/peer/repo.git", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RemoteURLMatches(c.stored, c.live); got != c.want {
				t.Errorf("RemoteURLMatches(%q, %q) = %v, want %v", c.stored, c.live, got, c.want)
			}
		})
	}
}
