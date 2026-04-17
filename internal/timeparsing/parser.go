// Package timeparsing provides layered time parsing for relative date/time expressions.
//
// The parsing follows a layered architecture (ADR-001):
//  1. Compact duration (+6h, -1d, +2w)
//  2. Natural language (tomorrow, next monday)
//  3. Absolute timestamp (RFC3339, date-only)
package timeparsing

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// compactDurationRe matches compact duration patterns: [+-]?(\d+)([hdwmy])
// Examples: +6h, -1d, +2w, 3m, 1y
var compactDurationRe = regexp.MustCompile(`^([+-]?)(\d+)([hdwmy])$`)

// ParseCompactDuration parses compact duration syntax and returns the resulting time.
//
// Format: [+-]?(\d+)([hdwmy])
//
// Units:
//   - h = hours
//   - d = days
//   - w = weeks
//   - m = months
//   - y = years
//
// Examples:
//   - "+6h" -> now + 6 hours
//   - "-1d" -> now - 1 day
//   - "+2w" -> now + 2 weeks
//   - "3m"  -> now + 3 months (no sign = positive)
//   - "1y"  -> now + 1 year
//
// Returns error if input doesn't match the compact duration pattern.
func ParseCompactDuration(s string, now time.Time) (time.Time, error) {
	matches := compactDurationRe.FindStringSubmatch(s)
	if matches == nil {
		return time.Time{}, fmt.Errorf("not a compact duration: %q", s)
	}

	sign := matches[1]
	amountStr := matches[2]
	unit := matches[3]

	amount, err := strconv.Atoi(amountStr)
	if err != nil {
		// Should not happen given regex ensures digits, but handle gracefully
		return time.Time{}, fmt.Errorf("invalid duration amount: %q", amountStr)
	}

	// Apply sign (default positive)
	if sign == "-" {
		amount = -amount
	}

	return applyDuration(now, amount, unit), nil
}

// applyDuration applies the given amount and unit to the base time.
func applyDuration(base time.Time, amount int, unit string) time.Time {
	switch unit {
	case "h":
		return base.Add(time.Duration(amount) * time.Hour)
	case "d":
		return base.AddDate(0, 0, amount)
	case "w":
		return base.AddDate(0, 0, amount*7)
	case "m":
		return base.AddDate(0, amount, 0)
	case "y":
		return base.AddDate(amount, 0, 0)
	default:
		// Should not happen given regex, but return base unchanged
		return base
	}
}

// isCompactDuration returns true if the string matches compact duration syntax.
func isCompactDuration(s string) bool {
	return compactDurationRe.MatchString(s)
}

var (
	nlpInDaysRe  = regexp.MustCompile(`(?i)^in\s+(\d+)\s+(hour|day|week|month|year)s?$`)
	nlpAgoRe     = regexp.MustCompile(`(?i)^(\d+)\s+(hour|day|week|month|year)s?\s+ago$`)
	nlpNextDayRe = regexp.MustCompile(`(?i)^next\s+(monday|tuesday|wednesday|thursday|friday|saturday|sunday)(?:\s+at\s+(\d{1,2})(am|pm))?$`)
	nlpAtTimeRe  = regexp.MustCompile(`(?i)\s+at\s+(\d{1,2})(am|pm)$`)

	weekdayMap = map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
		"wednesday": time.Wednesday, "thursday": time.Thursday,
		"friday": time.Friday, "saturday": time.Saturday,
	}
)

