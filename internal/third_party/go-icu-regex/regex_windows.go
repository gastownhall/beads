//go:build windows

// Copyright 2023 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package regex

import (
	"context"
	"fmt"
	"regexp"

	"gopkg.in/src-d/go-errors.v1"
)

// Regex is an interface that wraps around the ICU library, exposing ICU's regular expression functionality. It is
// imperative that Regex is closed once it is finished.
type Regex interface {
	// SetRegexString sets the string that will later be matched against. This must be called at least once before any other
	// calls are made (except for Close).
	SetRegexString(ctx context.Context, regexStr string, flags RegexFlags) error
	// SetMatchString sets the string that we will either be matching against, or executing the replacements on. This
	// must be called after SetRegexString, but before any other calls.
	SetMatchString(ctx context.Context, matchStr string) error
	// IndexOf returns the index of the previously-set regex matching the previously-set match string. Must call
	// SetRegexString and SetMatchString before this function. `endIndex` determines whether the returned index is at
	// the beginning or end of the match. `start` and `occurrence` start at 1, not 0. Returns 0 if the index was not found.
	IndexOf(ctx context.Context, start int, occurrence int, endIndex bool) (int, error)
	// Matches returns whether the previously-set regex matches the previously-set match string. Must call
	// SetRegexString and SetMatchString before this function.
	Matches(ctx context.Context, start int, occurrence int) (bool, error)
	// Replace returns a new string with the replacement string occupying the matched portions of the match string,
	// based on the regex. Position starts at 1, not 0. Must call SetRegexString and SetMatchString before this function.
	Replace(ctx context.Context, replacementStr string, position int, occurrence int) (string, error)
	// Substring returns the match of the previously-set match string, using the previously-set regex. Must call
	// SetRegexString and SetMatchString before this function. `start` and `occurrence` start at 1, not 0.
	Substring(ctx context.Context, start int, occurrence int) (string, bool, error)
	// Close frees up the internal resources. This MUST be called, else a panic will occur at some non-deterministic time.
	Close() error
}

var (
	// ErrRegexNotYetSet is returned when attempting to use another function before the regex has been initialized.
	ErrRegexNotYetSet = errors.NewKind("SetRegexString must be called before any other function")
	// ErrMatchNotYetSet is returned when attempting to use another function before the match string has been set.
	ErrMatchNotYetSet = errors.NewKind("SetMatchString must be called as there is nothing to match against")
	// ErrInvalidRegex is returned when an invalid regex is given
	ErrInvalidRegex = errors.NewKind("the given regular expression is invalid")
)

// RegexFlags are flags to define the behavior of the regular expression. Use OR (|) to combine flags. All flag values
// were taken directly from ICU.
type RegexFlags uint32

const (
	// Enable case insensitive matching.
	RegexFlags_None RegexFlags = 0

	// Enable case insensitive matching.
	RegexFlags_Case_Insensitive RegexFlags = 2

	// Allow white space and comments within patterns.
	RegexFlags_Comments RegexFlags = 4

	// If set, '.' matches line terminators,  otherwise '.' matching stops at line end.
	RegexFlags_Dot_All RegexFlags = 32

	// If set, treat the entire pattern as a literal string. Metacharacters or escape sequences in the input sequence
	// will be given no special meaning.
	//
	// The flag RegexFlags_Case_Insensitive retains its impact on matching when used in conjunction with this flag. The
	// other flags become superfluous.
	RegexFlags_Literal RegexFlags = 16

	// Control behavior of "$" and "^". If set, recognize line terminators within string, otherwise, match only at start
	// and end of input string.
	RegexFlags_Multiline RegexFlags = 8

	// Unix-only line endings. When this mode is enabled, only '\n' is recognized as a line ending in the behavior
	// of ., ^, and $.
	RegexFlags_Unix_Lines RegexFlags = 1

	// Unicode word boundaries. If set, \b uses the Unicode TR 29 definition of word boundaries. Warning: Unicode word
	// boundaries are quite different from traditional regular expression word boundaries.
	// See http://unicode.org/reports/tr29/#Word_Boundaries
	RegexFlags_Unicode_Word RegexFlags = 256

	// Error on Unrecognized backslash escapes. If set, fail with an error on patterns that contain backslash-escaped
	// ASCII letters without a known special meaning. If this flag is not set, these escaped letters represent
	// themselves.
	RegexFlags_Error_On_Unknown_Escapes RegexFlags = 512
)

// CreateRegex creates a Regex. |stringBufferInBytes| is a hint to allocate string buffers
// for a certain size to avoid reallocation in the future, but is currently unused by the
// primary implementation.
func CreateRegex(stringBufferInBytes uint32) Regex {
	return &privateRegex{}
}

