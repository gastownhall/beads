//go:build integration

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
	fakeGHFunction    = `gh() {
  if [ "${PR_PREFLIGHT_HOST_SENTINEL:-}" = present ]; then
    printf 'inherited host environment reached fake gh\n' >&2
    return 70
  fi
  case ":${BASHOPTS:-}:" in
    *:failglob:*)
      printf 'inherited failglob option reached fake gh\n' >&2
      return 71
      ;;
  esac

  printf '%s\0' "$#" >>"$GH_CALL_LOG"
  printf '%s\0' "$@" >>"$GH_CALL_LOG"

  case "$1 $2" in
    "pr view")
      for arg in "$@"; do
        case "$arg" in
          *closingIssuesReferences*)
            printf 'Unknown JSON field: "closingIssuesReferences"\n' >&2
            return 1
            ;;
        esac
      done
      printf '%s\n' "{\"number\":${PR_NUMBER},\"title\":\"Compatibility fixture\",\"author\":{\"login\":\"contributor\"},\"url\":\"${PR_URL}\",\"baseRefName\":\"main\",\"headRefName\":\"fix/preflight\",\"headRepositoryOwner\":{\"login\":\"contributor\"},\"isCrossRepository\":true,\"isDraft\":false,\"maintainerCanModify\":true,\"mergeStateStatus\":\"CLEAN\",\"mergeable\":\"MERGEABLE\",\"reviewDecision\":\"APPROVED\",\"changedFiles\":1,\"additions\":1,\"deletions\":0,\"files\":[{\"path\":\"README.md\"}],\"statusCheckRollup\":${PR_ROLLUP:-[]},\"latestReviews\":[]}"
      ;;
    "run list")
      printf '[]\n'
      ;;
    "api graphql")
      if [ "$GRAPHQL_EXIT" != 0 ]; then
        printf 'simulated GraphQL failure\n' >&2
        return "$GRAPHQL_EXIT"
      fi
      printf '%s\n' "$GRAPHQL_RESPONSE"
      ;;
    *)
      printf 'unexpected gh invocation\n' >&2
      return 68
      ;;
  esac
}
`
)

