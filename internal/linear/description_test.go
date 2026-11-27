package linear

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

var parseDescriptionMarker = GenerateIdempotencyMarker("bd-parse", "a@b.c", 9)

// parseLinearDescriptionCases pins the ParseLinearDescription grammar for
// GH#8506: markers go wherever a sync left them, and only the final
// in-order run of section headings with non-blank bodies lifts. TestParseLinearDescriptionRoundTrip
// builds its properties on the same table.
var parseLinearDescriptionCases = []struct {
	name string
	text string
	want DescriptionParts
}{
	{
		name: "stored form with marker",
		text: "DESC\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nNOTES\n" + parseDescriptionMarker + "\n",
		want: DescriptionParts{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "NOTES"},
	},
	{
		name: "stored form without marker",
		text: "DESC\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nNOTES",
		want: DescriptionParts{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "NOTES"},
	},
	{
		name: "build form",
		text: BuildLinearDescription(&types.Issue{
			Description:        "DESC",
			AcceptanceCriteria: "AC",
			Design:             "DESIGN",
			Notes:              "NOTES",
		}),
		want: DescriptionParts{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "NOTES"},
	},
	{
		// A create that stored only its marker holds nothing else.
		name: "marker only",
		text: parseDescriptionMarker,
		want: DescriptionParts{},
	},
	{
		// A marker a previous pull absorbed sits above the text.
		name: "marker mid-text",
		text: "\n" + parseDescriptionMarker + "\n\nDESC\n\n## Notes\n\nNOTES",
		want: DescriptionParts{Description: "DESC", Notes: "NOTES"},
	},
	{
		name: "crlf line endings and trailing blanks after a heading",
		text: "DESC\r\n\r\n## Notes  \r\n\r\nNOTES\r\n",
		want: DescriptionParts{Description: "DESC", Notes: "NOTES"},
	},
	{
		name: "empty description with sections",
		text: "## Notes\n\nNOTES",
		want: DescriptionParts{Notes: "NOTES"},
	},
	{
		name: "leading blank lines above a run",
		text: "\n\n## Design\n\nDESIGN",
		want: DescriptionParts{Design: "DESIGN"},
	},
	{
		// Design before Notes is a run BuildLinearDescription produces when
		// the acceptance criteria are empty.
		name: "design then notes lifts both",
		text: "DESC\n\n## Design\nDESIGN\n\n## Notes\nNOTES",
		want: DescriptionParts{Description: "DESC", Design: "DESIGN", Notes: "NOTES"},
	},
	{
		// Notes above Design is not a buildable run: the trailing run wins
		// and the earlier heading stays description text.
		name: "out of order keeps the earlier heading as text",
		text: "DESC\n\n## Notes\nNOTES\n\n## Design\nDESIGN",
		want: DescriptionParts{Description: "DESC\n\n## Notes\nNOTES", Design: "DESIGN"},
	},
	{
		// The GH#8506 damaged body: an earlier push's sections were copied
		// into the description and a new run appended. The last run lifts.
		name: "repeated run lifts the last one",
		text: "DESC\n\n## Design\nOLD-DESIGN\n\n## Notes\nOLD-NOTES\n\n## Design\nDESIGN\n\n## Notes\nNOTES",
		want: DescriptionParts{Description: "DESC\n\n## Design\nOLD-DESIGN\n\n## Notes\nOLD-NOTES", Design: "DESIGN", Notes: "NOTES"},
	},
	{
		// A template heading with nothing under it is not a run.
		name: "empty-bodied template heading stays text",
		text: "DESC\n\n## Notes\n",
		want: DescriptionParts{Description: "DESC\n\n## Notes\n"},
	},
	{
		// The fenced "## Design" would extend the run (rank 1 below Notes)
		// if headings inside fences counted.
		name: "heading inside a closed backtick fence stays text",
		text: "DESC\n\n```\n## Design\nFENCED-DESIGN\n```\n\n## Notes\nNOTES",
		want: DescriptionParts{Description: "DESC\n\n```\n## Design\nFENCED-DESIGN\n```", Notes: "NOTES"},
	},
	{
		name: "heading inside a closed tilde fence stays text",
		text: "~~~\n## Design\nFENCED-DESIGN\n~~~\n## Notes\nNOTES",
		want: DescriptionParts{Description: "~~~\n## Design\nFENCED-DESIGN\n~~~", Notes: "NOTES"},
	},
	{
		// An unclosed fence hides nothing.
		name: "unclosed fence does not hide the heading",
		text: "```\n## Notes\nNOTES",
		want: DescriptionParts{Description: "```", Notes: "NOTES"},
	},
	{
		name: "near-miss headings stay text",
		text: "### Notes\nX\n\n## notes\nY\n\n## Notes:\nZ\n\n ## Notes\nW",
		want: DescriptionParts{Description: "### Notes\nX\n\n## notes\nY\n\n## Notes:\nZ\n\n ## Notes\nW"},
	},
	{
		name: "plain text comes back byte-for-byte",
		text: "Just some text\n\nwith paragraphs  \r\nand trailing blanks",
		want: DescriptionParts{Description: "Just some text\n\nwith paragraphs  \r\nand trailing blanks"},
	},
}

