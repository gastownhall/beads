package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F7b review response: two cache-safety
// blockers (B1, B2) and one cache-quota bound (S7) found after F7b moved
// same-repo PR Linux/Windows/macOS legs onto Blacksmith and added
// push-to-main Blacksmith cache seeders in main.yml. These tests pin the
// fixes so a future edit cannot silently reopen any of them.

// defaultCachingActionFamilies are third-party Actions whose own action.yml
// enables a `cache` (or cache-equivalent) input by default, so moving a job
// that uses one onto Blacksmith makes it implicitly SAVE a GitHub Actions
// cache entry the moment it runs for a same-repo PR or merge_group -- a
// same-repo PR on a Blacksmith-reachable runs-on is attacker-controlled (the
// PR branch, not just its base), so an implicit cache save there is a
// poisoning vector no read-only PR check should have. actions/setup-go is
// already covered everywhere by assertPinnedGoCacheActions's blanket "cache
// must be false" sweep above; msys2/setup-msys2 is the concrete case B1
// found (confirmed via a real GitHub cache list entry saved from a
// refs/pull/.../merge ref). Listed here, not just fixed ad hoc, so the next
// Blacksmith-reachable job that pulls in a new default-caching action trips
// this sweep instead of silently repeating B1.
var defaultCachingActionFamilies = map[string]bool{
	setupGoActionFamily: true,
	"msys2/setup-msys2": true,
}

// TestBlacksmithReachablePRJobsDisableDefaultCachingActions is the B1 fix's
// pin, generalized per the review's instruction to cover default-caching
// setup-* actions generally, not just msys2/setup-msys2: every step in a
// pr.yml/pr-risk.yml job whose runs-on can select a Blacksmith label for a
// same-repo PR or merge_group, that uses one of defaultCachingActionFamilies,
// must disable that implicit caching outright ("false") or fail closed with
// the exact `runner.environment == 'github-hosted'` expression (the F7c
// review fix N1 convention: only the literal "github-hosted" string keeps
// caching on, so an empty/unknown runner.environment value -- including any
// future Blacksmith image that reports something other than "self-hosted" --
// keeps caching off).
func TestBlacksmithReachablePRJobsDisableDefaultCachingActions(t *testing.T) {
	const wantFailClosed = "${{ runner.environment == 'github-hosted' }}"
	for _, file := range []string{"pr.yml", "pr-risk.yml"} {
		workflow := readCIWorkflow(t, file)
		for jobName, job := range workflow.Jobs {
			if !strings.Contains(job.RunsOn, "blacksmith-") {
				continue
			}
			for _, step := range job.Steps {
				family := actionFamily(step.Uses)
				if !defaultCachingActionFamilies[family] {
					continue
				}
				cache := step.With["cache"]
				if cache != "false" && cache != wantFailClosed {
					t.Errorf("%s job %s step %q uses %s (caches by default) on a Blacksmith-reachable runner with cache=%q; want \"false\" or %q",
						file, jobName, step.Name, family, cache, wantFailClosed)
				}
			}
		}
	}
}

// blacksmithSaverJobs are main.yml's push-to-main-only jobs that seed a
// Blacksmith-selection GOCACHE a same-repo PR or merge_group job restores
// from: F7b's Windows saver (B2), the macOS saver, and the Blacksmith macOS
// `test` job (which saves its own race cache).
var blacksmithSaverJobs = []string{
	"test-windows", "blacksmith-macos-go-build-cache", "test",
}

