package fix

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestFixDependencyTargetExprMatchesCanonical(t *testing.T) {
	if fixDependencyTargetExpr != issueops.DepTargetExpr {
		t.Fatalf("fixDependencyTargetExpr = %q, want %q", fixDependencyTargetExpr, issueops.DepTargetExpr)
	}
}

// TestIsUnverifiableDepTarget pins the one rule the orphan check, its --fix
// and the --deep integrity check share (#4647): only an external: ref, or a
// target whose prefix differs from its source's and is not held here, names
// something this database cannot hold a row for.
func TestIsUnverifiableDepTarget(t *testing.T) {
	held := []string{"bd", "zz"}
	cases := []struct {
		name           string
		source, target string
		held           []string
		want           bool
	}{
		{"external ref", "bd-a", "external:proj:cap", held, true},
		{"external ref with hyphen", "bd-a", "external:my-proj:cap", held, true},
		{"another repository", "bd-a", "other-x", held, true},
		{"another repository, nothing held", "bd-a", "other-x", nil, true},
		{"same prefix", "bd-a", "bd-gone", held, false},
		{"same prefix, not held", "old-a", "old-gone", held, false},
		{"held allowed prefix", "bd-a", "zz-b", held, false},
		{"held prefix of the source's own repository", "zz-a", "bd-b", held, false},
		{"held prefix containing a hyphen", "bd-a", "my-proj-x", []string{"bd", "my-proj"}, false},
		{"no hyphen", "bd-a", "gone", held, false},
		{"empty target", "bd-a", "", held, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsUnverifiableDepTarget(c.source, c.target, c.held); got != c.want {
				t.Fatalf("IsUnverifiableDepTarget(%q, %q, %q) = %v, want %v", c.source, c.target, c.held, got, c.want)
			}
		})
	}
}
