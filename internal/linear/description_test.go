package linear

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// linearStoredDescription returns BuildLinearDescription(issue) the way Linear
// stores it: Linear's editor puts a blank line after each heading.
func linearStoredDescription(issue *types.Issue) string {
	stored := issue.Description
	if issue.AcceptanceCriteria != "" {
		stored += "\n\n## Acceptance Criteria\n\n" + issue.AcceptanceCriteria
	}
	if issue.Design != "" {
		stored += "\n\n## Design\n\n" + issue.Design
	}
	if issue.Notes != "" {
		stored += "\n\n## Notes\n\n" + issue.Notes
	}
	return stored
}

func fullDescriptionBead() *types.Issue {
	return &types.Issue{
		Title:              "T",
		Description:        "DESC",
		AcceptanceCriteria: "AC",
		Design:             "DESIGN",
		Notes:              "NOTES",
	}
}

// TestParseLinearDescriptionStoredForm pins AC-1: the description a push
// wrote, as Build emits it and as Linear stores it, with and without the
// create's idempotency marker, parses back into the four bead fields.
func TestParseLinearDescriptionStoredForm(t *testing.T) {
	bead := fullDescriptionBead()
	marker := GenerateIdempotencyMarker("bd-1", "a@b.c", 1)
	want := DescriptionParts{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "NOTES"}
	for name, text := range map[string]string{
		"built":                 BuildLinearDescription(bead),
		"built with marker":     AppendIdempotencyMarker(BuildLinearDescription(bead), marker),
		"stored":                linearStoredDescription(bead),
		"stored with marker":    AppendIdempotencyMarker(linearStoredDescription(bead), marker),
		"stored, trailing line": linearStoredDescription(bead) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ParseLinearDescription(text); got != want {
				t.Errorf("ParseLinearDescription(%q) = %+v, want %+v", text, got, want)
			}
		})
	}
}

// TestIssueToBeadsSplitsDescriptionSections pins AC-1 at the mapping layer:
// a pulled Linear issue fills the four fields, none holding a heading or the
// marker, and BuildLinearToLocalUpdates writes the same values.
func TestIssueToBeadsSplitsDescriptionSections(t *testing.T) {
	stored := AppendIdempotencyMarker(linearStoredDescription(fullDescriptionBead()), GenerateIdempotencyMarker("bd-1", "a@b.c", 1))
	li := &Issue{
		ID:          "uuid-1",
		Identifier:  "TEAM-1",
		Title:       "T",
		Description: stored,
		URL:         "https://linear.app/team/issue/TEAM-1/t",
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-01T00:00:00Z",
	}
	cfg := DefaultMappingConfig()

	got, ok := IssueToBeads(li, cfg).Issue.(*types.Issue)
	if !ok {
		t.Fatalf("IssueToBeads returned %T, want *types.Issue", IssueToBeads(li, cfg).Issue)
	}
	if got.Description != "DESC" || got.AcceptanceCriteria != "AC" || got.Design != "DESIGN" || got.Notes != "NOTES" {
		t.Fatalf("IssueToBeads fields = description %q, acceptance %q, design %q, notes %q; want DESC/AC/DESIGN/NOTES",
			got.Description, got.AcceptanceCriteria, got.Design, got.Notes)
	}

	updates := BuildLinearToLocalUpdates(li, cfg)
	for key, want := range map[string]string{
		"description":         "DESC",
		"acceptance_criteria": "AC",
		"design":              "DESIGN",
		"notes":               "NOTES",
	} {
		if updates[key] != want {
			t.Errorf("BuildLinearToLocalUpdates[%q] = %v, want %q", key, updates[key], want)
		}
	}

	// A body without sections writes no section keys, so the updates never
	// clear a field Linear does not show.
	li.Description = "Just text"
	updates = BuildLinearToLocalUpdates(li, cfg)
	for _, key := range []string{"acceptance_criteria", "design", "notes"} {
		if _, ok := updates[key]; ok {
			t.Errorf("BuildLinearToLocalUpdates(no sections)[%q] = %v, want no key", key, updates[key])
		}
	}
}