// privateRegex is the private implementation of the Regex interface for Windows.
type privateRegex struct {
	re   *regexp.Regexp
	str  string
	sset bool

	done  bool
	start int
	locs  [][]int
}

var _ Regex = (*privateRegex)(nil)

// SetRegexString implements the interface Regex.
func (pr *privateRegex) SetRegexString(ctx context.Context, regexStr string, flags RegexFlags) (err error) {
	_ = ctx

	// i : RegexFlags_Case_Insensitive
	// m : RegexFlags_Multiline
	// s : RegexFlags_Dot_All
	//     RegexFlags_Unix_Lines
	var flg = "(?"
	if flags&RegexFlags_Case_Insensitive != 0 {
		flg += "i"
	}
	if flags&RegexFlags_Multiline != 0 {
		flg += "m"
	}
	if flags&RegexFlags_Dot_All != 0 {
		flg += "s"
	}
	if len(flg) > 2 {
		flg += ")"
	} else {
		flg = ""
	}

	if flags&RegexFlags_Literal != 0 {
		regexStr = regexp.QuoteMeta(regexStr)
	}

	pr.done = false
	pr.sset = false
	pr.re, err = regexp.Compile(flg + regexStr)
	if err != nil {
		return ErrInvalidRegex.New()
	}
	return nil
}

// SetMatchString implements the interface Regex.
func (pr *privateRegex) SetMatchString(ctx context.Context, matchStr string) (err error) {
	_ = ctx
	if pr.re == nil {
		return ErrRegexNotYetSet.New()
	}
	pr.done = false
	pr.str = matchStr
	pr.sset = true
	return nil
}

func (pr *privateRegex) do(start int) error {
	if start < 1 {
		start = 1
	}
	if !pr.done || pr.start != start {
		if pr.re == nil {
			return ErrRegexNotYetSet.New()
		}
		if !pr.sset {
			return ErrMatchNotYetSet.New()
		}
		pr.locs = pr.re.FindAllStringIndex(pr.str[start-1:], -1)
		pr.start = start
		pr.done = true
	}
	return nil
}

func (pr *privateRegex) location(occurrence int) []int {
	occurrence--
	if occurrence < 0 {
		occurrence = 0
	}
	if len(pr.locs) < occurrence+1 {
		return nil
	}
	return pr.locs[occurrence]
}

// IndexOf implements the interface Regex.
func (pr *privateRegex) IndexOf(ctx context.Context, start int, occurrence int, endIndex bool) (int, error) {
	_ = ctx
	err := pr.do(start)
	if err != nil {
		return 0, err
	}
	loc := pr.location(occurrence)
	if loc == nil {
		return 0, nil
	}
	pos := loc[0]
	if endIndex {
		pos = loc[1]
	}
	return pos + pr.start, nil
}

// Matches implements the interface Regex.
func (pr *privateRegex) Matches(ctx context.Context, start int, occurrence int) (bool, error) {
	_ = ctx
	err := pr.do(start + 1)
	if err != nil {
		return false, err
	}
	loc := pr.location(occurrence)
	return loc != nil, nil
}

// Replace implements the interface Regex.
func (pr *privateRegex) Replace(ctx context.Context, replacement string, start int, occurrence int) (string, error) {
	_ = ctx
	err := pr.do(start)
	if err != nil {
		return "", err
	}

	var locs [][]int
	if occurrence == 0 {
		locs = pr.locs
	} else {
		loc := pr.location(occurrence)
		if loc != nil {
			locs = [][]int{loc}
		}
	}
	offs := pr.start - 1
	pos := offs
	ret := []byte(pr.str[:pos])
	for _, loc := range locs {
		ret = fmt.Appendf(ret, "%s%s", pr.str[pos:loc[0]+offs], replacement)
		pos = loc[1] + offs
	}
	ret = fmt.Append(ret, pr.str[pos:])
	return string(ret), nil
}

// Substring implements the interface Regex.
func (pr *privateRegex) Substring(ctx context.Context, start int, occurrence int) (string, bool, error) {
	_ = ctx
	err := pr.do(start)
	if err != nil {
		return "", false, err
	}
	loc := pr.location(occurrence)
	if loc == nil {
		return "", false, nil
	}
	return pr.str[loc[0]+pr.start-1 : loc[1]+pr.start-1], true, nil
}

// Close implements the interface Regex.
func (pr *privateRegex) Close() (err error) {
	pr.re = nil
	pr.str = ""
	pr.done = false
	pr.locs = nil
	return nil
}
