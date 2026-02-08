//go:build !windows

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

	"gopkg.in/src-d/go-errors.v1"

	"github.com/dolthub/go-icu-regex/internal/icu"
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

// privateRegex is the private implementation of the Regex interface.
type privateRegex struct {
	regexPtr *icu.URegularExpression
	regexStr icu.UCharStr
	matchStr icu.UCharStr
	matchSet bool
}

var _ Regex = (*privateRegex)(nil)

// SetRegexString implements the interface Regex.
func (pr *privateRegex) SetRegexString(ctx context.Context, regexStr string, flags RegexFlags) (err error) {
	if pr.regexPtr != nil {
		pr.regexPtr.Free()
		pr.regexPtr = nil
	}

	pr.regexStr.SetString(regexStr)
	pr.matchSet = false

	// Create the URegularExpression*
	errorCode := icu.UErrorCode(0)
	regex := icu.Uregex_open(&pr.regexStr, uint32(flags), &errorCode)
	if errorCode > 0 {
		return ErrInvalidRegex.New()
	}
	pr.regexPtr = regex
	return nil
}

// SetMatchString implements the interface Regex.
func (pr *privateRegex) SetMatchString(ctx context.Context, matchStr string) (err error) {
	// Check for the regex pointer first
	if pr.regexPtr == nil {
		return ErrRegexNotYetSet.New()
	}

	pr.matchStr.SetString(matchStr)
	pr.matchSet = true

	// Set the text on the URegularExpression*
	errorCode := icu.UErrorCode(0)
	icu.Uregex_setText(pr.regexPtr, &pr.matchStr, &errorCode)
	if errorCode > 0 {
		return fmt.Errorf("unexpected UErrorCode from uregex_setText: %d", errorCode)
	}
	return nil
}

// IndexOf implements the interface Regex.
func (pr *privateRegex) IndexOf(ctx context.Context, start int, occurrence int, endIndex bool) (int, error) {
	// Check for the regex pointer first
	if pr.regexPtr == nil {
		return 0, ErrRegexNotYetSet.New()
	}

	// Check that the match string has been set
	if !pr.matchSet {
		return 0, ErrMatchNotYetSet.New()
	}

	// Look for a match
	var errorCode icu.UErrorCode
	ok := icu.Uregex_find(pr.regexPtr, start-1, &errorCode)
	for i := 1; i < occurrence && ok; i++ {
		ok = icu.Uregex_findNext(pr.regexPtr, &errorCode)
	}
	if !ok {
		return 0, nil
	}

	// Get the index of the match
	var index int
	if endIndex {
		index32 := icu.Uregex_end(pr.regexPtr, 0, &errorCode)
		index = int(index32)
	} else {
		index32 := icu.Uregex_start(pr.regexPtr, 0, &errorCode)
		index = int(index32)
	}
	if errorCode > 0 {
		return 0, fmt.Errorf("unexpected UErrorCode from uregex_find/uregex_findNext: %d", errorCode)
	}

	return index + 1, nil
}

// Matches implements the interface Regex.
func (pr *privateRegex) Matches(ctx context.Context, start int, occurrence int) (ok bool, err error) {
	// Check for the regex pointer first
	if pr.regexPtr == nil {
		return false, ErrRegexNotYetSet.New()
	}

	// Check that the match string has been set
	if !pr.matchSet {
		return false, ErrMatchNotYetSet.New()
	}

	// Return if we found a match
	var errorCode icu.UErrorCode
	ok = icu.Uregex_find(pr.regexPtr, start, &errorCode)
	for i := 1; i < occurrence && ok; i++ {
		ok = icu.Uregex_findNext(pr.regexPtr, &errorCode)
	}
	if errorCode > 0 {
		return false, fmt.Errorf("unexpected UErrorCode from uregex_find/uregex_findNext: %d", errorCode)
	}
	return ok, err
}

// Replace implements the interface Regex.
func (pr *privateRegex) Replace(ctx context.Context, replacement string, start int, occurrence int) (replacedStr string, err error) {
	// Check for the regex pointer first
	if pr.regexPtr == nil {
		return "", ErrRegexNotYetSet.New()
	}

	// Check that the match string has been set
	if !pr.matchSet {
		return "", ErrMatchNotYetSet.New()
	}

	return icu.Replace(pr.regexPtr, replacement, &pr.matchStr, start-1, occurrence), nil
}

// Substring implements the interface Regex.
func (pr *privateRegex) Substring(ctx context.Context, start int, occurrence int) (string, bool, error) {
	// Check for the regex pointer first
	if pr.regexPtr == nil {
		return "", false, ErrRegexNotYetSet.New()
	}

	// Check that the match string has been set
	if !pr.matchSet {
		return "", false, ErrMatchNotYetSet.New()
	}

	// Look for a match
	var errorCode icu.UErrorCode
	ok := icu.Uregex_find(pr.regexPtr, start-1, &errorCode)
	for i := 1; i < occurrence && ok; i++ {
		ok = icu.Uregex_findNext(pr.regexPtr, &errorCode)
	}
	if !ok {
		return "", false, nil
	}

	// Get the bounds of the match
	idxStart := icu.Uregex_start(pr.regexPtr, 0, &errorCode)
	idxEnd := icu.Uregex_end(pr.regexPtr, 0, &errorCode)
	if errorCode > 0 {
		return "", false, fmt.Errorf("unexpected UErrorCode from uregex_find/uregex_findNext: %d", errorCode)
	}

	return pr.matchStr.GetSubstring(int(idxStart), int(idxEnd)), true, nil
}

// Close implements the interface Regex.
func (pr *privateRegex) Close() (err error) {
	if pr == nil {
		return nil
	}
	if pr.regexPtr != nil {
		pr.regexPtr.Free()
		pr.regexPtr = nil
	}
	pr.matchStr.Free()
	pr.regexStr.Free()
	return nil
}