type preflightFixture struct {
	prArg           string
	returnedNumber  string
	canonicalURL    string
	graphQLResponse string
	graphQLExit     string
	rollup          string
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

func TestPRPreflightFixtureIgnoresHostShellAndGitHubState(t *testing.T) {
	hostState := t.TempDir()
	startupMarker := filepath.Join(hostState, "startup-invoked")
	hostStartup := filepath.Join(hostState, "host-startup.sh")
	if err := os.WriteFile(
		hostStartup,
		[]byte("printf 'invoked\\n' >"+shSingleQuote(msysPath(startupMarker))+"\nexit 97\n"),
		0o600,
	); err != nil {
		t.Fatalf("write hostile host startup: %v", err)
	}

	t.Setenv("BASH_ENV", msysPath(hostStartup))
	t.Setenv("ENV", msysPath(hostStartup))
	t.Setenv("BASHOPTS", "failglob")
	t.Setenv("SHELLOPTS", "nounset")
	t.Setenv("GH_CONFIG_DIR", filepath.Join(hostState, "gh"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(hostState, "gitconfig"))
	t.Setenv("PR_PREFLIGHT_HOST_SENTINEL", "present")

	run := runPRPreflightWithFakeGH(t, preflightFixture{})
	if run.err != nil {
		t.Fatalf("curated preflight process failed under hostile host state: %v\n%s", run.err, run.output)
	}
	if _, err := os.Stat(startupMarker); err == nil {
		t.Fatal("preflight process sourced the inherited host startup file")
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect hostile startup marker: %v", err)
	}
	if len(run.calls) == 0 {
		t.Fatal("controlled gh function did not intercept any calls")
	}
}

// A head SHA whose earlier check suites were cancelled or failed keeps those
// stale entries in statusCheckRollup next to the fresh green re-runs of the
// same checks. GitHub's mergeability is latest-per-name; preflight must be
// too, or green PRs stay blocked forever (gastownhall/beads#5073-5076).
func TestPRPreflightChecksUseLatestRunPerName(t *testing.T) {
	staleGreenRollup := `[` +
		`{"name":"CI Gate / Required","status":"COMPLETED","conclusion":"FAILURE","completedAt":"2026-07-27T10:00:00Z"},` +
		`{"name":"CI Gate / Required","status":"COMPLETED","conclusion":"SUCCESS","completedAt":"2026-07-28T10:00:00Z"},` +
		`{"name":"Test (Proxied 1/2)","status":"COMPLETED","conclusion":"CANCELLED","completedAt":"2026-07-27T09:00:00Z"},` +
		`{"name":"Test (Proxied 1/2)","status":"COMPLETED","conclusion":"SUCCESS","completedAt":"2026-07-28T09:00:00Z"},` +
		`{"name":"Lint","status":"COMPLETED","conclusion":"SUCCESS","completedAt":"2026-07-28T08:00:00Z"}` +
		`]`

	t.Run("stale failures and cancellations are superseded by newer green runs", func(t *testing.T) {
		run := runPRPreflightWithFakeGH(t, preflightFixture{rollup: staleGreenRollup})
		if !strings.Contains(run.output, "No failed or pending status checks reported.") {
			t.Fatalf("expected stale rollup entries to be superseded:\n%s", run.output)
		}
		if strings.Contains(run.output, "failed or require action") {
			t.Fatalf("stale rollup entries still counted as failures:\n%s", run.output)
		}
	})

	t.Run("a genuinely failing latest run still blocks", func(t *testing.T) {
		freshRedRollup := `[` +
			`{"name":"CI Gate / Required","status":"COMPLETED","conclusion":"SUCCESS","completedAt":"2026-07-27T10:00:00Z"},` +
			`{"name":"CI Gate / Required","status":"COMPLETED","conclusion":"FAILURE","completedAt":"2026-07-28T10:00:00Z"}` +
			`]`
		run := runPRPreflightWithFakeGH(t, preflightFixture{rollup: freshRedRollup})
		if !strings.Contains(run.output, "1 status check(s) failed or require action.") {
			t.Fatalf("expected the latest failing run to block:\n%s", run.output)
		}
	})

	t.Run("a commit status cannot supersede a same-named check run", func(t *testing.T) {
		// GitHub treats a check run and a commit status sharing a name as
		// independent required gates; both must pass.
		mixedRollup := `[` +
			`{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"FAILURE","completedAt":"2026-07-28T10:00:00Z"},` +
			`{"__typename":"StatusContext","context":"build","state":"SUCCESS","startedAt":"2026-07-28T11:00:00Z"}` +
			`]`
		run := runPRPreflightWithFakeGH(t, preflightFixture{rollup: mixedRollup})
		if !strings.Contains(run.output, "1 status check(s) failed or require action.") {
			t.Fatalf("expected the failing check run to block despite the same-named commit status:\n%s", run.output)
		}
	})

	t.Run("identically named jobs in different workflows stay independent", func(t *testing.T) {
		crossWorkflowRollup := `[` +
			`{"__typename":"CheckRun","workflowName":"Main","name":"Test","status":"COMPLETED","conclusion":"FAILURE","completedAt":"2026-07-28T10:00:00Z"},` +
			`{"__typename":"CheckRun","workflowName":"Nightly","name":"Test","status":"COMPLETED","conclusion":"SUCCESS","completedAt":"2026-07-28T11:00:00Z"}` +
			`]`
		run := runPRPreflightWithFakeGH(t, preflightFixture{rollup: crossWorkflowRollup})
		if !strings.Contains(run.output, "1 status check(s) failed or require action.") {
			t.Fatalf("expected the other workflow's red job to keep blocking:\n%s", run.output)
		}
	})

	t.Run("an in-progress rerun with a zero completion time supersedes an old failure as pending", func(t *testing.T) {
		// gh exports absent CheckRun times as the Go zero time, not null.
		rerunRollup := `[` +
			`{"__typename":"CheckRun","workflowName":"Main","name":"CI","status":"COMPLETED","conclusion":"FAILURE","startedAt":"2026-07-27T09:00:00Z","completedAt":"2026-07-27T10:00:00Z"},` +
			`{"__typename":"CheckRun","workflowName":"Main","name":"CI","status":"IN_PROGRESS","conclusion":"","startedAt":"2026-07-28T12:00:00Z","completedAt":"0001-01-01T00:00:00Z"}` +
			`]`
		run := runPRPreflightWithFakeGH(t, preflightFixture{rollup: rerunRollup})
		if !strings.Contains(run.output, "1 status check(s) are still pending.") {
			t.Fatalf("expected the fresh rerun to classify as pending, not stale failure:\n%s", run.output)
		}
	})
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
	if fixture.rollup == "" {
		fixture.rollup = "[]"
	}

	bash, path := prPreflightProcessTools(t)

	runDir := t.TempDir()
	callLog := filepath.Join(runDir, "gh-calls")
	if err := os.WriteFile(callLog, nil, 0o600); err != nil {
		t.Fatalf("initialize fake gh call log: %v", err)
	}
	bashEnvironment := filepath.Join(runDir, "bash-env.sh")
	if err := os.WriteFile(bashEnvironment, []byte(fakeGHFunction), 0o600); err != nil {
		t.Fatalf("write controlled Bash environment: %v", err)
	}

	root := sourceRepoRoot(t)
	cmd := exec.Command(
		bash,
		"--noprofile",
		"--norc",
		msysPath(filepath.Join(root, "scripts", "pr-preflight.sh")),
		fixture.prArg, "--repo", "default/repo",
	)
	cmd.Dir = runDir
	cmd.Env = append(prPreflightWindowsRuntimeEnv(),
		"PATH="+path,
		"HOME="+msysPath(runDir),
		"XDG_CONFIG_HOME="+msysPath(filepath.Join(runDir, "xdg-config")),
		"XDG_STATE_HOME="+msysPath(filepath.Join(runDir, "xdg-state")),
		"GH_CONFIG_DIR="+msysPath(filepath.Join(runDir, "gh-config")),
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV="+msysPath(bashEnvironment),
		"ENV=",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GH_CALL_LOG="+msysPath(callLog),
		"GRAPHQL_EXIT="+fixture.graphQLExit,
		"GRAPHQL_RESPONSE="+fixture.graphQLResponse,
		"PR_NUMBER="+fixture.returnedNumber,
		"PR_URL="+fixture.canonicalURL,
		"PR_ROLLUP="+fixture.rollup,
	)
	out, runErr := cmd.CombinedOutput()
	logBytes, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatalf("read fake gh call log: %v", readErr)
	}
	return preflightRun{output: string(out), calls: parseGHCalls(t, logBytes), err: runErr}
}

func prPreflightProcessTools(t *testing.T) (string, string) {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required to exercise pr-preflight.sh: %v", err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required to exercise pr-preflight.sh: %v", err)
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Fatalf("jq is required to exercise pr-preflight.sh: %v", err)
	}

	entries := []string{
		msysPath(filepath.Dir(git)),
		msysPath(filepath.Dir(jq)),
		"/usr/bin",
		"/bin",
	}
	path := strings.Join(uniqueStrings(entries), ":")
	probeScript := `set -eu
test -n "$BASH_VERSION"
command -v git >/dev/null
command -v jq >/dev/null
`
	if runtime.GOOS == "windows" {
		probeScript += `case "$(uname -s)" in
  MINGW*|MSYS*) ;;
  *) printf 'expected Git Bash, found %s\n' "$(uname -s)" >&2; exit 64 ;;
esac
`
	}
	probe := exec.Command(bash, "--noprofile", "--norc", "-c", probeScript)
	probe.Env = append(prPreflightWindowsRuntimeEnv(),
		"PATH="+path,
		"HOME="+msysPath(t.TempDir()),
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("selected Bash cannot provide the required preflight boundary: %v\n%s", err, output)
	}
	return bash, path
}

func prPreflightWindowsRuntimeEnv() []string {
	if runtime.GOOS != "windows" {
		return nil
	}

	var env []string
	for _, key := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
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
