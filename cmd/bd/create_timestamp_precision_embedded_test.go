//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEmbeddedCreateJSONEchoesSubSecondTimestamps pins that `bd create --json`
// prints the instant the row was written, fraction included.
//
// created_at is a DATETIME(0) column, so anything that renders a create by
// re-reading the row it just wrote prints whole seconds. `bd create --json` has
// never done that: it echoes the stamp the write carried, and agents that create
// several issues in a burst order them by exactly that fraction. A build that
// echoed the re-read made every same-second create tie.
//
// THE ASSERTION IS THAT A FRACTION IS THERE AT ALL, across two creates. A
// truncating build prints a whole second every time, so it fails on the first
// one; a correct build prints a whole second only when the clock lands exactly
// on a second boundary, which is a billion-to-one per create and so a billion
// billion-to-one across the pair. Note that "two creates tie" is NOT usable as
// the assertion here even though it is the property that matters: each create is
// a subprocess that boots embedded Dolt, so consecutive runs are seconds apart
// and would not tie even when truncated. The executor-level proof of the tie
// lives in conformance.RunLifecycleCreateEchoesSubSecondTimestamps.
func TestEmbeddedCreateJSONEchoesSubSecondTimestamps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cts")

	first := createdAtEcho(t, bd, dir, "first issue")
	second := createdAtEcho(t, bd, dir, "second issue")

	firstAt := parseRFC3339Nano(t, "first", first.createdAt)
	parseRFC3339Nano(t, "second", second.createdAt)

	fractional := false
	for _, echo := range []createEcho{first, second} {
		for _, member := range []struct{ name, value string }{
			{"created_at", echo.createdAt},
			{"updated_at", echo.updatedAt},
		} {
			at := parseRFC3339Nano(t, echo.id+" "+member.name, member.value)
			if at.Nanosecond() != 0 {
				fractional = true
			}
		}
	}
	if !fractional {
		t.Errorf("no timestamp `bd create --json` echoed across two creates carries a sub-second part "+
			"(%s created_at=%q updated_at=%q, %s created_at=%q updated_at=%q); the echo is being rounded to the "+
			"DATETIME(0) column, which is what makes same-second creates unorderable",
			first.id, first.createdAt, first.updatedAt, second.id, second.createdAt, second.updatedAt)
	}

	// The echo still names the row it created. It is not the SAME text: the
	// column is DATETIME(0) and the engine ROUNDS to it rather than truncating,
	// so a read can land up to half a second either side of the echo. That skew
	// is v1.2.2 behavior and stays — `bd show` and `bd list` answer whole
	// seconds, as they always have. What must hold is that the two name one
	// instant, which a second of slack states without pinning either the digits
	// or the rounding mode.
	shown := bdShow(t, bd, dir, first.id)
	if skew := shown.CreatedAt.UTC().Sub(firstAt.UTC()); skew > time.Second || skew < -time.Second {
		t.Errorf("%s: create echoed created_at %v but show reports %v (%v apart); the two must name the same instant, "+
			"give or take the second-granular column",
			first.id, firstAt, shown.CreatedAt.UTC(), skew)
	}
}

type createEcho struct {
	id        string
	createdAt string
	updatedAt string
}

// createdAtEcho runs `bd create --json` and reads the timestamps as the RAW
// wire strings, because the defect is about the text a consumer parses: a
// decode into time.Time would accept both spellings silently.
func createdAtEcho(t *testing.T, bd, dir, title string) createEcho {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, "create", "--json", title)
	if err != nil {
		t.Fatalf("bd create --json %q: %v\n%s", title, err, out)
	}
	s := string(out)
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in `bd create --json` output:\n%s", s)
	}
	var raw struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.NewDecoder(strings.NewReader(s[start:])).Decode(&raw); err != nil {
		t.Fatalf("parse `bd create --json` output: %v\nraw: %s", err, s[start:])
	}
	if raw.ID == "" {
		t.Fatalf("`bd create --json` echoed no id:\n%s", s)
	}
	return createEcho{id: raw.ID, createdAt: raw.CreatedAt, updatedAt: raw.UpdatedAt}
}

func parseRFC3339Nano(t *testing.T, label, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("%s created_at %q does not parse as RFC3339Nano: %v", label, value, err)
	}
	return parsed
}