// parserCases are Linear descriptions with none of bd's headings, or with
// them out of order, repeated, empty-bodied, fenced or misspelled (AC-2).
// A nil want means nothing is lifted and the text comes back byte-for-byte.
var parserCases = []struct {
	name string
	text string
	want *DescriptionParts
}{
	{name: "plain text", text: "Hello\n\nworld  \n"},
	{name: "empty", text: ""},
	{name: "marker only", text: "<!-- bd-idempotency: 0123456789ab -->", want: &DescriptionParts{}},
	{
		name: "marker mid-text",
		text: "A\n<!-- bd-idempotency: 0123456789ab -->\nB\n\n## Notes\nN",
		want: &DescriptionParts{Description: "A\nB", Notes: "N"},
	},
	{
		name: "crlf and trailing spaces after a heading",
		text: "D\r\n\r\n## Notes  \t\r\n\r\nN1\r\nN2\r\n",
		want: &DescriptionParts{Description: "D", Notes: "N1\r\nN2"},
	},
	{name: "empty description with sections", text: "## Design\n\nX", want: &DescriptionParts{Design: "X"}},
	{
		name: "out of order",
		text: "D\n\n## Notes\nN\n\n## Design\nX",
		want: &DescriptionParts{Description: "D\n\n## Notes\nN", Design: "X"},
	},
	{
		name: "repeated run",
		text: "D\n\n## Design\nX\n\n## Notes\nN\n<!-- bd-idempotency: 0123456789ab -->\n\n## Design\n\nX\n\n## Notes\n\nN",
		want: &DescriptionParts{Description: "D\n\n## Design\nX\n\n## Notes\nN", Design: "X", Notes: "N"},
	},
	{name: "empty-bodied template heading", text: "D\n\n## Acceptance Criteria\n\n"},
	{
		name: "empty-bodied heading above a section",
		text: "D\n\n## Acceptance Criteria\n\n## Notes\nN",
		want: &DescriptionParts{Description: "D\n\n## Acceptance Criteria", Notes: "N"},
	},
	{name: "closed backtick fence", text: "D\n\n```markdown\n## Notes\nN\n```"},
	{name: "closed tilde fence", text: "D\n\n~~~\n## Design\nX\n~~~\n"},
	{
		name: "fenced heading above a real section",
		text: "```\n## Notes\nx\n```\n\n## Notes\n\nN",
		want: &DescriptionParts{Description: "```\n## Notes\nx\n```", Notes: "N"},
	},
	{
		name: "unclosed fence hides nothing",
		text: "D\n```\ncode\n\n## Notes\nN",
		want: &DescriptionParts{Description: "D\n```\ncode", Notes: "N"},
	},
	{name: "deeper heading", text: "D\n\n### Notes\nN"},
	{name: "lower-case heading", text: "D\n\n## notes\nN"},
	{name: "heading with colon", text: "D\n\n## Notes:\nN"},
	{name: "indented heading", text: "D\n\n ## Notes\nN"},
}

// TestParseLinearDescription pins AC-2: only the final in-order run with
// non-blank bodies is lifted, and a text with nothing lifted and no marker
// comes back byte-for-byte.
func TestParseLinearDescription(t *testing.T) {
	for _, tc := range parserCases {
		t.Run(tc.name, func(t *testing.T) {
			want := DescriptionParts{Description: tc.text}
			if tc.want != nil {
				want = *tc.want
			}
			if got := ParseLinearDescription(tc.text); got != want {
				t.Errorf("ParseLinearDescription(%q) =\n  %+v\nwant\n  %+v", tc.text, got, want)
			}
		})
	}
}

// TestParseLinearDescriptionRoundTrip pins the parser's properties (AC-1,
// AC-2): P1 Parse(Build(L)) == L, P2 Parse(Build(Parse(x))) == Parse(x),
// P3 Linear's stored form parses like Build's output, and P4 Build(Parse(x))
// reproduces x up to blank lines, trailing whitespace and markers.
func TestParseLinearDescriptionRoundTrip(t *testing.T) {
	marker := GenerateIdempotencyMarker("bd-1", "a@b.c", 1)
	for mask := 0; mask < 8; mask++ {
		bead := &types.Issue{Description: "DESC\n\nmore"}
		if mask&1 != 0 {
			bead.AcceptanceCriteria = "AC line 1\n\n  AC line 2"
		}
		if mask&2 != 0 {
			bead.Design = "DESIGN"
		}
		if mask&4 != 0 {
			bead.Notes = "NOTES\n```\ncode\n```"
		}
		want := DescriptionParts{Description: bead.Description, AcceptanceCriteria: bead.AcceptanceCriteria, Design: bead.Design, Notes: bead.Notes}
		for form, text := range map[string]string{
			"built":              BuildLinearDescription(bead),
			"stored":             linearStoredDescription(bead),
			"stored with marker": AppendIdempotencyMarker(linearStoredDescription(bead), marker),
		} {
			if got := ParseLinearDescription(text); got != want {
				t.Errorf("P1/P3 mask %03b %s: ParseLinearDescription(%q) = %+v, want %+v", mask, form, text, got, want)
			}
		}
	}

	for _, tc := range parserCases {
		parsed := ParseLinearDescription(tc.text)
		rebuilt := BuildLinearDescription(partsIssue(parsed))
		if again := ParseLinearDescription(rebuilt); again != parsed {
			t.Errorf("P2 %s: Parse(Build(Parse(x))) = %+v, want %+v", tc.name, again, parsed)
		}
		if got, want := canonicalLinearText(rebuilt), canonicalLinearText(tc.text); got != want {
			t.Errorf("P4 %s: Build(Parse(x)) = %q, want %q up to blank lines, trailing whitespace and markers", tc.name, got, want)
		}
	}
}

func partsIssue(p DescriptionParts) *types.Issue {
	return &types.Issue{Description: p.Description, AcceptanceCriteria: p.AcceptanceCriteria, Design: p.Design, Notes: p.Notes}
}
