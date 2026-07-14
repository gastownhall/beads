package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	closingIssuesOne  = `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[{"id":"I_kwDOExample"}]}}}}}`
	closingIssuesZero = `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[]}}}}}`
)

type preflightFixture struct {
	prArg           string
	returnedNumber  string
	canonicalURL    string
	graphQLResponse string
	graphQLExit     string
}

type preflightRun struct {
	output string
	calls  [][]string
	err    error
}

func TestPRPreflightSupportsGitHubCLIWithoutClosingIssuesReferences(t *testing.T) {
	t.Run("numeric lookup uses exact canonical GraphQL coordinates", func(t *testing.T) {
		run := runPRPreflightWithFakeGH(t, preflightFixture{})
		if run.err != nil {
			t.Fatalf("pr-preflight failed with old gh field set: %v\n%s", run.err, run.output)
		}
		if !strings.Contains(run.output, "[pass] PR references at least one closing issue.") {
			t.Fatalf("pr-preflight did not preserve the closing-issue check:\n%s", run.output)
		}

		prView := requireGHCall(t, run.calls, "pr", "view")
		if strings.Contains(requireFlagValue(t, prView, "--json"), "closingIssuesReferences") {
			t.Fatalf("pr view still requested the unsupported field: %q", prView)
		}

		api := requireGHCall(t, run.calls, "api", "graphql")
		if len(api) != 12 {
			t.Fatalf("unexpected GraphQL argv length: %q", api)
		}
		wantFixed := map[int]string{
			0: "api", 1: "graphql", 2: "--hostname", 3: "github.com",
			4: "-f", 6: "-f", 7: "owner=octo", 8: "-f", 9: "name=repo",
			10: "-F", 11: "number=7",
		}
		for index, want := range wantFixed {
			if api[index] != want {
				t.Fatalf("GraphQL argv[%d] = %q, want %q; argv=%q", index, api[index], want, api)
			}
		}
		if !strings.HasPrefix(api[5], "query=") || !strings.Contains(api[5], "closingIssuesReferences(first: 1) { nodes { id } }") {
			t.Fatalf("GraphQL query does not request closing-issue presence: %q", api[5])
		}
		for _, binding := range []string{
			"query($owner: String!, $name: String!, $number: Int!)",
			"repository(owner: $owner, name: $name)",
			"pullRequest(number: $number)",
		} {
			if !strings.Contains(api[5], binding) {
				t.Fatalf("GraphQL query does not bind %q: %q", binding, api[5])
			}
		}
	})

	t.Run("canonical PR URL and zero count preserve warning behavior", func(t *testing.T) {
		run := runPRPreflightWithFakeGH(t, preflightFixture{
			prArg:           "https://github.com/octo/repo/pull/7",
			graphQLResponse: closingIssuesZero,
		})
		if run.err != nil {
			t.Fatalf("URL preflight with zero closing issues failed: %v\n%s", run.err, run.output)
		}
		if !strings.Contains(run.output, "[warn] No closing issue reference found.") {
			t.Fatalf("zero closing issues did not retain the warning:\n%s", run.output)
		}
	})

	tests := []struct {
		name        string
		fixture     preflightFixture
		wantError   string
		wantAPICall bool
	}{
		{
			name:        "GraphQL command failure",
			fixture:     preflightFixture{graphQLExit: "42"},
			wantError:   "error: could not query closing issue references for octo/repo#7",
			wantAPICall: true,
		},
		{
			name: "malformed successful response",
			fixture: preflightFixture{
				graphQLResponse: `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":"malformed"}}}}}`,
			},
			wantError:   "error: invalid closing issue response for octo/repo#7",
			wantAPICall: true,
		},
		{
			name: "valid response followed by malformed response",
			fixture: preflightFixture{
				graphQLResponse: closingIssuesOne + "\n" + `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":"malformed"}}}}}`,
			},
			wantError:   "error: invalid closing issue response for octo/repo#7",
			wantAPICall: true,
		},
		{
			name: "multiple valid responses",
			fixture: preflightFixture{
				graphQLResponse: closingIssuesZero + "\n" + closingIssuesOne,
			},
			wantError:   "error: invalid closing issue response for octo/repo#7",
			wantAPICall: true,
		},
		{
			name: "partial data with GraphQL errors",
			fixture: preflightFixture{
				graphQLResponse: `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[{"id":"I_kwDOExample"}]}}}},"errors":[{"message":"partial failure"}]}`,
			},
			wantError:   "error: invalid closing issue response for octo/repo#7",
			wantAPICall: true,
		},
		{
			name: "closing issue node missing id",
			fixture: preflightFixture{
				graphQLResponse: `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[{}]}}}}}`,
			},
			wantError:   "error: invalid closing issue response for octo/repo#7",
			wantAPICall: true,
		},
		{
			name: "more than one node in first-one response",
			fixture: preflightFixture{
				graphQLResponse: `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[{"id":"I_one"},{"id":"I_two"}]}}}}}`,
			},
			wantError:   "error: invalid closing issue response for octo/repo#7",
			wantAPICall: true,
		},
		{
			name:        "invalid canonical URL",
			fixture:     preflightFixture{canonicalURL: "https://github.com/octo/repo/issues/7"},
			wantError:   "error: invalid canonical GitHub PR URL: https://github.com/octo/repo/issues/7",
			wantAPICall: false,
		},
		{
			name: "URL and response number mismatch",
			fixture: preflightFixture{
				canonicalURL: "https://github.com/octo/repo/pull/8",
			},
			wantError:   "error: PR URL number does not match returned PR number: https://github.com/octo/repo/pull/8",
			wantAPICall: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runPRPreflightWithFakeGH(t, test.fixture)
			if exitCode(run.err) != 2 {
				t.Fatalf("exit = %d, want 2; error=%v\n%s", exitCode(run.err), run.err, run.output)
			}
			if !strings.Contains(run.output, test.wantError) {
				t.Fatalf("missing exact failure %q:\n%s", test.wantError, run.output)
			}
			if strings.Contains(run.output, "No closing issue reference found") {
				t.Fatalf("failure became an absent-reference warning:\n%s", run.output)
			}
			if hasGHCall(run.calls, "api", "graphql") != test.wantAPICall {
				t.Fatalf("GraphQL call presence = %v, want %v; calls=%q", hasGHCall(run.calls, "api", "graphql"), test.wantAPICall, run.calls)
			}
		})
	}
}

