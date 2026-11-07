package scripts_test

import "slices"
import "strings"

// bazelInvocations returns the arguments (after `bazel <verb>`) of every
// `bazel <verb>` command in a step's run script whose verb is in verbs. A
// command continued over several lines with a trailing backslash is one
// command: its continuation lines' arguments are included. Arguments stop at
// the first shell pipe or list operator.
//
// Helper-only file (no Test functions, like repo_helpers_test.go): shared by
// scripts_test's nogo_lint_policy_test.go and nogo_integration_scan_test.go's
// dedicated go_test target (independent review of #7486, cost fix), so each
// can declare only the data it reads without duplicating this parser.
func bazelInvocations(run string, verbs ...string) [][]string {
	var out [][]string
	for _, line := range strings.Split(strings.ReplaceAll(run, "\\\n", " "), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "bazel" || !slices.Contains(verbs, fields[1]) {
			continue
		}
		var args []string
		for _, f := range fields[2:] {
			if f == "|" || f == "||" || f == "&&" || f == ";" {
				break
			}
			args = append(args, f)
		}
		out = append(out, args)
	}
	return out
}