// TestBlacksmithSaverJobsGuardedAgainstPullRequest is the B2 fix's pin: each
// of blacksmithSaverJobs must carry the exact push-to-main-only, same-repo
// guard F7c's own blacksmith-setup-go-cache seeder uses, so a same-repo PR or
// merge_group run (an attacker-controlled PR branch on a Blacksmith-reachable
// runs-on) can never execute -- let alone save a cache entry from -- one of
// these jobs. Before this fix none of the four carried a job-level `if:` at
// all beyond (for test-windows) an event/ref check that omitted the
// repository check, so a same-repo fork of beads with push access to its own
// main would not have been excluded.
func TestBlacksmithSaverJobsGuardedAgainstPullRequest(t *testing.T) {
	const wantIf = "github.event_name == 'push' && github.ref == 'refs/heads/main' && github.repository == 'gastownhall/beads'"
	workflow := readCIWorkflow(t, "main.yml")
	for _, jobName := range blacksmithSaverJobs {
		job := workflow.job(t, jobName)
		if job.If != wantIf {
			t.Errorf("main.yml's %s if=%q, want exactly %q", jobName, job.If, wantIf)
		}
	}

	const ownRepo = "gastownhall/beads"
	cases := []struct {
		name string
		ctx  map[string]string
		want bool
	}{
		{"actual push to main", map[string]string{
			"github.event_name": "push", "github.ref": "refs/heads/main", "github.repository": ownRepo,
		}, true},
		{"same-repo pull_request stays excluded", map[string]string{
			"github.event_name": "pull_request", "github.ref": "refs/pull/1/merge", "github.repository": ownRepo,
			"github.event.pull_request.head.repo.full_name": ownRepo,
		}, false},
		{"merge_group stays excluded", map[string]string{
			"github.event_name": "merge_group", "github.ref": "refs/heads/gh-readonly-queue/main/pr-1", "github.repository": ownRepo,
		}, false},
		{"push to a non-main branch stays excluded", map[string]string{
			"github.event_name": "push", "github.ref": "refs/heads/not-main", "github.repository": ownRepo,
		}, false},
		{"push from a fork stays excluded", map[string]string{
			"github.event_name": "push", "github.ref": "refs/heads/main", "github.repository": "someone-else/beads",
		}, false},
	}
	for _, jobName := range blacksmithSaverJobs {
		job := workflow.job(t, jobName)
		for _, c := range cases {
			t.Run(jobName+"/"+c.name, func(t *testing.T) {
				got, err := evalGHExpr(job.If, c.ctx)
				if err != nil {
					t.Fatalf("evalGHExpr(%q): %v", job.If, err)
				}
				if ghTruthy(got) != c.want {
					t.Errorf("evalGHExpr(%q) under %+v = %#v, want truthy=%v", job.If, c.ctx, got, c.want)
				}
			})
		}
	}
}

// TestBlacksmithSaverCacheKeysAreNotPerCommit is the S7 fix's pin: each of
// main.yml's Blacksmith savers' Go-cache
// restore/save key pairs is bounded by go.sum content plus a UTC calendar day
// (via that job's own "Compute cache date" step, id: cache-date), not by
// github.sha, so an ordinary day of push traffic to main cannot mint a new
// multi-GB cache entry per push and exhaust Blacksmith's 25 GB/week/repo
// quota (measured ~1.74-2.4GB/push under the old per-sha scheme). Mirrors
// F7c review fix S5's TestBlacksmithSeederCacheKeyIsNotPerCommit for
// blacksmith-setup-go-cache.
func TestBlacksmithSaverCacheKeysAreNotPerCommit(t *testing.T) {
	workflow := readCIWorkflow(t, "main.yml")

	type saverSteps struct {
		job              string
		dateStepJob      string // job the "Compute cache date" step lives in, usually == job
		restoreStepNames []string
		saveStepNames    []string
	}
	cases := []saverSteps{
		{
			job:              "test-windows",
			restoreStepNames: []string{"Restore non-race Go build cache"},
			saveStepNames:    []string{"Save non-race Go build cache"},
		},
		{
			job:              "blacksmith-macos-go-build-cache",
			restoreStepNames: []string{"Restore non-race Go build cache"},
			saveStepNames:    []string{"Save non-race Go build cache"},
		},
		{
			job:              "test",
			restoreStepNames: []string{"Restore non-race Go build cache", "Restore race Go build cache"},
			saveStepNames:    []string{"Save race Go build cache"},
		},
	}

	for _, c := range cases {
		job := workflow.job(t, c.job)
		dateStep := job.step(t, "Compute cache date")
		if dateStep.ID != "cache-date" {
			t.Errorf("main.yml's %s Compute cache date step has id %q, want \"cache-date\"", c.job, dateStep.ID)
		}
		if !strings.Contains(dateStep.Run, "date -u") {
			t.Errorf("main.yml's %s Compute cache date step run = %q, want it to compute a UTC date", c.job, dateStep.Run)
		}

		for _, name := range append(append([]string{}, c.restoreStepNames...), c.saveStepNames...) {
			step := job.step(t, name)
			key := step.With["key"]
			if strings.Contains(key, "github.sha") {
				t.Errorf("main.yml's %s %s step key = %q, must not key per-commit (github.sha) - see F7b review fix S7", c.job, name, key)
			}
			if !strings.Contains(key, "steps.cache-date.outputs.today") {
				t.Errorf("main.yml's %s %s step key = %q, want it keyed by steps.cache-date.outputs.today", c.job, name, key)
			}
			if !strings.Contains(key, "hashFiles('go.sum')") {
				t.Errorf("main.yml's %s %s step key = %q, want it keyed by hashFiles('go.sum')", c.job, name, key)
			}
		}
	}
}

