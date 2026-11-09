package doctor

import (
	"regexp"
	"strings"
)

// testPrefixPattern matches the leading words of a test-style title. It is
// applied to the lowercased title.
var testPrefixPattern = regexp.MustCompile(`^(test|benchmark|sample|tmp|temp|debug|dummy)[-_\s]`)

// IsTestTitle reports whether title starts with a test-style word such as
// "test-" or "tmp_". It is the predicate bd create (warning, description
// requirement) and bd export --scrub use; it is not by itself evidence of
// pollution.
func IsTestTitle(title string) bool {
	return testPrefixPattern.MatchString(strings.ToLower(title))
}
