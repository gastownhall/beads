package depid

import "testing"

// Golden vectors. These were computed independently with Python's
// uuid.uuid5 (which implements the same RFC-4122 v5 algorithm as
// google/uuid.NewSHA1) over the same namespace and "issue\x1ftarget" key. If
// New ever changes its namespace, separator, or encoding these will break — and
// that break is intentional, because such a change re-keys every existing edge
// and must be a conscious, migration-backed decision.
func TestNewGolden(t *testing.T) {
	cases := []struct {
		issue, target, want string
	}{
		{"issue-1", "issue-2", "91990ee3-275c-59c5-9291-30f8a8732228"},
		{"mybd-7x76", "mybd-kd8a", "c77e0366-7f45-5f8c-9f12-2c51e0a0c5fd"},
		{"a", "external:https://x/y", "0ac7aa98-536c-5bfa-9e11-df69cc67438a"},
		{"w-1", "wisp-2", "a98c6757-0779-51f0-8fc1-8d2c8be7cb97"},
	}
	for _, c := range cases {
		if got := New(c.issue, c.target); got != c.want {
			t.Errorf("New(%q,%q) = %q, want %q", c.issue, c.target, got, c.want)
		}
	}
}

// Determinism: same input -> same output, regardless of call count.
func TestNewDeterministic(t *testing.T) {
	a := New("x", "y")
	b := New("x", "y")
	if a != b {
		t.Fatalf("New not deterministic: %q != %q", a, b)
	}
}

// Distinct edges must not collide, including the boundary case where naive
// concatenation without a separator would alias ("ab"+"c" vs "a"+"bc").
func TestNewNoCollision(t *testing.T) {
	seen := map[string]string{}
	pairs := [][2]string{
		{"ab", "c"}, {"a", "bc"}, {"a", "b"}, {"b", "a"},
		{"issue-1", "issue-2"}, {"issue-1", "issue-3"},
	}
	for _, p := range pairs {
		id := New(p[0], p[1])
		if prev, ok := seen[id]; ok {
			t.Fatalf("collision: (%q,%q) and %q both -> %s", p[0], p[1], prev, id)
		}
		seen[id] = p[0] + "|" + p[1]
	}
}