func parseNaturalLanguage(s string, now time.Time) (time.Time, error) {
	lower := strings.TrimSpace(strings.ToLower(s))
	if lower == "" {
		return time.Time{}, fmt.Errorf("not a natural language time expression: %q", s)
	}

	base := now
	atHour := -1

	// Strip trailing "at Xam/pm" for compound expressions like "tomorrow at 9am"
	if m := nlpAtTimeRe.FindStringSubmatch(lower); m != nil {
		h, _ := strconv.Atoi(m[1])
		if strings.ToLower(m[2]) == "pm" && h != 12 {
			h += 12
		} else if strings.ToLower(m[2]) == "am" && h == 12 {
			h = 0
		}
		atHour = h
		lower = strings.TrimSpace(lower[:len(lower)-len(m[0])])
	}

	switch lower {
	case "tomorrow":
		base = now.AddDate(0, 0, 1)
	case "yesterday":
		base = now.AddDate(0, 0, -1)
	case "today":
		// base stays as now
	default:
		// "next <weekday>"
		if m := nlpNextDayRe.FindStringSubmatch(s); m != nil {
			wd := weekdayMap[strings.ToLower(m[1])]
			diff := int(wd) - int(now.Weekday())
			if diff <= 0 {
				diff += 7
			}
			base = now.AddDate(0, 0, diff)
			if m[2] != "" {
				h, _ := strconv.Atoi(m[2])
				if strings.EqualFold(m[3], "pm") && h != 12 {
					h += 12
				} else if strings.EqualFold(m[3], "am") && h == 12 {
					h = 0
				}
				atHour = h
			}
			if atHour >= 0 {
				base = time.Date(base.Year(), base.Month(), base.Day(), atHour, 0, 0, 0, base.Location())
			}
			return base, nil
		}
		// "in N units"
		if m := nlpInDaysRe.FindStringSubmatch(s); m != nil {
			n, _ := strconv.Atoi(m[1])
			return applyNLPUnit(now, n, strings.ToLower(m[2])), nil
		}
		// "N units ago"
		if m := nlpAgoRe.FindStringSubmatch(s); m != nil {
			n, _ := strconv.Atoi(m[1])
			return applyNLPUnit(now, -n, strings.ToLower(m[2])), nil
		}
		return time.Time{}, fmt.Errorf("not a natural language time expression: %q", s)
	}

	if atHour >= 0 {
		base = time.Date(base.Year(), base.Month(), base.Day(), atHour, 0, 0, 0, base.Location())
	}
	return base, nil
}

func applyNLPUnit(base time.Time, n int, unit string) time.Time {
	switch unit {
	case "hour":
		return base.Add(time.Duration(n) * time.Hour)
	case "day":
		return base.AddDate(0, 0, n)
	case "week":
		return base.AddDate(0, 0, n*7)
	case "month":
		return base.AddDate(0, n, 0)
	case "year":
		return base.AddDate(n, 0, 0)
	}
	return base
}

// dateOnlyRe matches date-only format YYYY-MM-DD to avoid NLP misinterpretation.
var dateOnlyRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ParseRelativeTime parses a time expression using the layered architecture (ADR-001).
//
// Parsing order:
//  1. Compact duration (+6h, -1d, +2w)
//  2. Absolute formats (date-only, RFC3339) - checked before NLP to avoid misinterpretation
//  3. Natural language (tomorrow, next monday)
//
// Returns the parsed time or an error if no layer could parse the input.
func ParseRelativeTime(s string, now time.Time) (time.Time, error) {
	// Layer 1: Compact duration
	if t, err := ParseCompactDuration(s, now); err == nil {
		return t, nil
	}

	// Layer 2: Absolute formats (must be checked before NLP to avoid misinterpretation)
	// NLP parser can incorrectly parse "2025-02-01" as a time, so we check date formats first.

	// Try date-only format (YYYY-MM-DD)
	if dateOnlyRe.MatchString(s) {
		if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
			return t, nil
		}
	}

	// Try RFC3339 format (2025-01-15T10:00:00Z)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try ISO 8601 datetime without timezone (2025-01-15T10:00:00)
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local); err == nil {
		return t, nil
	}

	// Try datetime with space (2025-01-15 10:00:00)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t, nil
	}

	// Layer 3: Natural language (after absolute formats to avoid misinterpretation)
	if t, err := parseNaturalLanguage(s, now); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("cannot parse time expression: %q (examples: +6h, tomorrow, 2025-01-15)", s)
}
