package main

import (
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestShowBriefDepsFlag guards the wiring for #5546: the flag must exist, must
// default to off so the JSON payload is unchanged for callers that do not ask,
// and must be read by the proxied route. The direct route and the
// proxied-server route gather flags separately, so a flag added to one reaches
// neither by implication.
func TestShowBriefDepsFlag(t *testing.T) {
	flag := showCmd.Flags().Lookup("brief-deps")
	if flag == nil {
		t.Fatal("brief-deps flag is not registered on showCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("brief-deps default = %q, want false: the default payload must not change", flag.DefValue)
	}

	setBriefDeps := func(t *testing.T, v string) {
		t.Helper()
		if err := showCmd.Flags().Set("brief-deps", v); err != nil {
			t.Fatalf("set brief-deps=%s: %v", v, err)
		}
		// pflag.Set leaves Changed true, which outlives the value reset.
		t.Cleanup(func() {
			_ = showCmd.Flags().Set("brief-deps", "false")
			showCmd.Flags().Lookup("brief-deps").Changed = false
		})
	}

	t.Run("proxied route defaults off", func(t *testing.T) {
		if in := gatherShowProxiedInput(showCmd, []string{"be-abc"}); in.detail.briefDeps {
			t.Error("brief-deps defaulted to on")
		}
	})

	t.Run("proxied route reads it", func(t *testing.T) {
		setBriefDeps(t, "true")
		if in := gatherShowProxiedInput(showCmd, []string{"be-abc"}); !in.detail.briefDeps {
			t.Error("gatherShowProxiedInput did not carry brief-deps to the proxied request")
		}
	})
}

// TestShowGetRequestCarriesBriefDeps pins the hop from the gathered flags onto
// the read contract, for both show routes. The HTTP door has the equivalent in
// TestGetIssueBriefDepsReachesTheRequest; without this, deleting either
// assignment leaves the whole suite green.
func TestShowGetRequestCarriesBriefDeps(t *testing.T) {
	t.Run("direct route", func(t *testing.T) {
		if got := showGetRequest("be-abc", showDetailFlags{briefDeps: true}); !got.BriefDeps {
			t.Errorf("showGetRequest dropped BriefDeps: %+v", got)
		}
		if got := showGetRequest("be-abc", showDetailFlags{}); got.BriefDeps {
			t.Errorf("showGetRequest invented BriefDeps: %+v", got)
		}
	})

	t.Run("proxied route", func(t *testing.T) {
		in := &showProxiedInput{detail: showDetailFlags{briefDeps: true}}
		if got := in.getRequest("be-abc"); !got.BriefDeps {
			t.Errorf("showProxiedInput.getRequest dropped BriefDeps: %+v", got)
		}
		if got := (&showProxiedInput{}).getRequest("be-abc"); got.BriefDeps {
			t.Errorf("showProxiedInput.getRequest invented BriefDeps: %+v", got)
		}
	})

	t.Run("sibling options are unaffected", func(t *testing.T) {
		got := showGetRequest("be-abc", showDetailFlags{briefDeps: true})
		if !got.IncludeDependents || !got.IncludeComments || got.ID != "be-abc" {
			t.Errorf("sibling fields disturbed: %+v", got)
		}
	})
}

// TestShowCountOnlyFlag pins the default and the opt-in fast path of the bd
// show --json detail view for both show routes (#4122): with no flags the
// request asks for dependents and comments, as v0.49.6 did; --count-only asks
// for neither; and under --count-only each --include-* re-adds its own
// relation and only that one. Without --count-only the --include-* flags
// change nothing.
func TestShowCountOnlyFlag(t *testing.T) {
	flag := showCmd.Flags().Lookup("count-only")
	if flag == nil {
		t.Fatal("count-only flag is not registered on showCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("count-only default = %q, want false: the default payload is the full detail view", flag.DefValue)
	}

	set := func(t *testing.T, names ...string) {
		t.Helper()
		for _, name := range names {
			if err := showCmd.Flags().Set(name, "true"); err != nil {
				t.Fatalf("set %s=true: %v", name, err)
			}
			// pflag.Set leaves Changed true, which outlives the value reset.
			t.Cleanup(func() {
				_ = showCmd.Flags().Set(name, "false")
				showCmd.Flags().Lookup(name).Changed = false
			})
		}
	}

	cases := []struct {
		name                       string
		flags                      []string
		wantDependents, wantCommts bool
	}{
		{"default is the full payload", nil, true, true},
		{"count-only drops both", []string{"count-only"}, false, false},
		{"count-only plus include-comments", []string{"count-only", "include-comments"}, false, true},
		{"count-only plus include-dependents", []string{"count-only", "include-dependents"}, true, false},
		{"include-comments alone changes nothing", []string{"include-comments"}, true, true},
		{"include-dependents alone changes nothing", []string{"include-dependents"}, true, true},
		{"both includes alone change nothing", []string{"include-comments", "include-dependents"}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set(t, tc.flags...)
			check := func(route string, got issueops.GetRequest) {
				t.Helper()
				if got.ID != "be-abc" || got.IncludeDependents != tc.wantDependents || got.IncludeComments != tc.wantCommts {
					t.Errorf("%s route with %v: got %+v, want IncludeDependents=%v IncludeComments=%v",
						route, tc.flags, got, tc.wantDependents, tc.wantCommts)
				}
			}
			check("direct", showGetRequest("be-abc", showDetailFlagsFrom(showCmd)))
			check("proxied", gatherShowProxiedInput(showCmd, []string{"be-abc"}).getRequest("be-abc"))
		})
	}
}
