package timeparsing

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RepeatHorizon bounds the forward search for a cron occurrence. A pattern
// whose next match lies beyond this (for example "0 0 30 2 *" — February 30th,
// which never occurs) is reported as an error at PARSE time rather than
// looping. Five years comfortably covers every schedule a bead can express,
// including "once a year on a leap day", whose worst gap is four years.
const RepeatHorizon = 5 * 365 * 24 * time.Hour

// Repeat is a parsed recurrence rule. Two forms are accepted:
//
//	interval — a compact duration applied to the previous occurrence:
//	           "+1d", "+2w", "3m", "1y" (see ParseCompactDuration). The sign
//	           must be positive: a recurrence that walks backwards has no
//	           next occurrence.
//	cron     — a five-field cron expression, "minute hour dom month dow":
//	           "0 9 * * 1" (09:00 every Monday), "*/15 * * * *",
//	           "0 0 1 JAN *". Fields accept *, n, a-b, */n, a-b/n and
//	           comma-separated lists of those. Month accepts JAN..DEC and
//	           day-of-week SUN..SAT (case-insensitive); dow 7 means Sunday.
//
// The zero Repeat is the empty rule: IsZero reports true and Next refuses.
type Repeat struct {
	raw string
	// interval holds the compact-duration amount and unit when the rule is an
	// interval; cron is nil in that case, and vice versa.
	amount int
	unit   string
	cron   *cronSchedule
}

// ParseRepeat parses a recurrence rule. An empty string yields the zero Repeat
// and no error, so callers can parse an unset field without special-casing it.
func ParseRepeat(s string) (Repeat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Repeat{}, nil
	}
	if matches := compactDurationRe.FindStringSubmatch(s); matches != nil {
		if matches[1] == "-" {
			return Repeat{}, fmt.Errorf("repeat interval %q must be positive", s)
		}
		amount, err := strconv.Atoi(matches[2])
		if err != nil || amount <= 0 {
			return Repeat{}, fmt.Errorf("repeat interval %q must be positive", s)
		}
		return Repeat{raw: s, amount: amount, unit: matches[3]}, nil
	}
	if fields := strings.Fields(s); len(fields) == 5 {
		cron, err := parseCron(fields)
		if err != nil {
			return Repeat{}, fmt.Errorf("invalid cron pattern %q: %w", s, err)
		}
		r := Repeat{raw: s, cron: cron}
		// Reject a pattern with no occurrence inside the horizon at parse time,
		// so an impossible schedule is refused where it is typed rather than
		// silently never firing.
		if _, err := r.Next(time.Now().UTC()); err != nil {
			return Repeat{}, fmt.Errorf("invalid cron pattern %q: %w", s, err)
		}
		return r, nil
	}
	return Repeat{}, fmt.Errorf("unrecognized repeat pattern %q: want an interval (+1d, +2w, 1m, 1y) or a 5-field cron expression (0 9 * * 1)", s)
}

// IsZero reports whether this is the empty rule.
func (r Repeat) IsZero() bool { return r.unit == "" && r.cron == nil }

// IsInterval reports whether the rule is a compact-duration interval, as
// opposed to a cron schedule.
func (r Repeat) IsInterval() bool { return r.unit != "" }

// String returns the rule as it was written.
func (r Repeat) String() string { return r.raw }

// FirstAtOrAfter returns the first occurrence at or after t, for a series that
// has not produced one yet.
//
// It differs from Next for INTERVAL rules: a series starting on a given date
// has its first occurrence ON that date, not one interval past it. For cron
// rules it is the first matching minute at or after t, which is Next's own
// search started one instant earlier.
func (r Repeat) FirstAtOrAfter(t time.Time) (time.Time, error) {
	switch {
	case r.cron != nil:
		return r.cron.next(t.Add(-time.Nanosecond))
	case r.unit != "":
		return t, nil
	default:
		return time.Time{}, fmt.Errorf("no repeat pattern set")
	}
}

// Next returns the first occurrence strictly after t. Interval rules add the
// interval to t (a month or year step clamps to a shorter target month); cron
// rules search forward to the next matching minute. It is NextFrom with t as
// its own anchor.
func (r Repeat) Next(t time.Time) (time.Time, error) {
	return r.NextFrom(t, t)
}

// NextFrom returns the first occurrence strictly after t for a series
// anchored on anchor.
//
// Only a month or year interval reads the anchor: the step keeps the anchor's
// day-of-month, clamped to the target month's length, so a series anchored on
// Jan 30 runs Feb 28, Mar 30, Apr 30 rather than sliding to the 28th once
// February has clamped it, and one anchored on the 31st lands on the last day
// of every month (Jan 31 -> Feb 28 -> Mar 31 -> Apr 30). Every other rule
// ignores the anchor.
func (r Repeat) NextFrom(t, anchor time.Time) (time.Time, error) {
	switch {
	case r.cron != nil:
		return r.cron.next(t)
	case r.unit == "m" || r.unit == "y":
		next := applyDuration(t, r.amount, r.unit)
		day := anchor.Day()
		if last := daysInMonth(next); day > last {
			day = last
		}
		return next.AddDate(0, 0, day-next.Day()), nil
	case r.unit != "":
		return applyDuration(t, r.amount, r.unit), nil
	default:
		return time.Time{}, fmt.Errorf("no repeat pattern set")
	}
}