func TestParseLinearDescription(t *testing.T) {
	for _, tc := range parseLinearDescriptionCases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseLinearDescription(tc.text)
			if got != tc.want {
				t.Errorf("ParseLinearDescription(%q)\n got  %+v\n want %+v", tc.text, got, tc.want)
			}
		})
	}
}

// TestParseLinearDescriptionRoundTrip pins the parser properties:
//
//	P1: Parse(Build(L)) == L for every combination of non-blank fields,
//	    when no field holds a candidate line.
//	P2: Parse(Build(Parse(x))) == Parse(x).
//	P3: Linear's stored form (a blank line after each heading) parses the
//	    same as the tight form.
//	P4: Build(Parse(x)) reproduces x up to blank lines, trailing whitespace
//	    and markers: text is never lost or reordered.
func TestParseLinearDescriptionRoundTrip(t *testing.T) {
	// P1: all 8 combinations of present fields.
	for mask := 0; mask < 8; mask++ {
		local := &types.Issue{Description: "DESC"}
		if mask&1 != 0 {
			local.AcceptanceCriteria = "AC"
		}
		if mask&2 != 0 {
			local.Design = "DESIGN"
		}
		if mask&4 != 0 {
			local.Notes = "NOTES"
		}
		got := ParseLinearDescription(BuildLinearDescription(local))
		if got.Description != local.Description ||
			got.AcceptanceCriteria != local.AcceptanceCriteria ||
			got.Design != local.Design ||
			got.Notes != local.Notes {
			t.Errorf("P1 mask %03b: Parse(Build(%+v)) = %+v", mask, *local, got)
		}
	}

	for _, tc := range parseLinearDescriptionCases {
		t.Run(tc.name, func(t *testing.T) {
			parsed := ParseLinearDescription(tc.text)

			// P2: a fixed point after one build.
			once := ParseLinearDescription(BuildLinearDescriptionFromParts(parsed))
			if once != parsed {
				t.Errorf("P2: Parse(Build(Parse(x)))\n got  %+v\n want %+v", once, parsed)
			}

			// P4: no text lost or reordered.
			if got, want := canonicalLinearText(BuildLinearDescriptionFromParts(parsed)), canonicalLinearText(tc.text); got != want {
				t.Errorf("P4: canonical(Build(Parse(x)))\n got  %q\n want %q", got, want)
			}

			// P3: Linear's blank line after each heading changes nothing
			// that lifts; the description may keep the added blank lines.
			if parsed != (DescriptionParts{}) {
				stored := insertBlankLinesAfterHeadingLines(tc.text)
				got := ParseLinearDescription(stored)
				if canonicalLinearText(got.Description) != canonicalLinearText(parsed.Description) ||
					got.AcceptanceCriteria != parsed.AcceptanceCriteria ||
					got.Design != parsed.Design ||
					got.Notes != parsed.Notes {
					t.Errorf("P3: Parse(stored form %q)\n got  %+v\n want %+v", stored, got, parsed)
				}
			}
		})
	}
}

// BuildLinearDescriptionFromParts composes the Linear body of parsed parts.
func BuildLinearDescriptionFromParts(parts DescriptionParts) string {
	return BuildLinearDescription(&types.Issue{
		Description:        parts.Description,
		AcceptanceCriteria: parts.AcceptanceCriteria,
		Design:             parts.Design,
		Notes:              parts.Notes,
	})
}

// insertBlankLinesAfterHeadingLines adds a blank line after every section
// heading line, the way Linear renders a stored description.
func insertBlankLinesAfterHeadingLines(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, line)
		if headingRank(line) >= 0 {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}
