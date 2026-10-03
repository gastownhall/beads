package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// windowsTestBinaryManifestEntry mirrors one non-comment, non-blank line of
// scripts/ci/windows-test-binaries.txt: "name cgo tags package".
type windowsTestBinaryManifestEntry struct {
	name string
	cgo  string
	tags string
	pkg  string
}

func readWindowsTestBinariesManifest(t *testing.T) []windowsTestBinaryManifestEntry {
	t.Helper()

	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "windows-test-binaries.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var entries []windowsTestBinaryManifestEntry
	for _, line := range strings.Split(string(data), "\n") {
		if idx := strings.Index(line, "#"); idx != -1 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			t.Fatalf("malformed manifest line %q: want 4 whitespace-separated fields (name cgo tags package), got %d", line, len(fields))
		}
		entries = append(entries, windowsTestBinaryManifestEntry{name: fields[0], cgo: fields[1], tags: fields[2], pkg: fields[3]})
	}
	return entries
}

// TestWindowsTestBinariesManifestPinned pins the advisory-phase manifest to
// exactly the three binaries its two "-prebuilt" consumer jobs need (see the
// manifest's own header comment for why the rollout is scoped this way).
// Growing the manifest without growing its consumers, or vice versa, is
// exactly the drift TestWindowsTestBinariesConsumersMatchManifest below
// exists to catch; this test instead catches an unreviewed change to the
// three pinned entries themselves (toolchain flags, package, or tags
// silently drifting out from under the native jobs they mirror).
func TestWindowsTestBinariesManifestPinned(t *testing.T) {
	entries := readWindowsTestBinariesManifest(t)
	want := []windowsTestBinaryManifestEntry{
		{name: "bd.exe", cgo: "1", tags: "gms_pure_go", pkg: "./cmd/bd"},
		{name: "cmd-bd-cgo.test.exe", cgo: "1", tags: "gms_pure_go", pkg: "./cmd/bd"},
		{name: "cmd-bd-nocgo.test.exe", cgo: "0", tags: "gms_pure_go", pkg: "./cmd/bd"},
	}
	if len(entries) != len(want) {
		t.Fatalf("manifest has %d entries, want %d: got %+v", len(entries), len(want), entries)
	}
	for i, got := range entries {
		if got != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// TestWindowsCrossCompileToolchainPinned pins build-windows-test-binaries.sh
// to the same mingw-w64 cross-compiler release.yml's goreleaser job uses for
// the shipped bd-windows-amd64 binary (.goreleaser.yml), so the two toolchains
// cannot silently diverge.
func TestWindowsCrossCompileToolchainPinned(t *testing.T) {
	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "build-windows-test-binaries.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		`env_args=(GOOS=windows GOARCH=amd64 CGO_ENABLED="$cgo")`,
		`env_args+=(CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++)`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("build-windows-test-binaries.sh does not contain %q", want)
		}
	}

	goreleaser := readGoreleaserBuilds(t)
	var windowsBuild *goreleaserBuild
	for i := range goreleaser {
		if goreleaser[i].ID == "bd-windows-amd64" {
			windowsBuild = &goreleaser[i]
			break
		}
	}
	if windowsBuild == nil {
		t.Fatal(".goreleaser.yml has no bd-windows-amd64 build to compare against")
	}
}

// winBinRefPattern matches `.../win-bin/<name>` references in step `run`
// bodies, capturing the file name so it can be checked against the manifest.
var winBinRefPattern = regexp.MustCompile(`win-bin/([A-Za-z0-9_.-]+)`)

