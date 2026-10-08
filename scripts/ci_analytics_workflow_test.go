package scripts_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// ciAnalyticsLaneJobs lists bazel.yml's lane jobs that carry the "CI
// analytics summary"/"Upload CI analytics summary" steps
// (rbe-ci-bep-analytics-design.md, S3), mapped to each job's LANE env
// value. The artifact name and the LANE env are both derived from this one
// map in the checks below, instead of being pinned independently and
// allowed to drift apart.
var ciAnalyticsLaneJobs = map[string]string{
	"bazel-test":           "test",
	"bazel-pure":           "pure",
	"bazel-embedded":       "embedded",
	"bazel-integration":    "integration",
	"bazel-doltserver":     "doltserver",
	"bazel-proxied":        "proxied-server",
	"bazel-server-storage": "server-storage",
	"bazel-cmd-dolt":       "cmd-dolt",
}

// ciAnalyticsExcludedJobs are bazel.yml jobs that deliberately do not run
// the CI analytics extractor, with the reason: mirrors
// TestBazelLaneIsGatedAlongsideLegacy's gated/advisory/flag-gated split, so
// a new job in bazel.yml can't silently go unaccounted for here either.
var ciAnalyticsExcludedJobs = map[string]string{
	"rbe":                 "decides the execution mode; builds nothing itself, so there is no BEP/exec-log/profile to summarize",
	"rbe-prewarm":         "best-effort pre-warm dispatch to gascity's rbe-west pool; not a build/test lane",
	"package-mcp":         "F3 package gate; runs pytest, not a bazel invocation with a BEP to extract",
	"package-npm":         "F3 package gate; runs npm, not a bazel invocation with a BEP to extract",
	"bazel-release-cross": "one bazel-release-cross-compile.sh run with no BEP/exec-log/profile capture, as when it was a bazel-pure step",
}

// ciAnalyticsRunTemplateRE finds a GitHub Actions expression (${{ ... }})
// inside a `run:` block: ci_analytics_extract.py's arguments must come from
// $LANE/$MODE/$CHECK_RUN_ID/$PR_HINT shell variables (set in `env:`, which
// GitHub Actions substitutes before the shell ever sees them), never a raw
// ${{ }} expression spliced into the command line itself.
var ciAnalyticsRunTemplateRE = regexp.MustCompile(`\$\{\{`)

// ciAnalyticsRunnerTempPathRE captures the basename (the part after
// $RUNNER_TEMP/) of any $RUNNER_TEMP-relative path a run: block mentions,
// including one built from a shell loop variable (e.g. bazel-bep-$step.json).
var ciAnalyticsRunnerTempPathRE = regexp.MustCompile(`\$RUNNER_TEMP/([\w.$-]+)`)

// ciAnalyticsWriterFlagRE captures the $RUNNER_TEMP-relative basename
// written by one of the three bazel flags that produce a BEP file, a
// compact exec log or a JSON profile: the only three file kinds the
// summary step is allowed to read.
var ciAnalyticsWriterFlagRE = regexp.MustCompile(`--(?:build_event_json_file|execution_log_compact_file|profile)="?\$RUNNER_TEMP/([\w.$-]+)"?`)

// ciAnalyticsForLoopRE matches a `for <var> in <values>; do` line, so a
// basename built from the loop variable (bazel-bep-$step.json) can be
// expanded to every concrete value the loop actually iterates.
var ciAnalyticsForLoopRE = regexp.MustCompile(`for (\w+) in ([^;]+); do`)

// ciAnalyticsFileBasenameRE keeps only basenames that name a real
// input file (.json or .log.zst): the summary step's own --out path
// ($RUNNER_TEMP/ci-analytics/ci-analytics.json) is not $RUNNER_TEMP-direct
// and never matches a writer flag, so it is dropped rather than
// misclassified as a bad reader reference.
var ciAnalyticsFileBasenameRE = regexp.MustCompile(`\.(json|log\.zst)$`)

const (
	// The path/retention/cleanup contract of every "Upload CI analytics
	// summary" step: the redacted summary ci_analytics_extract.py wrote,
	// kept a week, replaced (not 409'd) by a job re-run, and silently
	// skipped (not failed) if the summary step itself never ran.
	ciAnalyticsUploadPath           = "${{ runner.temp }}/ci-analytics/ci-analytics.json"
	ciAnalyticsUploadRetentionDays  = "7"
	ciAnalyticsUploadIfNoFilesFound = "ignore"
	ciAnalyticsUploadOverwrite      = "true"
	ciAnalyticsUploadTimeoutMinutes = 2
	ciAnalyticsSummaryMaxTimeout    = 3
	// The real job context (requires actionlint >= 1.7.8), not a
	// github.run_id substitution: the design's contract with S4.
	ciAnalyticsCheckRunID = "${{ job.check_run_id }}"
	ciAnalyticsMode       = "${{ needs.rbe.outputs.mode }}"
	ciAnalyticsPRHint     = "${{ github.event.pull_request.number || 0 }}"
)