func runPRPreflightWithFakeGH(t *testing.T, fixture preflightFixture) preflightRun {
	t.Helper()

	if fixture.prArg == "" {
		fixture.prArg = "7"
	}
	if fixture.returnedNumber == "" {
		fixture.returnedNumber = "7"
	}
	if fixture.canonicalURL == "" {
		fixture.canonicalURL = "https://github.com/octo/repo/pull/7"
	}
	if fixture.graphQLResponse == "" {
		fixture.graphQLResponse = closingIssuesOne
	}
	if fixture.graphQLExit == "" {
		fixture.graphQLExit = "0"
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is required to test pr-preflight.sh: %v", err)
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skipf("jq is required to test pr-preflight.sh: %v", err)
	}

	bin := t.TempDir()
	callLog := filepath.Join(t.TempDir(), "gh-calls")
	if err := os.WriteFile(callLog, nil, 0o600); err != nil {
		t.Fatalf("initialize fake gh call log: %v", err)
	}
	writeExecutable(t, filepath.Join(bin, "gh"), `#!/bin/sh
set -eu
printf '%s\0' "$#" >>"$GH_CALL_LOG"
printf '%s\0' "$@" >>"$GH_CALL_LOG"

case "$1 $2" in
  "pr view")
    for arg in "$@"; do
      case "$arg" in
        *closingIssuesReferences*)
          printf 'Unknown JSON field: "closingIssuesReferences"\n' >&2
          exit 1
          ;;
      esac
    done
    printf '%s\n' "{\"number\":${PR_NUMBER},\"title\":\"Compatibility fixture\",\"author\":{\"login\":\"contributor\"},\"url\":\"${PR_URL}\",\"baseRefName\":\"main\",\"headRefName\":\"fix/preflight\",\"headRepositoryOwner\":{\"login\":\"contributor\"},\"isCrossRepository\":true,\"isDraft\":false,\"maintainerCanModify\":true,\"mergeStateStatus\":\"CLEAN\",\"mergeable\":\"MERGEABLE\",\"reviewDecision\":\"APPROVED\",\"changedFiles\":1,\"additions\":1,\"deletions\":0,\"files\":[{\"path\":\"README.md\"}],\"statusCheckRollup\":[],\"latestReviews\":[]}"
    ;;
  "run list")
    printf '[]\n'
    ;;
  "api graphql")
    if [ "$GRAPHQL_EXIT" != 0 ]; then
      printf 'simulated GraphQL failure\n' >&2
      exit "$GRAPHQL_EXIT"
    fi
    printf '%s\n' "$GRAPHQL_RESPONSE"
    ;;
  *)
    printf 'unexpected gh invocation\n' >&2
    exit 68
    ;;
esac
`)

	root := preflightRepoRoot(t)
	cmd := exec.Command(bash, shellPath(t, filepath.Join(root, "scripts", "pr-preflight.sh")), fixture.prArg, "--repo", "default/repo")
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + shellPath(t, bin) + ":" + bashPathList(t, os.Getenv("PATH")) + ":/usr/bin:/bin",
		"HOME=" + shellPath(t, t.TempDir()),
		"LC_ALL=C",
		"LANG=C",
		"GH_CALL_LOG=" + shellPath(t, callLog),
		"GRAPHQL_EXIT=" + fixture.graphQLExit,
		"GRAPHQL_RESPONSE=" + fixture.graphQLResponse,
		"PR_NUMBER=" + fixture.returnedNumber,
		"PR_URL=" + fixture.canonicalURL,
	}
	out, runErr := cmd.CombinedOutput()
	logBytes, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatalf("read fake gh call log: %v", readErr)
	}
	return preflightRun{output: string(out), calls: parseGHCalls(t, logBytes), err: runErr}
}

func parseGHCalls(t *testing.T, log []byte) [][]string {
	t.Helper()
	tokens := strings.Split(string(log), "\x00")
	tokens = tokens[:len(tokens)-1]
	var calls [][]string
	for len(tokens) > 0 {
		count, err := strconv.Atoi(tokens[0])
		if err != nil || count < 0 || len(tokens) < count+1 {
			t.Fatalf("invalid fake gh call log: %q", log)
		}
		calls = append(calls, append([]string(nil), tokens[1:count+1]...))
		tokens = tokens[count+1:]
	}
	return calls
}

func requireGHCall(t *testing.T, calls [][]string, prefix ...string) []string {
	t.Helper()
	for _, call := range calls {
		if len(call) >= len(prefix) {
			match := true
			for index := range prefix {
				match = match && call[index] == prefix[index]
			}
			if match {
				return call
			}
		}
	}
	t.Fatalf("missing gh call with prefix %q; calls=%q", prefix, calls)
	return nil
}

func hasGHCall(calls [][]string, prefix ...string) bool {
	for _, call := range calls {
		if len(call) < len(prefix) {
			continue
		}
		match := true
		for index := range prefix {
			match = match && call[index] == prefix[index]
		}
		if match {
			return true
		}
	}
	return false
}

func requireFlagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	t.Fatalf("missing %s value in %q", flag, args)
	return ""
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func preflightRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}
