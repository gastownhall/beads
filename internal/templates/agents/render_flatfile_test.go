package agents

import (
	"strings"
	"testing"
)

// BEADS-985z: flat-file workspaces must get flat-file guidance — the Dolt
// architecture text, sync block, and `bd dolt push` directives are all
// rewritten at render time. The case-insensitive "dolt" scan doubles as an
// anchor-rot guard: if a template edit rewords any Dolt fragment so a
// replacement anchor stops matching, the stranded mention fails this test
// instead of shipping Dolt instructions to flat-file users.
func TestFlatfileRenderHasNoDoltReferences(t *testing.T) {
	opts := RenderOpts{HasRemote: true, Flatfile: true}
	docs := map[string]string{
		"full":    RenderSectionWithOpts(ProfileFull, opts),
		"minimal": RenderSectionWithOpts(ProfileMinimal, opts),
		"codex":   CodexSectionBodyWithOpts(opts),
		"agents":  EmbeddedDefaultWithOpts(opts),
	}
	for name, doc := range docs {
		if idx := strings.Index(strings.ToLower(doc), "dolt"); idx != -1 {
			start := idx - 60
			if start < 0 {
				start = 0
			}
			end := idx + 60
			if end > len(doc) {
				end = len(doc)
			}
			t.Errorf("%s: flatfile render still mentions Dolt: ...%s...", name, doc[start:end])
		}
		if !strings.Contains(doc, "flat JSON files") {
			t.Errorf("%s: flatfile render missing the flat-file architecture line", name)
		}
	}
}

// The Flatfile opt must never leak into non-flatfile renders: Dolt workspaces
// keep the canonical Dolt guidance verbatim.
func TestNonFlatfileRenderKeepsDoltGuidance(t *testing.T) {
	opts := RenderOpts{HasRemote: true}
	for _, profile := range []Profile{ProfileFull, ProfileMinimal} {
		section := RenderSectionWithOpts(profile, opts)
		if !strings.Contains(section, "Dolt") {
			t.Errorf("%s: non-flatfile render lost its Dolt guidance", profile)
		}
	}
	if got, want := EmbeddedDefaultWithOpts(opts), EmbeddedDefault(); got != want {
		t.Error("EmbeddedDefaultWithOpts without Flatfile must return the template verbatim")
	}
}

// Backend-distinct hashes make ReplaceSectionWithOpts treat a backend change
// as staleness, so re-running setup after a migration rewrites the section.
func TestFlatfileHashDiffersFromDolt(t *testing.T) {
	for _, profile := range []Profile{ProfileFull, ProfileMinimal} {
		dolt := CurrentHashWithOpts(profile, RenderOpts{HasRemote: true})
		flat := CurrentHashWithOpts(profile, RenderOpts{HasRemote: true, Flatfile: true})
		if dolt == flat {
			t.Errorf("%s: flatfile and dolt template hashes are identical; section freshness cannot detect backend changes", profile)
		}
	}
}