// TestWindowsTestBinariesConsumersMatchManifest confirms every binary name
// the "-prebuilt" jobs reference actually exists in the manifest that builds
// it - a renamed manifest entry (or a typo'd reference) fails a Windows job
// at runtime with a missing-file error; this catches it at review time
// instead.
func TestWindowsTestBinariesConsumersMatchManifest(t *testing.T) {
	entries := readWindowsTestBinariesManifest(t)
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.name] = true
	}

	pr := readCIWorkflow(t, "pr.yml")
	referenced := make(map[string]bool)
	for _, jobName := range []string{"test-windows-liveness-prebuilt", "worktree-remove-windows-prebuilt"} {
		job := pr.job(t, jobName)
		for _, step := range job.Steps {
			for _, env := range step.Env {
				for _, m := range winBinRefPattern.FindAllStringSubmatch(env, -1) {
					referenced[m[1]] = true
					if !names[m[1]] {
						t.Errorf("%s step %q env references win-bin/%s, not in manifest %+v", jobName, step.Name, m[1], entries)
					}
				}
			}
			for _, m := range winBinRefPattern.FindAllStringSubmatch(step.Run, -1) {
				referenced[m[1]] = true
				if !names[m[1]] {
					t.Errorf("%s step %q run references win-bin/%s, not in manifest %+v", jobName, step.Name, m[1], entries)
				}
			}
		}
	}
	// Every manifest entry this advisory phase builds should be consumed by
	// at least one of the two twins - an unused entry is dead weight in the
	// cross-compile job for no benefit.
	for name := range names {
		if !referenced[name] {
			t.Errorf("manifest entry %q is never referenced by test-windows-liveness-prebuilt or worktree-remove-windows-prebuilt", name)
		}
	}
}

// TestWindowsTestBinariesArtifactNameConsistent pins the upload/download
// artifact name shared by windows-test-binaries and its two consumers, and
// that both consumers declare the producer as a `needs` dependency (without
// it, the download step would race the upload and fail intermittently
// instead of deterministically).
func TestWindowsTestBinariesArtifactNameConsistent(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	producer := pr.job(t, "windows-test-binaries")
	upload := producer.step(t, "Upload Windows test binaries")
	uploadName := upload.With["name"]
	if uploadName != "windows-test-binaries" {
		t.Fatalf("producer upload name = %q, want %q", uploadName, "windows-test-binaries")
	}

	for _, jobName := range []string{"test-windows-liveness-prebuilt", "worktree-remove-windows-prebuilt"} {
		job := pr.job(t, jobName)
		download := job.step(t, "Download Windows test binaries")
		if got := download.With["name"]; got != uploadName {
			t.Errorf("%s download name = %q, want %q (matching the producer's upload name)", jobName, got, uploadName)
		}
		if len(job.Needs) != 1 || job.Needs[0] != "windows-test-binaries" {
			t.Errorf("%s needs = %v, want exactly [windows-test-binaries]", jobName, job.Needs)
		}
	}
}

// runFlagLiteral extracts the `-run '...'` argument from a step's run body.
func runFlagLiteral(t *testing.T, step ciWorkflowStep) string {
	t.Helper()
	m := regexp.MustCompile(`-run '([^']+)'`).FindStringSubmatch(step.Run)
	if m == nil {
		t.Fatalf("step %q run body has no -run '...' literal:\n%s", step.Name, step.Run)
	}
	return m[1]
}

