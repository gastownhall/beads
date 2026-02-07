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
	"time"

	"github.com/olebedev/when"
	"github.com/olebedev/when/rules/common"
	"github.com/olebedev/when/rules/en"
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

// nlpParser is the singleton natural language parser (olebedev/when).
// Initialized lazily on first use.
var nlpParser *when.Parser

// getNLPParser returns the singleton NLP parser, initializing it if needed.
func getNLPParser() *when.Parser {
	if nlpParser == nil {
		nlpParser = when.New(nil)
		nlpParser.Add(en.All...)
		nlpParser.Add(common.All...)
	}
	return nlpParser
}

// parseNaturalLanguage parses natural language time expressions using olebedev/when.
//
// Examples:
//   - "tomorrow" -> tomorrow at current time
//   - "next monday" -> next Monday at current time
//   - "next monday at 9am" -> next Monday at 9:00
//   - "in 3 days" -> now + 3 days
//   - "3 days ago" -> now - 3 days
//
// Known Issues (olebedev/when):
//   - Month name "September" may not parse correctly in some contexts.
//     Workaround: Use date format "2025-09-15" instead of "September 15" or "Sep 15".
//     This is a known issue in the olebedev/when library.
//
// Returns error if input cannot be parsed as natural language.
func parseNaturalLanguage(s string, now time.Time) (time.Time, error) {
	parser := getNLPParser()
	result, err := parser.Parse(s, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("NLP parse error: %w", err)
	}
	if result == nil {
		return time.Time{}, fmt.Errorf("not a natural language time expression: %q", s)
	}
	return result.Time, nil
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
