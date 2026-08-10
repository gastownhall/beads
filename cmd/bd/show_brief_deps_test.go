package main

import "testing"

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
		if got := showGetRequest("be-abc", false, false, true); !got.BriefDeps {
			t.Errorf("showGetRequest dropped BriefDeps: %+v", got)
		}
		if got := showGetRequest("be-abc", false, false, false); got.BriefDeps {
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
		got := showGetRequest("be-abc", true, true, false)
		if !got.IncludeDependents || !got.IncludeComments || got.ID != "be-abc" {
			t.Errorf("sibling fields disturbed: %+v", got)
		}
	})
}
