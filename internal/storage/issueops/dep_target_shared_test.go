package issueops

import (
	"testing"
)

func TestDepTargetKindFromPresence(t *testing.T) {
	for _, tc := range []struct {
		name, source, target    string
		issueExists, wispExists bool
		want                    DepTargetKind
	}{
		{"local foreign-prefix issue", "a-queue", "b-blocker", true, false, DepTargetIssue},
		{"local foreign-prefix wisp", "a-queue", "b-wisp", false, true, DepTargetWisp},
		{"absent foreign-prefix", "a-queue", "t-other", false, false, DepTargetExternal},
		{"explicit external wins", "a-queue", "external:GH-42", true, false, DepTargetExternal},
		{"same-prefix local", "a-queue", "a-blocker", true, false, DepTargetIssue},
		{"same-prefix missing uses FK", "a-queue", "a-missing", false, false, DepTargetIssue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DepTargetKindFromPresence(tc.source, tc.target, tc.issueExists, tc.wispExists)
			if got != tc.want {
				t.Fatalf("DepTargetKindFromPresence(%q, %q) = %v; want %v", tc.source, tc.target, got, tc.want)
			}
		})
	}
}
