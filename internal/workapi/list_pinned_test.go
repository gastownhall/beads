package workapi

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestPinnedDefaultBySelector owns the rule that decides whether a listing
// forces Pinned=false: a selector that ASKS for pinned-carrying beads must not
// then have them filtered back out.
//
// The old builder compared the raw --status string to "pinned"/"hooked", so
// two selectors that do ask for those beads were still getting the exclusion:
// "all", which promises every status, and any multi-status set containing
// pinned or hooked ("pinned,closed"), which never matched the two string
// equalities. Both now lift the default. This is the observable behavior
// change riding on this filter work — scripts that relied on --status=all
// hiding pinned beads will see them appear — so it is pinned HERE, not left to
// the golden table.
func TestPinnedDefaultBySelector(t *testing.T) {
	var cfg ListConfig

	// pinnedDefault reports filter.Pinned as a tri-state: nil (no constraint,
	// i.e. pinned beads are admitted) or the forced value.
	pinnedDefault := func(t *testing.T, in issueops.ListRequest) *bool {
		t.Helper()
		filter, err := BuildListFilter(in, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter(%+v): %v", in, err)
		}
		return filter.Pinned
	}

	t.Run("selectors that ask for pinned beads lift the default", func(t *testing.T) {
		for _, status := range []string{"pinned", "hooked", "all", "pinned,closed", "closed,hooked", " all "} {
			if got := pinnedDefault(t, issueops.ListRequest{Status: status}); got != nil {
				t.Errorf("--status=%q: Pinned = %v, want nil (no pinned exclusion)", status, *got)
			}
		}
	})

	t.Run("every other selector keeps the exclusion", func(t *testing.T) {
		for _, status := range []string{"", "open", "closed", "open,closed", "in_progress"} {
			got := pinnedDefault(t, issueops.ListRequest{Status: status})
			if got == nil || *got {
				t.Errorf("--status=%q: Pinned = %v, want false", status, got)
			}
		}
	})

	t.Run("--no-pinned wins over the selector", func(t *testing.T) {
		for _, status := range []string{"pinned", "all", "pinned,closed"} {
			got := pinnedDefault(t, issueops.ListRequest{Status: status, NoPinnedFlag: true})
			if got == nil || *got {
				t.Errorf("--status=%q --no-pinned: Pinned = %v, want false", status, got)
			}
		}
	})

	t.Run("--pinned forces pinned-only regardless of selector", func(t *testing.T) {
		got := pinnedDefault(t, issueops.ListRequest{Status: "all", PinnedFlag: true})
		if got == nil || !*got {
			t.Errorf("--status=all --pinned: Pinned = %v, want true", got)
		}
	})

	// --ready forces status open and otherwise IGNORES the selector, so an
	// "all" that the query never applies must not have a pinned side effect
	// either. Without this, `bd list --ready --status=all` would admit pinned
	// beads into a ready listing that is filtered to open.
	t.Run("--ready ignores the selector, including for pinned", func(t *testing.T) {
		got := pinnedDefault(t, issueops.ListRequest{Status: "all", ReadyFlag: true})
		if got == nil || *got {
			t.Errorf("--ready --status=all: Pinned = %v, want false", got)
		}
	})
}

// TestIncludeAllTypesLiftsEverySuppression pins IncludeAllTypes as the union of
// the type knobs AND the plane knob: nothing is hidden for being a template, a
// gate, an infra type, or a wisp. It is the guarantee `bd human list` rests on.
func TestIncludeAllTypesLiftsEverySuppression(t *testing.T) {
	cfg := ListConfig{}

	t.Run("lifts type exclusions and admits the ephemeral plane", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{IncludeAllTypes: true}, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(filter.ExcludeTypes) != 0 {
			t.Errorf("ExcludeTypes = %v, want none", filter.ExcludeTypes)
		}
		if filter.IsTemplate != nil {
			t.Errorf("IsTemplate = %v, want nil (templates not hidden)", *filter.IsTemplate)
		}
		if filter.SkipWisps {
			t.Error("SkipWisps = true, want false: IncludeAllTypes must admit the ephemeral plane")
		}
	})

	t.Run("without it the default listing still suppresses", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{}, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(filter.ExcludeTypes) == 0 {
			t.Error("default listing should exclude gate/infra types")
		}
		if filter.IsTemplate == nil || *filter.IsTemplate {
			t.Errorf("IsTemplate = %v, want false", filter.IsTemplate)
		}
		if !filter.SkipWisps {
			t.Error("SkipWisps = false, want true for a default listing")
		}
	})

	// It is a TYPE/plane knob only: the status axis is untouched, so the
	// done/frozen exclusions and the pinned default still apply.
	t.Run("says nothing about status", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{IncludeAllTypes: true}, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(filter.ExcludeStatus) == 0 {
			t.Error("IncludeAllTypes must not lift the default status exclusions")
		}
		if filter.Pinned == nil || *filter.Pinned {
			t.Errorf("Pinned = %v, want false: IncludeAllTypes must not lift the pinned default", filter.Pinned)
		}
	})

	// ExcludeTypes is the caller's own explicit exclusion, not a default
	// suppression, so IncludeAllTypes must leave it alone.
	t.Run("explicit ExcludeTypes still applies", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{
			IncludeAllTypes: true,
			ExcludeTypes:    []string{"gate"},
		}, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(filter.ExcludeTypes) != 1 || filter.ExcludeTypes[0] != types.IssueType("gate") {
			t.Errorf("ExcludeTypes = %v, want [gate]", filter.ExcludeTypes)
		}
	})
}

// TestStatusAllCannotBeCombined pins the error a multi-status set containing
// "all" gets: "all" is a real selector on its own, so reporting it as merely
// invalid would contradict the flag help.
func TestStatusAllCannotBeCombined(t *testing.T) {
	var filter types.IssueFilter
	err := ApplyStatusFilter(&filter, "all,open", nil)
	if err == nil {
		t.Fatal("expected an error for --status=all,open")
	}
	want := `status "all" cannot be combined with other statuses`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}
