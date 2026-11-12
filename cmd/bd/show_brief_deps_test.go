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
		if in := gatherShowProxiedInput(showCmd, []string{"be-abc"}); in.briefDeps {
			t.Error("brief-deps defaulted to on")
		}
	})

	t.Run("proxied route reads it", func(t *testing.T) {
		setBriefDeps(t, "true")
		if in := gatherShowProxiedInput(showCmd, []string{"be-abc"}); !in.briefDeps {
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
		in := &showProxiedInput{briefDeps: true}
		if got := in.getRequest("be-abc"); !got.BriefDeps {
			t.Errorf("showProxiedInput.getRequest dropped BriefDeps: %+v", got)
		}
		if got := (&showProxiedInput{}).getRequest("be-abc"); got.BriefDeps {
			t.Errorf("showProxiedInput.getRequest invented BriefDeps: %+v", got)
		}
	})

	t.Run("sibling options are unaffected", func(t *testing.T) {
		got := showGetRequest("be-abc", showDetailFlags{includeDependents: true, includeComments: true})
		if !got.IncludeDependents || !got.IncludeComments || got.ID != "be-abc" {
			t.Errorf("sibling fields disturbed: %+v", got)
		}
	})
}

// TestShowCountOnlyFlag guards the wiring for #4122 (bd-b8tzlr): plain
// `bd show --json` must keep emitting the full detail payload — dependents and
// comments arrays, the v0.49.6 contract — and the count-only view PR #4010
// introduced must be an explicit opt-in. The direct route and the
// proxied-server route gather flags separately, so the default must be
// carried to both, and --include-* must re-add one relation only under
// --count-only.
func TestShowCountOnlyFlag(t *testing.T) {
	flag := showCmd.Flags().Lookup("count-only")
	if flag == nil {
		t.Fatal("count-only flag is not registered on showCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("count-only default = %q, want false: the full payload must be the default", flag.DefValue)
	}

	// pflag.Set leaves Changed true, which outlives the value reset, so every
	// case resets what it touched (the TestShowBriefDepsFlag pattern).
	setFlag := func(t *testing.T, name, v string) {
		t.Helper()
		if err := showCmd.Flags().Set(name, v); err != nil {
			t.Fatalf("set %s=%s: %v", name, v, err)
		}
		t.Cleanup(func() {
			_ = showCmd.Flags().Set(name, "false")
			showCmd.Flags().Lookup(name).Changed = false
		})
	}

	// want names the request a flag combination must produce.
	want := func(deps, comments bool) issueops.GetRequest {
		return issueops.GetRequest{ID: "be-abc", IncludeDependents: deps, IncludeComments: comments}
	}

	t.Run("both routes default to the full payload", func(t *testing.T) {
		in := gatherShowProxiedInput(showCmd, []string{"be-abc"})
		if got := in.getRequest("be-abc"); got != want(true, true) {
			t.Errorf("proxied route default request: %+v, want the full payload", got)
		}
		if got := showGetRequest("be-abc", showDetailFlagsFrom(showCmd)); got != want(true, true) {
			t.Errorf("direct route default request: %+v, want the full payload", got)
		}
	})

	t.Run("count-only drops both arrays", func(t *testing.T) {
		setFlag(t, "count-only", "true")
		in := gatherShowProxiedInput(showCmd, []string{"be-abc"})
		if got := in.getRequest("be-abc"); got != want(false, false) {
			t.Errorf("proxied route --count-only request: %+v", got)
		}
		if got := showGetRequest("be-abc", showDetailFlagsFrom(showCmd)); got != want(false, false) {
			t.Errorf("direct route --count-only request: %+v", got)
		}
	})

	t.Run("count-only plus include-comments adds comments only", func(t *testing.T) {
		setFlag(t, "count-only", "true")
		setFlag(t, "include-comments", "true")
		in := gatherShowProxiedInput(showCmd, []string{"be-abc"})
		if got := in.getRequest("be-abc"); got != want(false, true) {
			t.Errorf("proxied route --count-only --include-comments request: %+v", got)
		}
		if got := showGetRequest("be-abc", showDetailFlagsFrom(showCmd)); got != want(false, true) {
			t.Errorf("direct route --count-only --include-comments request: %+v", got)
		}
	})

	t.Run("count-only plus include-dependents adds dependents only", func(t *testing.T) {
		setFlag(t, "count-only", "true")
		setFlag(t, "include-dependents", "true")
		in := gatherShowProxiedInput(showCmd, []string{"be-abc"})
		if got := in.getRequest("be-abc"); got != want(true, false) {
			t.Errorf("proxied route --count-only --include-dependents request: %+v", got)
		}
		if got := showGetRequest("be-abc", showDetailFlagsFrom(showCmd)); got != want(true, false) {
			t.Errorf("direct route --count-only --include-dependents request: %+v", got)
		}
	})

	t.Run("include flags alone change nothing", func(t *testing.T) {
		setFlag(t, "include-comments", "true")
		setFlag(t, "include-dependents", "true")
		if got := showGetRequest("be-abc", showDetailFlagsFrom(showCmd)); got != want(true, true) {
			t.Errorf("--include-* without --count-only: %+v, want the default full payload", got)
		}
	})
}