// cronSchedule is a parsed five-field cron expression. Each field is a set of
// permitted values; domRestricted/dowRestricted record whether the field was
// written as "*", which selects the Vixie OR rule below.
type cronSchedule struct {
	minutes       map[int]bool
	hours         map[int]bool
	doms          map[int]bool
	months        map[int]bool
	dows          map[int]bool
	domRestricted bool
	dowRestricted bool
}

var cronMonthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var cronDowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func parseCron(fields []string) (*cronSchedule, error) {
	minutes, _, err := parseCronField(fields[0], 0, 59, nil, "minute")
	if err != nil {
		return nil, err
	}
	hours, _, err := parseCronField(fields[1], 0, 23, nil, "hour")
	if err != nil {
		return nil, err
	}
	doms, domRestricted, err := parseCronField(fields[2], 1, 31, nil, "day-of-month")
	if err != nil {
		return nil, err
	}
	months, _, err := parseCronField(fields[3], 1, 12, cronMonthNames, "month")
	if err != nil {
		return nil, err
	}
	dows, dowRestricted, err := parseCronField(fields[4], 0, 7, cronDowNames, "day-of-week")
	if err != nil {
		return nil, err
	}
	// Cron day-of-week accepts both 0 and 7 for Sunday; normalize to 0 so the
	// match below can compare against time.Weekday directly.
	if dows[7] {
		delete(dows, 7)
		dows[0] = true
	}
	return &cronSchedule{
		minutes: minutes, hours: hours, doms: doms, months: months, dows: dows,
		domRestricted: domRestricted, dowRestricted: dowRestricted,
	}, nil
}

// parseCronField expands one field into its value set, and reports whether the
// field was restricted (anything other than a bare "*").
func parseCronField(field string, min, max int, names map[string]int, label string) (map[int]bool, bool, error) {
	out := make(map[int]bool)
	restricted := field != "*"
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false, fmt.Errorf("%s field has an empty element", label)
		}
		step := 1
		if slash := strings.Index(part, "/"); slash >= 0 {
			var err error
			step, err = strconv.Atoi(part[slash+1:])
			if err != nil || step <= 0 {
				return nil, false, fmt.Errorf("%s field has an invalid step in %q", label, part)
			}
			part = part[:slash]
		}
		lo, hi := min, max
		if part != "*" {
			dash := strings.Index(part, "-")
			// A leading dash would be a negative value, not a range.
			if dash > 0 {
				var err error
				if lo, err = parseCronValue(part[:dash], names); err != nil {
					return nil, false, fmt.Errorf("%s field: %w", label, err)
				}
				if hi, err = parseCronValue(part[dash+1:], names); err != nil {
					return nil, false, fmt.Errorf("%s field: %w", label, err)
				}
			} else {
				v, err := parseCronValue(part, names)
				if err != nil {
					return nil, false, fmt.Errorf("%s field: %w", label, err)
				}
				lo, hi = v, v
				// A bare value with a step means "from v to the field maximum",
				// the standard n/step reading.
				if step > 1 {
					hi = max
				}
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, false, fmt.Errorf("%s field value out of range in %q (want %d-%d)", label, part, min, max)
		}
		for v := lo; v <= hi; v += step {
			out[v] = true
		}
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("%s field matches nothing", label)
	}
	return out, restricted, nil
}

func parseCronValue(s string, names map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("unrecognized value %q", s)
	}
	return v, nil
}

// next returns the first minute strictly after t that the schedule matches.
// The search advances by the largest safe unit for whichever field mismatched,
// so a yearly pattern resolves in a handful of steps rather than by walking
// every minute.
func (c *cronSchedule) next(t time.Time) (time.Time, error) {
	deadline := t.Add(RepeatHorizon)
	// Strictly after: drop sub-minute precision, then step one minute on.
	cur := t.Truncate(time.Minute).Add(time.Minute)
	for cur.Before(deadline) {
		switch {
		case !c.months[int(cur.Month())]:
			// Jump to midnight on the first of the next month.
			cur = time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, cur.Location()).AddDate(0, 1, 0)
		case !c.matchesDay(cur):
			cur = time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, cur.Location()).AddDate(0, 0, 1)
		case !c.hours[cur.Hour()]:
			cur = time.Date(cur.Year(), cur.Month(), cur.Day(), cur.Hour(), 0, 0, 0, cur.Location()).Add(time.Hour)
		case !c.minutes[cur.Minute()]:
			cur = cur.Add(time.Minute)
		default:
			return cur, nil
		}
	}
	return time.Time{}, fmt.Errorf("no occurrence within %s of %s", RepeatHorizon, t.Format(time.RFC3339))
}

// matchesDay applies the Vixie day rule: when BOTH day-of-month and
// day-of-week are restricted, a day matches if EITHER does; otherwise the
// restricted one alone decides.
func (c *cronSchedule) matchesDay(t time.Time) bool {
	dom := c.doms[t.Day()]
	dow := c.dows[int(t.Weekday())]
	if c.domRestricted && c.dowRestricted {
		return dom || dow
	}
	return dom && dow
}