// TestBlacksmithSaverVenueAndFlavorMatricesAreComplete re-pins mutations the
// reviewer's mutate.py found surviving against pre-fix code (M8, M9). The
// Windows and macOS savers (test-windows, blacksmith-macos-go-build-cache)
// run one Blacksmith leg only: no GitHub-hosted Windows or macOS PR leg is
// left to seed (ga-96smfk.22).
func TestBlacksmithSaverVenueAndFlavorMatricesAreComplete(t *testing.T) {
	workflow := readCIWorkflow(t, "main.yml")
	for _, jobName := range []string{"test-windows", "blacksmith-macos-go-build-cache"} {
		if m := workflow.job(t, jobName).Strategy.Matrix; len(m.Venue) != 0 || len(m.Include) != 0 {
			t.Errorf("main.yml's %s has a matrix %+v; it seeds the one Blacksmith label its PR legs run on", jobName, m)
		}
	}
}

// TestWarmNonRaceCacheScriptCompilesGitattributespolicy re-pins mutation M15:
// scripts/ci/warm-non-race-cache.sh must still compile
// ./scripts/gitattributespolicy's integration test binary -- it is one of
// check-doc-freshness-platforms' ubuntu-leg steps (TestRequiredHost etc.),
// and dropping it from the shared warm script would silently stop warming
// that leg's GOCACHE entry, cold-compiling it on every PR again.
func TestWarmNonRaceCacheScriptCompilesGitattributespolicy(t *testing.T) {
	root := sourceRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "warm-non-race-cache.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "go test '-tags=integration,gms_pure_go' -c -o /dev/null ./scripts/gitattributespolicy"
	if !strings.Contains(string(data), want) {
		t.Errorf("scripts/ci/warm-non-race-cache.sh is missing %q", want)
	}
}

// TestWindowsSaverAndLivenessTimeoutsAreTwentyMinutes re-pins mutation M13
// (updated for F7b review fix S6/S8's 15->20 bump, which moved the exact
// anchor text mutate.py's M13 used): both main.yml's test-windows (S8) and
// pr.yml's test-windows-liveness (S6) must keep timeout-minutes: 20 -- their
// shared rationale is a measured ~8m worst case on this exact job plus
// consistent headroom across both; silently shrinking either's budget risks
// spurious timeout failures with no test catching the regression.
func TestWindowsSaverAndLivenessTimeoutsAreTwentyMinutes(t *testing.T) {
	if job := readCIWorkflow(t, "main.yml").job(t, "test-windows"); job.TimeoutMinutes != 20 {
		t.Errorf("main.yml test-windows timeout-minutes = %d, want 20", job.TimeoutMinutes)
	}
	if job := readCIWorkflow(t, "pr.yml").job(t, "test-windows-liveness"); job.TimeoutMinutes != 20 {
		t.Errorf("pr.yml test-windows-liveness timeout-minutes = %d, want 20", job.TimeoutMinutes)
	}
}

// blacksmithMacOSPRLegJobs are pr.yml's mixed-OS matrix jobs whose macOS leg
// runs on Blacksmith macOS for same-repo PRs/merge_group.
var blacksmithMacOSPRLegJobs = []string{"pr-preflight-platforms", "check-doc-freshness-platforms"}