func ciAnalyticsContinueOnError(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}

// ciAnalyticsExpandBasenames expands every $<loopvar> token in basenames
// using any `for <loopvar> in ...; do` loop found in run, returning the
// set of concrete basenames actually produced or consumed. A basename with
// no loop-variable token (the 7 lanes with a single literal BEP/exec-log/
// profile set) expands to itself unchanged.
func ciAnalyticsExpandBasenames(run string, basenames []string) map[string]bool {
	out := map[string]bool{}
	loops := ciAnalyticsForLoopRE.FindAllStringSubmatch(run, -1)
	for _, b := range basenames {
		expanded := []string{b}
		for _, loop := range loops {
			token := "$" + loop[1]
			if !strings.Contains(b, token) {
				continue
			}
			var next []string
			for _, e := range expanded {
				for _, v := range strings.Fields(loop[2]) {
					next = append(next, strings.ReplaceAll(e, token, v))
				}
			}
			expanded = next
		}
		for _, e := range expanded {
			out[e] = true
		}
	}
	return out
}

// TestCIAnalyticsStepsAreSafeAndBounded is beads' version of gascity's pin
// test of the same name (scripts/bazel_multilane_test.go, ci/analytics-
// extractor @ 00161373bc7bbfcef8d6a05ac5ce70409fc7f2ac), adapted to beads'
// 8 separately-named lane jobs rather than gascity's single matrix "lane"
// job plus "coverage". The rbe-ci-bep-analytics-design.md "CI analytics
// summary" step (ci_analytics_extract.py) and its upload, in every lane
// job, must stay strictly reporting-only: continue-on-error, a short
// timeout so a parser bug can never make the step (or the job) run long,
// no raw GitHub Actions expression spliced into the shell command (only
// validated $ENV_VARS, never a string this extractor would have to
// re-validate itself), !cancelled() required rather than always() (always()
// also runs after a workflow cancellation, which these reporting-only
// steps should not survive), an exact upload name/path/retention contract,
// and every path the summary step reads must actually be written by a
// bazel flag earlier in the same job.
func TestCIAnalyticsStepsAreSafeAndBounded(t *testing.T) {
	workflow := readCIWorkflow(t, "bazel.yml")

	// Every job in bazel.yml is either one of the lane jobs or has a
	// recorded reason it is excluded: a new job cannot join bazel.yml
	// without a decision about CI analytics.
	for name := range workflow.Jobs {
		_, lane := ciAnalyticsLaneJobs[name]
		_, excluded := ciAnalyticsExcludedJobs[name]
		if lane == excluded {
			t.Errorf("bazel.yml job %q: lane=%v excluded=%v; add it to exactly one of ciAnalyticsLaneJobs or ciAnalyticsExcludedJobs (with a reason)", name, lane, excluded)
		}
	}
	for name := range ciAnalyticsLaneJobs {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("ciAnalyticsLaneJobs lists %s, which bazel.yml does not have", name)
		}
	}
	for name := range ciAnalyticsExcludedJobs {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("ciAnalyticsExcludedJobs lists %s, which bazel.yml does not have", name)
		}
	}

	// --execution_log_compact_file without
	// --experimental_build_event_upload_strategy=local in the same command
	// writes a compact exec log bazel never uploads anywhere the extractor
	// (or anything else) can read it back: require both together, in every
	// job's every step, not just the 8 lane jobs.
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "--execution_log_compact_file") && !strings.Contains(step.Run, "--experimental_build_event_upload_strategy=local") {
				t.Errorf("%s job step %q: run has --execution_log_compact_file without --experimental_build_event_upload_strategy=local in the same command", name, step.Name)
			}
		}
	}

	for jobName, wantLane := range ciAnalyticsLaneJobs {
		job := workflow.job(t, jobName)

		summaryIdx := slices.IndexFunc(job.Steps, func(s ciWorkflowStep) bool { return s.Name == "CI analytics summary" })
		uploadIdx := slices.IndexFunc(job.Steps, func(s ciWorkflowStep) bool { return s.Name == "Upload CI analytics summary" })
		if summaryIdx < 0 || uploadIdx < 0 {
			t.Fatalf("%s job: missing CI analytics summary/upload steps", jobName)
		}
		if uploadIdx != summaryIdx+1 {
			t.Errorf("%s job: Upload CI analytics summary is step %d, want %d (immediately after CI analytics summary)", jobName, uploadIdx, summaryIdx+1)
		}
		summary := job.Steps[summaryIdx]
		upload := job.Steps[uploadIdx]

		if !ciAnalyticsContinueOnError(summary.ContinueOnError) {
			t.Errorf("%s job %q: continue-on-error %v, want true", jobName, summary.Name, summary.ContinueOnError)
		}
		if summary.TimeoutMinutes <= 0 || summary.TimeoutMinutes > ciAnalyticsSummaryMaxTimeout {
			t.Errorf("%s job %q: timeout-minutes %d, want 1-%d", jobName, summary.Name, summary.TimeoutMinutes, ciAnalyticsSummaryMaxTimeout)
		}
		if ciAnalyticsRunTemplateRE.MatchString(summary.Run) {
			t.Errorf("%s job %q: run: contains a ${{ }} expression; use env: and a shell variable instead:\n%s", jobName, summary.Name, summary.Run)
		}
		//nolint:misspell // GitHub Actions spells it cancelled()
		if !strings.Contains(summary.If, "!cancelled()") {
			t.Errorf("%s job %q: if %q does not require !cancelled(); always() also runs after a workflow cancellation", jobName, summary.Name, summary.If)
		}
		if got := summary.Env["LANE"]; got != wantLane {
			t.Errorf("%s job CI analytics summary: LANE %q, want %q", jobName, got, wantLane)
		}
		if got := summary.Env["MODE"]; got != ciAnalyticsMode {
			t.Errorf("%s job CI analytics summary: MODE %q, want %q", jobName, got, ciAnalyticsMode)
		}
		if got := summary.Env["CHECK_RUN_ID"]; got != ciAnalyticsCheckRunID {
			t.Errorf("%s job CI analytics summary: CHECK_RUN_ID %q, want %q", jobName, got, ciAnalyticsCheckRunID)
		}
		if got := summary.Env["PR_HINT"]; got != ciAnalyticsPRHint {
			t.Errorf("%s job CI analytics summary: PR_HINT %q, want %q", jobName, got, ciAnalyticsPRHint)
		}

		if !ciAnalyticsContinueOnError(upload.ContinueOnError) {
			t.Errorf("%s job %q: continue-on-error %v, want true", jobName, upload.Name, upload.ContinueOnError)
		}
		if upload.TimeoutMinutes != ciAnalyticsUploadTimeoutMinutes {
			t.Errorf("%s job %q: timeout-minutes %d, want %d", jobName, upload.Name, upload.TimeoutMinutes, ciAnalyticsUploadTimeoutMinutes)
		}
		//nolint:misspell // GitHub Actions spells it cancelled()
		if !strings.Contains(upload.If, "!cancelled()") {
			t.Errorf("%s job %q: if %q does not require !cancelled()", jobName, upload.Name, upload.If)
		}
		wantUploadName := "ci-analytics-" + wantLane + "-${{ github.run_attempt }}"
		if upload.With["name"] != wantUploadName {
			t.Errorf("%s job %q: upload name %q, want %q", jobName, upload.Name, upload.With["name"], wantUploadName)
		}
		if upload.With["path"] != ciAnalyticsUploadPath {
			t.Errorf("%s job %q: upload path %q, want %q (the redacted summary, never a raw BEP/exec-log/profile)", jobName, upload.Name, upload.With["path"], ciAnalyticsUploadPath)
		}
		if upload.With["retention-days"] != ciAnalyticsUploadRetentionDays {
			t.Errorf("%s job %q: retention-days %q, want %q", jobName, upload.Name, upload.With["retention-days"], ciAnalyticsUploadRetentionDays)
		}
		if upload.With["if-no-files-found"] != ciAnalyticsUploadIfNoFilesFound {
			t.Errorf("%s job %q: if-no-files-found %q, want %q", jobName, upload.Name, upload.With["if-no-files-found"], ciAnalyticsUploadIfNoFilesFound)
		}
		if upload.With["overwrite"] != ciAnalyticsUploadOverwrite {
			t.Errorf("%s job %q: overwrite %q, want %q", jobName, upload.Name, upload.With["overwrite"], ciAnalyticsUploadOverwrite)
		}

		// Every $RUNNER_TEMP file the summary step reads must be written by
		// a --build_event_json_file/--execution_log_compact_file/--profile
		// flag somewhere earlier in the same job (expanding any shell loop
		// variable on both sides, e.g. bazel-pure's per-step bazel-bep-
		// $step.json).
		readerRaw := ciAnalyticsRunnerTempPathRE.FindAllStringSubmatch(summary.Run, -1)
		readerBasenames := make([]string, 0, len(readerRaw))
		for _, m := range readerRaw {
			readerBasenames = append(readerBasenames, m[1])
		}
		readerSet := ciAnalyticsExpandBasenames(summary.Run, readerBasenames)

		writerSet := map[string]bool{}
		for i, step := range job.Steps {
			if i == summaryIdx || i == uploadIdx {
				continue
			}
			raw := ciAnalyticsWriterFlagRE.FindAllStringSubmatch(step.Run, -1)
			basenames := make([]string, 0, len(raw))
			for _, m := range raw {
				basenames = append(basenames, m[1])
			}
			for b := range ciAnalyticsExpandBasenames(step.Run, basenames) {
				writerSet[b] = true
			}
		}

		for b := range readerSet {
			if !ciAnalyticsFileBasenameRE.MatchString(b) {
				continue // not a file reference (e.g. the --out path's directory component)
			}
			if !writerSet[b] {
				t.Errorf("%s job CI analytics summary: reads %q, which no --build_event_json_file/--execution_log_compact_file/--profile flag in this job writes", jobName, b)
			}
		}
	}
}