// TestWindowsTestBinariesSelectSameTestsAsNativeJobs is the F4.4 side-by-side
// policy test: the cross-built "-prebuilt" jobs must select exactly the same
// tests, with the same CGO_ENABLED selection, as the native windows-latest
// jobs they are advisory twins of. A -run literal or CGO_ENABLED value that
// drifts between the two paths would mean the cross-build is silently
// validating a different test subset than the required native lane -
// defeating the entire point of running them side by side before any flip.
func TestWindowsTestBinariesSelectSameTestsAsNativeJobs(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")

	native := pr.job(t, "test-windows-liveness")
	prebuilt := pr.job(t, "test-windows-liveness-prebuilt")

	nativeGlobalPrime := native.step(t, "Run native Windows global Prime override")
	prebuiltGlobalPrime := prebuilt.step(t, "Run native Windows global Prime override (prebuilt)")
	if n, p := runFlagLiteral(t, nativeGlobalPrime), runFlagLiteral(t, prebuiltGlobalPrime); n != p {
		t.Errorf("global-prime -run literal mismatch: native %q prebuilt %q", n, p)
	}
	if got := nativeGlobalPrime.Env["CGO_ENABLED"]; got != "1" {
		t.Fatalf("test-windows-liveness CGO_ENABLED = %q, want \"1\" (manifest entries bd.exe/cmd-bd-cgo.test.exe assume this)", got)
	}

	nativeLiveness := native.step(t, "Run Windows liveness regression test")
	prebuiltLiveness := prebuilt.step(t, "Run Windows liveness regression test (prebuilt)")
	if n, p := runFlagLiteral(t, nativeLiveness), runFlagLiteral(t, prebuiltLiveness); n != p {
		t.Errorf("liveness regression -run literal mismatch: native %q prebuilt %q", n, p)
	}

	nativeWorktree := pr.job(t, "worktree-remove-windows").step(t, "Run native Windows worktree removal boundary tests")
	prebuiltWorktree := pr.job(t, "worktree-remove-windows-prebuilt").step(t, "Run native Windows worktree removal boundary tests (prebuilt)")
	if n, p := runFlagLiteral(t, nativeWorktree), runFlagLiteral(t, prebuiltWorktree); n != p {
		t.Errorf("worktree-remove -run literal mismatch: native %q prebuilt %q", n, p)
	}
	if got := nativeWorktree.Env["CGO_ENABLED"]; got != "0" {
		t.Fatalf("worktree-remove-windows CGO_ENABLED = %q, want \"0\" (manifest entry cmd-bd-nocgo.test.exe assumes this)", got)
	}
}

// TestWindowsCrossCompileJobsNeverUseSecrets guards the fork-safety
// constraint this whole slice was built under: windows-test-binaries and its
// two "-prebuilt" consumers run on every PR, including from forks and
// Dependabot, and must never need a secret to do so (they only compile code
// already in the checkout and move an artifact between jobs). The same
// applies to main.yml's cache-seeding twin, which never runs on a fork PR at
// all but should still carry no incentive to grow one.
func TestWindowsCrossCompileJobsNeverUseSecrets(t *testing.T) {
	pr := readCIWorkflow(t, "pr.yml")
	main := readCIWorkflow(t, "main.yml")

	type namedJob struct {
		workflow string
		name     string
		job      ciWorkflowJob
	}
	jobs := []namedJob{
		{"pr.yml", "windows-test-binaries", pr.job(t, "windows-test-binaries")},
		{"pr.yml", "test-windows-liveness-prebuilt", pr.job(t, "test-windows-liveness-prebuilt")},
		{"pr.yml", "worktree-remove-windows-prebuilt", pr.job(t, "worktree-remove-windows-prebuilt")},
		{"main.yml", "windows-test-binaries-cache", main.job(t, "windows-test-binaries-cache")},
	}
	for _, nj := range jobs {
		if nj.job.Secrets != nil {
			t.Errorf("%s job %s has non-nil secrets: %v", nj.workflow, nj.name, nj.job.Secrets)
		}
		for _, step := range nj.job.Steps {
			if strings.Contains(step.Run, "secrets.") {
				t.Errorf("%s job %s step %q run references secrets.*", nj.workflow, nj.name, step.Name)
			}
			for k, v := range step.Env {
				if strings.Contains(v, "secrets.") {
					t.Errorf("%s job %s step %q env %s references secrets.*", nj.workflow, nj.name, step.Name, k)
				}
			}
			for k, v := range step.With {
				if strings.Contains(v, "secrets.") {
					t.Errorf("%s job %s step %q with %s references secrets.*", nj.workflow, nj.name, step.Name, k)
				}
			}
		}
	}
}
