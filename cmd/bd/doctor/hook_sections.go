package doctor

import "strings"

// The whole-line forms of the section markers hooks.go generates:
// "# --- BEGIN BEADS INTEGRATION v1.2.3 ---". Only content between whole
// markers is safe to strip textually — a marker tag inside another line
// (for example inside a string literal) belongs to the script, not to bd.
const (
	hookMarkerLineBegin = "# --- BEGIN BEADS INTEGRATION"
	hookMarkerLineEnd   = "# --- END BEADS INTEGRATION"
)

// BdSectionMarkers describes the state of bd BEGIN/END section markers in
// hook content.
type BdSectionMarkers int

const (
	// BdSectionMarkersNone: no marker tags anywhere in the content.
	BdSectionMarkersNone BdSectionMarkers = iota
	// BdSectionMarkersPaired: every marker is a whole-line marker, and the
	// markers form complete, ordered, non-nested BEGIN/END pairs. Such a
	// section can be removed textually without understanding the script.
	BdSectionMarkersPaired
	// BdSectionMarkersAmbiguous: marker tags are present but do not form
	// clean whole-line pairs — unpaired, nested, reversed, or embedded in
	// other lines. bd never rewrites such content automatically.
	BdSectionMarkersAmbiguous
)

// InspectBdSectionMarkers classifies the bd section markers in content. It
// is the validation gate for every automatic non-shell repair (GH#6348):
// only Paired content may have sections stripped; Ambiguous content is left
// untouched with a manual-inspection diagnostic.
func InspectBdSectionMarkers(content string) BdSectionMarkers {
	if !strings.Contains(content, hookMarkerBeginTag) && !strings.Contains(content, hookMarkerEndTag) {
		return BdSectionMarkersNone
	}
	sawPair := false
	depth := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, hookMarkerLineBegin):
			if depth != 0 {
				return BdSectionMarkersAmbiguous // nested BEGIN
			}
			depth = 1
		case strings.HasPrefix(trimmed, hookMarkerLineEnd):
			if depth == 0 {
				return BdSectionMarkersAmbiguous // unmatched or reversed END
			}
			sawPair = true
			depth = 0
		default:
			// A marker tag inside another line (e.g. inside a string
			// literal) is not a whole-line marker bd can strip around.
			if strings.Contains(line, hookMarkerBeginTag) || strings.Contains(line, hookMarkerEndTag) {
				return BdSectionMarkersAmbiguous
			}
		}
	}
	if depth != 0 {
		return BdSectionMarkersAmbiguous // BEGIN never closed
	}
	if !sawPair {
		return BdSectionMarkersAmbiguous // tags only, no complete pair
	}
	return BdSectionMarkersPaired
}
