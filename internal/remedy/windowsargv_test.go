package remedy

import (
	"slices"
	"strings"
	"testing"
)

// windowsArgv splits a command line into the arguments a Go program on
// Windows receives for it, so a test on any host can check how bd.exe reads a
// command a remedy prints for Windows. It follows the rules Microsoft
// documents for the C runtime ("Parsing C command-line arguments"), which Go's
// os package applies too:
//
//   - arguments are separated by spaces and tabs outside double quotes;
//   - a double quote starts or ends a quoted run and is dropped, except that
//     inside one, a doubled quote ("") stands for one literal quote and ends
//     the run (the rule before 2008, which Go keeps);
//   - backslashes are literal unless a double quote follows them: then 2n of
//     them stand for n backslashes and the quote acts as above, and 2n+1 of
//     them stand for n backslashes and a literal quote.
func windowsArgv(cmd string) []string {
	var args []string
	i := 0
	for {
		for i < len(cmd) && (cmd[i] == ' ' || cmd[i] == '\t') {
			i++
		}
		if i == len(cmd) {
			return args
		}
		var arg strings.Builder
		quoted := false
	scan:
		for i < len(cmd) {
			switch c := cmd[i]; {
			case (c == ' ' || c == '\t') && !quoted:
				break scan
			case c == '\\':
				end := i
				for end < len(cmd) && cmd[end] == '\\' {
					end++
				}
				n := end - i
				if end == len(cmd) || cmd[end] != '"' {
					arg.WriteString(cmd[i:end])
					i = end
					continue
				}
				arg.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					arg.WriteByte('"')
					i = end + 1
					continue
				}
				// An even run leaves the quote after it to act as a quote.
				i = end
			case c == '"':
				if quoted && i+1 < len(cmd) && cmd[i+1] == '"' {
					arg.WriteByte('"')
					quoted = false
					i += 2
					continue
				}
				quoted = !quoted
				i++
			default:
				arg.WriteByte(c)
				i++
			}
		}
		args = append(args, arg.String())
	}
}

// TestWindowsArgv pins the parser above on the examples Microsoft documents,
// and on the doubled quote inside quotes that Go reads by the rule before
// 2008.
func TestWindowsArgv(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cmd  string
		want []string
	}{
		{`"a b c" d e`, []string{"a b c", "d", "e"}},
		{`"ab\"c" "\\" d`, []string{`ab"c`, `\`, "d"}},
		{`a\\\b d"e f"g h`, []string{`a\\\b`, "de fg", "h"}},
		{`a\\\"b c d`, []string{`a\"b`, "c", "d"}},
		{`a\\\\"b c" d e`, []string{`a\\b c`, "d", "e"}},
		{`a"b"" c d`, []string{`ab"`, "c", "d"}},
		{`bd sql "x" ""`, []string{"bd", "sql", "x", ""}},
		{"  a\t b\\", []string{"a", `b\`}},
	} {
		if got := windowsArgv(tc.cmd); !slices.Equal(got, tc.want) {
			t.Errorf("windowsArgv(%s) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}
