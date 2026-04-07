package remotecache

import (
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
