package types

import (
	"strings"
	"testing"
	"time"
)

func ptime(t time.Time) *time.Time { return &t }

func TestValidateRecurrenceAcceptsValidRules(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	for _, issue := range []Issue{
		{},                           // not recurring
		{RepeatPattern: "+1w"},       // interval, unbounded
		{RepeatPattern: "0 9 * * 1"}, // cron, unbounded
		{RepeatPattern: "+1d", RepeatStart: ptime(start)},                        // start only
		{RepeatPattern: "+1d", RepeatEnd: ptime(end)},                            // end only
		{RepeatPattern: "+1d", RepeatStart: ptime(start), RepeatEnd: ptime(end)}, // both
	} {
		if err := issue.ValidateRecurrence(); err != nil {
			t.Errorf("ValidateRecurrence(%+v) = %v, want nil", issue, err)
		}
	}
}

func TestValidateRecurrenceRejectsIncoherentRules(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		issue Issue
		want  string
	}{
		{"bounds without a pattern read as a dropped --repeat",
			Issue{RepeatStart: ptime(start)}, "require repeat_pattern"},
		{"end bound without a pattern",
			Issue{RepeatEnd: ptime(start)}, "require repeat_pattern"},
		{"unparseable pattern",
			Issue{RepeatPattern: "every tuesday"}, "invalid repeat_pattern"},
		{"backwards interval never advances",
			Issue{RepeatPattern: "-1d"}, "invalid repeat_pattern"},
		{"end before start",
			Issue{RepeatPattern: "+1d", RepeatStart: ptime(start), RepeatEnd: ptime(start.AddDate(0, 0, -1))},
			"before repeat_start"},
		{"over-length pattern",
			Issue{RepeatPattern: strings.Repeat("x", MaxFieldLen+1)}, "repeat_pattern"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.issue.ValidateRecurrence()
			if err == nil {
				t.Fatalf("ValidateRecurrence() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateRecurrence() = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// ValidateWithCustom is the gate every create and update path runs, so an
// incoherent recurrence must be refused there and not only by the direct call.
func TestValidateWithCustomRejectsBadRecurrence(t *testing.T) {
	issue := Issue{Title: "t", Status: StatusOpen, IssueType: TypeTask, Priority: 2, RepeatPattern: "not a rule"}
	if err := issue.ValidateWithCustom(nil, nil); err == nil {
		t.Fatal("ValidateWithCustom() = nil, want a recurrence refusal")
	}
}

func TestIsRecurring(t *testing.T) {
	if (&Issue{}).IsRecurring() {
		t.Error("empty pattern reported as recurring")
	}
	if !(&Issue{RepeatPattern: "+1d"}).IsRecurring() {
		t.Error("set pattern reported as not recurring")
	}
}

func TestNextOccurrenceInterval(t *testing.T) {
	after := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	issue := &Issue{RepeatPattern: "+1w"}
	next, ok, err := issue.NextOccurrence(after)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence() = (%v, %v, %v), want a value", next, ok, err)
	}
	if want := after.AddDate(0, 0, 7); !next.Equal(want) {
		t.Errorf("NextOccurrence() = %v, want %v", next, want)
	}
}

// A bead that is not recurring reports "no next occurrence" WITHOUT an error:
// the series ending and the series never existing are both normal, and only a
// broken pattern is an error.
func TestNextOccurrenceNotRecurring(t *testing.T) {
	next, ok, err := (&Issue{}).NextOccurrence(time.Now())
	if err != nil || ok || !next.IsZero() {
		t.Errorf("NextOccurrence() = (%v, %v, %v), want (zero, false, nil)", next, ok, err)
	}
}

// RepeatEnd stops the series rather than producing an occurrence past it.
func TestNextOccurrenceStopsAtRepeatEnd(t *testing.T) {
	after := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	issue := &Issue{RepeatPattern: "+1w", RepeatEnd: ptime(after.AddDate(0, 0, 3))}
	next, ok, err := issue.NextOccurrence(after)
	if err != nil {
		t.Fatalf("NextOccurrence() error = %v", err)
	}
	if ok {
		t.Errorf("NextOccurrence() = %v, want the series to have ended", next)
	}
}

// An occurrence exactly ON RepeatEnd is still in the series: the bound is the
// last allowed occurrence, not the first excluded one.
func TestNextOccurrenceIncludesRepeatEndItself(t *testing.T) {
	after := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	last := after.AddDate(0, 0, 7)
	issue := &Issue{RepeatPattern: "+1w", RepeatEnd: ptime(last)}
	next, ok, err := issue.NextOccurrence(after)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence() = (%v, %v, %v), want the final occurrence", next, ok, err)
	}
	if !next.Equal(last) {
		t.Errorf("NextOccurrence() = %v, want %v", next, last)
	}
}

// A series whose start bound is still in the future counts from the START, not
// from whenever the caller asked: the first instance lands on the schedule
// rather than relative to the question.
func TestNextOccurrenceHonorsRepeatStart(t *testing.T) {
	now := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	issue := &Issue{RepeatPattern: "+1w", RepeatStart: ptime(start)}
	next, ok, err := issue.NextOccurrence(now)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence() = (%v, %v, %v), want a value", next, ok, err)
	}
	if !next.Equal(start) {
		t.Errorf("NextOccurrence() = %v, want the start bound %v", next, start)
	}
}

func TestNextOccurrenceReportsBrokenPattern(t *testing.T) {
	issue := &Issue{RepeatPattern: "nonsense"}
	if _, _, err := issue.NextOccurrence(time.Now()); err == nil {
		t.Fatal("NextOccurrence() error = nil, want a parse failure")
	}
}