// TestBlacksmithMacOSSaverMatchesPRLegs pins the contract between pr.yml's
// macOS PR legs and their only seeder, main.yml's
// blacksmith-macos-go-build-cache: Blacksmith cannot see GitHub-saved caches
// (and vice versa), so the saver must run on exactly the label every PR's
// macOS leg runs on (forks included), write the cache path/key family those
// legs restore, and compile what they compile (the shared warm-up). The PR
// legs stay restore-only and are inside the default-caching-action sweep.
func TestBlacksmithMacOSSaverMatchesPRLegs(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	mainWF := readCIWorkflow(t, "main.yml")
	saver := mainWF.job(t, "blacksmith-macos-go-build-cache")

	if want := "${{ '" + blacksmithMacOSLabel + "' }}"; saver.RunsOn != want {
		t.Errorf("blacksmith-macos-go-build-cache runs-on = %q, want %q", saver.RunsOn, want)
	}
	if saver.TimeoutMinutes == 0 {
		t.Error("blacksmith-macos-go-build-cache has no timeout-minutes")
	}

	saverSave := saver.step(t, "Save non-race Go build cache")
	for _, jobName := range blacksmithMacOSPRLegJobs {
		job := pr.job(t, jobName)
		// Every PR's macOS leg, forks included, runs on the saver's label.
		macOSLegs := 0
		for _, leg := range job.Strategy.Matrix.Include {
			if leg.OS != macOSRunner {
				continue
			}
			macOSLegs++
			if got := mustEvalGHRunsOn(t, job.RunsOn, map[string]string{"matrix.runner": leg.Runner, "matrix.os": leg.OS}); got != blacksmithMacOSLabel {
				t.Errorf("pr.yml %s macOS leg resolves to %q, want the saver's %q", jobName, got, blacksmithMacOSLabel)
			}
		}
		if macOSLegs != 1 {
			t.Errorf("pr.yml %s has %d macOS legs, want 1", jobName, macOSLegs)
		}
		if setup := job.step(t, "Set up Go"); setup.With["cache"] != "false" {
			t.Errorf("pr.yml %s Set up Go cache = %q, want \"false\"", jobName, setup.With["cache"])
		}
		for _, step := range job.Steps {
			if fam := actionFamily(step.Uses); fam == cacheSaveActionFamily || fam == cacheMonolithicActionFamily {
				t.Errorf("pr.yml %s step %q uses %s; PR macOS legs are restore-only", jobName, step.Name, fam)
			}
		}
		restore := job.step(t, "Restore non-race Go build cache")
		if restore.With["path"] != saverSave.With["path"] {
			t.Errorf("pr.yml %s restores %q, saver writes %q", jobName, restore.With["path"], saverSave.With["path"])
		}
		if prefix := restore.With["restore-keys"]; prefix == "" || !strings.HasPrefix(saverSave.With["key"], prefix) {
			t.Errorf("saver key %q is not reachable from pr.yml %s restore-keys %q", saverSave.With["key"], jobName, prefix)
		}
		modRestore := job.step(t, "Restore Go module cache")
		if modSave := saver.step(t, "Save Go module cache"); !strings.HasPrefix(modSave.With["key"], modRestore.With["restore-keys"]) || modSave.With["path"] != modRestore.With["path"] {
			t.Errorf("saver module cache %q/%q not reachable from pr.yml %s restore %q/%q",
				modSave.With["path"], modSave.With["key"], jobName, modRestore.With["path"], modRestore.With["restore-keys"])
		}
	}

	const warm = "Warm non-race GOCACHE for macOS preflight/doc-freshness"
	assertStepRunsExactly(t, saver, warm, "bash scripts/ci/warm-non-race-cache.sh")
	assertGoCacheEnv(t, saver, warm, "non-race")
	assertGoCacheEnv(t, saver, "Build", "non-race")
	assertStepsBefore(t, saver, []string{"Restore Go module cache", "Restore non-race Go build cache"}, []string{"Build", warm})
	assertStepsBefore(t, saver, []string{"Build", warm}, []string{"Save Go module cache", "Save non-race Go build cache"})
	if step := saver.step(t, warm); step.If != "" || (step.ContinueOnError != nil && step.ContinueOnError != false) {
		t.Errorf("%q if=%q continue-on-error=%v, want unconditional", warm, step.If, step.ContinueOnError)
	}
}
