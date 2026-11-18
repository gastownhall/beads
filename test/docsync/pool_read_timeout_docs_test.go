package docsync

import (
	"strings"
	"testing"
)

// poolReadTimeoutHeading is the troubleshooting section a reader reaches from
// the "possible pool read timeout ...; read not retried" error (GH#5258).
const poolReadTimeoutHeading = `### Read timeout: "possible pool read timeout"`

// TestPoolReadTimeoutDocs guards the user docs for the pool read and write
// deadlines and for the error a read that ran its read deadline now returns
// (GH#5258): both knobs in the configuration table, and a troubleshooting
// section that says what the error means and how to tell it from an empty
// list. Each assertion is scoped to the row or section a reader lands on.
func TestPoolReadTimeoutDocs(t *testing.T) {
	t.Run("configuration rows", func(t *testing.T) {
		config := readRepoDoc(t, "docs", "reference", "configuration.md")
		for _, knob := range []struct{ key, env string }{
			{"dolt.pool-read-timeout", "BEADS_DOLT_POOL_READ_TIMEOUT"},
			{"dolt.pool-write-timeout", "BEADS_DOLT_POOL_WRITE_TIMEOUT"},
		} {
			row := tableRow(config, "`"+knob.key+"`")
			if row == "" {
				t.Errorf("configuration.md has no table row for `%s`", knob.key)
				continue
			}
			requirePhrases(t, "configuration.md row "+knob.key, row,
				"`"+knob.env+"`",
				"`10s`",
				"per-I/O deadline",
				"not a budget for the whole query",
			)
		}
	})

	t.Run("troubleshooting section", func(t *testing.T) {
		guide := readRepoDoc(t, "docs", "reference", "troubleshooting.md")
		section, ok := docSubtree(guide, poolReadTimeoutHeading)
		if !ok {
			t.Fatalf("troubleshooting.md missing the %q section", poolReadTimeoutHeading)
		}
		requirePhrases(t, "troubleshooting.md read-timeout section", section,
			"read not retried",
			"per-I/O deadline",
			"possible",
			"BEADS_DOLT_POOL_READ_TIMEOUT",
			"dolt.pool-read-timeout",
			"exits 1",
			"stdout is empty",
			"`[]`",
			"recurring",
		)
		forbidPhrases(t, "troubleshooting.md read-timeout section", section,
			"bd dolt stop",
			"restart the server",
		)
	})
}

// tableRow returns the first Markdown table line whose first cell is cell, or
// "" when none is.
func tableRow(doc, cell string) string {
	for _, line := range strings.Split(doc, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) > 2 && strings.TrimSpace(cells[0]) == "" && strings.TrimSpace(cells[1]) == cell {
			return line
		}
	}
	return ""
}
