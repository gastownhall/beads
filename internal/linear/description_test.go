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

// TestPushFieldsEqualLinearStoredDescription pins AC-7: the push comparators
// match the description Linear stored for an unchanged bead (a blank line
// after each heading, marker present or absent), including BatchPush's
// pre-rendered comparableIssue, and still tell a changed body, section text
// or section presence apart.
func TestPushFieldsEqualLinearStoredDescription(t *testing.T) {
	config := DefaultMappingConfig()
	state := &State{ID: "state-progress", Name: "In Progress", Type: "started"}
	local := fullDescriptionBead()
	local.Status = types.StatusInProgress
	local.Priority = 2
	marker := GenerateIdempotencyMarker("bd-1", "a@b.c", 1)

	remoteWith := func(description string) *Issue {
		return &Issue{
			Identifier:  "TEAM-1",
			Title:       local.Title,
			Description: description,
			Priority:    PriorityToLinear(local.Priority, config),
			State:       state,
			CreatedAt:   "2026-01-01T00:00:00Z",
			UpdatedAt:   "2026-01-01T00:00:00Z",
		}
	}
	// What BatchPush compares: the FormatDescription copy with the
	// structured fields cleared.
	comparable := *local
	comparable.Description = BuildLinearDescription(local)
	comparable.AcceptanceCriteria, comparable.Design, comparable.Notes = "", "", ""

	check := func(t *testing.T, description string, want bool) {
		t.Helper()
		remote := remoteWith(description)
		if got := PushFieldsEqual(local, remote, config, nil); got != want {
			t.Errorf("PushFieldsEqual(local, %q) = %v, want %v", description, got, want)
		}
		if got := PushFieldsEqual(&comparable, remote, config, nil); got != want {
			t.Errorf("PushFieldsEqual(comparableIssue, %q) = %v, want %v", description, got, want)
		}
		pulled, _ := IssueToBeads(remote, config).Issue.(*types.Issue)
		if got := PushFieldsEqualToBeads(local, pulled); got != want {
			t.Errorf("PushFieldsEqualToBeads(local, pulled %q) = %v, want %v", description, got, want)
		}
	}

	stored := linearStoredDescription(local)
	for name, description := range map[string]string{
		"built":              BuildLinearDescription(local),
		"stored":             stored,
		"built with marker":  AppendIdempotencyMarker(BuildLinearDescription(local), marker),
		"stored with marker": AppendIdempotencyMarker(stored, marker),
	} {
		t.Run("equal/"+name, func(t *testing.T) { check(t, description, true) })
	}

	without := func(field func(*types.Issue)) string {
		changed := *local
		field(&changed)
		return AppendIdempotencyMarker(linearStoredDescription(&changed), marker)
	}
	for name, description := range map[string]string{
		"body":              without(func(i *types.Issue) { i.Description = "DESC changed" }),
		"section text":      without(func(i *types.Issue) { i.Design = "DESIGN changed" }),
		"section missing":   without(func(i *types.Issue) { i.Notes = "" }),
		"section unheaded":  "DESC\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nNOTES",
		"section added":     stored + "\n\n## Notes\n\nMORE",
		"heading level off": "DESC\n\n## Acceptance Criteria\n\nAC\n\n### Design\n\nDESIGN\n\n## Notes\n\nNOTES",
	} {
		t.Run("unequal/"+name, func(t *testing.T) { check(t, description, false) })
	}
}

// TestSettleDescriptionFields pins the pull's settle rules: a section Linear
// shows wins; an absent section empties a blank local field, or one whose
// text Linear still shows elsewhere; otherwise the local text is kept and
// reported, so the push restores the section (AC-5).
func TestSettleDescriptionFields(t *testing.T) {
	tr := &Tracker{}
	local := func() *types.Issue {
		return &types.Issue{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "Local notes\n\n  second line"}
	}
	for _, tc := range []struct {
		name     string
		local    *types.Issue
		linear   string
		want     DescriptionParts
		wantKept bool
	}{
		{
			name:   "present section wins",
			local:  local(),
			linear: "DESC\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nLinear notes",
			want:   DescriptionParts{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "Linear notes"},
		},
		{
			name:   "absent and blank locally",
			local:  &types.Issue{Description: "DESC", AcceptanceCriteria: "AC"},
			linear: "DESC v2\n\n## Acceptance Criteria\n\nAC",
			want:   DescriptionParts{Description: "DESC v2", AcceptanceCriteria: "AC"},
		},
		{
			name:   "absent but still in the Linear text",
			local:  local(),
			linear: "DESC\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\nLocal notes\n  second line  ",
			want:   DescriptionParts{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN\n\nLocal notes\n  second line"},
		},
		{
			name:     "absent and missing from the Linear text",
			local:    local(),
			linear:   "DESC v2\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\nLocal notes",
			want:     DescriptionParts{Description: "DESC v2", AcceptanceCriteria: "AC", Design: "DESIGN\n\nLocal notes", Notes: "Local notes\n\n  second line"},
			wantKept: true,
		},
		{
			name:     "every section absent",
			local:    local(),
			linear:   "DESC v2",
			want:     DescriptionParts{Description: "DESC v2", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "Local notes\n\n  second line"},
			wantKept: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pulled := partsIssue(ParseLinearDescription(tc.linear))
			kept := tr.SettleDescriptionFields(tc.local, pulled)
			got := DescriptionParts{Description: pulled.Description, AcceptanceCriteria: pulled.AcceptanceCriteria, Design: pulled.Design, Notes: pulled.Notes}
			if got != tc.want || kept != tc.wantKept {
				t.Errorf("SettleDescriptionFields() = %+v, kept %v; want %+v, kept %v", got, kept, tc.want, tc.wantKept)
			}
		})
	}
}

// TestSettleDescriptionFieldsKeepsUnchangedBead pins AC-6 at the unit level:
// when the Linear text is the bead's own pushed text, the settle restores the
// bead's four fields, even when its description ends in a heading the parser
// lifts.
func TestSettleDescriptionFieldsKeepsUnchangedBead(t *testing.T) {
	bead := &types.Issue{Description: "DESC\n\n## Acceptance Criteria\n- [ ] works", Notes: "NOTES"}
	stored := "DESC\n\n## Acceptance Criteria\n\n- [ ] works\n\n## Notes\n\nNOTES\n<!-- bd-idempotency: 0123456789ab -->"
	pulled := partsIssue(ParseLinearDescription(stored))
	if pulled.AcceptanceCriteria == "" {
		t.Fatalf("fixture: the parser should lift the bead's own heading; got %+v", pulled)
	}
	if kept := (&Tracker{}).SettleDescriptionFields(bead, pulled); kept {
		t.Errorf("SettleDescriptionFields reported kept local text for an unchanged bead")
	}
	if pulled.Description != bead.Description || pulled.AcceptanceCriteria != "" || pulled.Design != "" || pulled.Notes != "NOTES" {
		t.Errorf("SettleDescriptionFields reshaped an unchanged bead: description %q, acceptance %q, design %q, notes %q",
			pulled.Description, pulled.AcceptanceCriteria, pulled.Design, pulled.Notes)
	}
}
