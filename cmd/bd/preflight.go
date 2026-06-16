package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/metrics"
)

// CheckResult represents the result of a single preflight check.
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Skipped bool   `json:"skipped,omitempty"`
	Warning bool   `json:"warning,omitempty"`
	Output  string `json:"output,omitempty"`
	Command string `json:"command"`
}

// PreflightResult represents the overall preflight check results.
type PreflightResult struct {
	Checks  []CheckResult `json:"checks"`
	Passed  bool          `json:"passed"`
	Summary string        `json:"summary"`
}

var preflightCmd = &cobra.Command{
	Use:     "preflight",
	GroupID: "maint",
	Short:   "Show PR readiness checklist",
	Long: `Display a checklist of common pre-PR checks for contributors.

This command helps catch common issues before pushing to CI:
- Tests not run locally
- Lint errors
- Unformatted Go files
- .beads/issues.jsonl pollution
- Stale nix vendorHash
- Version mismatches

Examples:
  bd preflight              # Show checklist
  bd preflight --check      # Run checks automatically
  bd preflight --check --json  # JSON output for programmatic use
  bd preflight --check --skip-lint  # Explicitly skip lint check
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runPreflight,
}

func init() {
	preflightCmd.Flags().Bool("check", false, "Run checks automatically")
	preflightCmd.Flags().Bool("fix", false, "Auto-fix issues where possible (vendorHash, version sync)")
	preflightCmd.Flags().Bool("json", false, "Output results as JSON")
	preflightCmd.Flags().Bool("skip-lint", false, "Skip lint check explicitly")

	rootCmd.AddCommand(preflightCmd)
}

func runPreflight(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("preflight")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	check, _ := cmd.Flags().GetBool("check")
	fix, _ := cmd.Flags().GetBool("fix")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	skipLint, _ := cmd.Flags().GetBool("skip-lint")

	if fix {
		return runFixes(jsonOutput)
	}

	if check {
		return runChecks(jsonOutput, skipLint)
	}

	// Static checklist mode — tailor the checklist to the detected project
	// stack so non-Go projects don't get a misleading Go/Nix checklist (GH#4364).
	root := git.GetRepoRoot()
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}

	fmt.Println("PR Readiness Checklist:")
	fmt.Println()
	for _, item := range buildPreflightChecklist(root) {
		fmt.Printf("[ ] %s\n", item)
	}
	fmt.Println()
	fmt.Println("Run 'bd preflight --check' to validate automatically.")
	return nil
}

// fileExists reports whether name exists directly under dir.
func fileExists(dir, name string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// buildPreflightChecklist returns PR-readiness checklist items tailored to the
// project's language stack, detected from standard marker files in dir. A
// project with no recognized stack gets a generic reminder rather than a
// misleading Go checklist, and the repo's own Go+Nix specific items
// (gms_pure_go build tags, nix vendorHash, version.go vs default.nix) only
// appear where they apply (GH#4364).
func buildPreflightChecklist(dir string) []string {
	// Preserve the exact rich checklist when run inside the beads repo itself
	// (its primary audience), so the gms_pure_go build tags and nix/version
	// reminders beads contributors rely on are not lost.
	if isBeadsRepo(dir) {
		return []string{
			"Tests pass: go test -tags gms_pure_go -short ./...",
			"Lint passes: golangci-lint run --build-tags=gms_pure_go ./...",
			"Formatting: gofmt -l .",
			"No beads pollution: check .beads/issues.jsonl diff",
			"Nix hash current: go.sum unchanged or vendorHash updated",
			"Version sync: version.go matches default.nix",
		}
	}

	var items []string
	switch {
	case fileExists(dir, "go.mod"):
		items = append(items,
			"Tests pass: go test ./...",
			"Lint passes: golangci-lint run ./...",
			"Formatting: gofmt -l .",
		)
	case fileExists(dir, "package.json"):
		items = append(items,
			"Tests pass: npm test",
			"Types check: tsc --noEmit",
		)
	case fileExists(dir, "pyproject.toml"), fileExists(dir, "setup.py"):
		items = append(items,
			"Tests pass: pytest",
			"Lint passes: ruff check",
		)
	case fileExists(dir, "Cargo.toml"):
		items = append(items,
			"Tests pass: cargo test",
			"Lint passes: cargo clippy",
		)
	default:
		items = append(items, "Tests pass: run your project's test suite, linter, and formatter")
	}

	// Relevant to any beads workspace, regardless of language.
	if fileExists(dir, ".beads") {
		items = append(items, "No beads pollution: check .beads/issues.jsonl diff")
	}

	return items
}

// isBeadsRepo reports whether dir is the beads source repo, detected by the
// module path in go.mod. Used to keep preflight's repo-specific checklist.
func isBeadsRepo(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // path is constructed internally (repo root + fixed filename)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")) == "github.com/steveyegge/beads"
		}
	}
	return false
}

// runChecks executes all preflight checks and reports results.
func runChecks(jsonOutput, skipLint bool) error {
	var results []CheckResult

	// Run test check
	testResult := runTestCheck()
	results = append(results, testResult)

	// Run lint check
	lintResult := runLintCheck(skipLint)
	results = append(results, lintResult)

	// Run formatting check
	fmtResult := runFmtCheck()
	results = append(results, fmtResult)

	// Run beads pollution check
	beadsResult := runBeadsPollutionCheck()
	results = append(results, beadsResult)

	// Run nix hash check
	nixResult := runNixHashCheck()
	results = append(results, nixResult)

	// Run version sync check
	versionResult := runVersionSyncCheck()
	results = append(results, versionResult)

	// Run AGENTS.md / CLAUDE.md divergence check
	divergenceResult := runAgentDocDivergenceCheck()
	results = append(results, divergenceResult)

	// Calculate overall result
	allPassed := true
	passCount := 0
	skipCount := 0
	warnCount := 0
	for _, r := range results {
		if r.Skipped {
			skipCount++
		} else if r.Warning {
			warnCount++
			// Warnings don't fail the overall result but count as "not passed"
		} else if r.Passed {
			passCount++
		} else {
			allPassed = false
		}
	}

	runCount := len(results) - skipCount
	summary := fmt.Sprintf("%d/%d checks passed", passCount, runCount)
	if warnCount > 0 {
		summary += fmt.Sprintf(", %d warning(s)", warnCount)
	}
	if skipCount > 0 {
		summary += fmt.Sprintf(" (%d skipped)", skipCount)
	}

	if jsonOutput {
		result := PreflightResult{
			Checks:  results,
			Passed:  allPassed,
			Summary: summary,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return HandleError("encoding preflight result: %v", err)
		}
		if !allPassed {
			return SilentExit()
		}
		return nil
	}
	for _, r := range results {
		if r.Skipped {
			fmt.Printf("⚠ %s (skipped)\n", r.Name)
		} else if r.Warning {
			fmt.Printf("⚠ %s\n", r.Name)
		} else if r.Passed {
			fmt.Printf("✓ %s\n", r.Name)
		} else {
			fmt.Printf("✗ %s\n", r.Name)
		}
		fmt.Printf("  Command: %s\n", r.Command)
		if r.Skipped && r.Output != "" {
			fmt.Printf("  Reason: %s\n", r.Output)
		} else if r.Warning && r.Output != "" {
			fmt.Printf("  Warning: %s\n", r.Output)
		} else if !r.Passed && r.Output != "" {
			output := truncateOutput(r.Output, 500)
			fmt.Printf("  Output:\n")
			for _, line := range strings.Split(output, "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
		fmt.Println()
	}
	fmt.Println(summary)

	if !allPassed {
		return SilentExit()
	}
	return nil
}

// runTestCheck runs go test -short ./... and returns the result.
func runTestCheck() CheckResult {
	command := "go test -tags gms_pure_go -short ./..."
	cmd := exec.Command("go", "test", "-tags", "gms_pure_go", "-short", "./...")
	output, err := cmd.CombinedOutput()

	return CheckResult{
		Name:    "Tests pass",
		Passed:  err == nil,
		Output:  string(output),
		Command: command,
	}
}

// runLintCheck runs golangci-lint and returns the result.
func runLintCheck(skipLint bool) CheckResult {
	command := "golangci-lint run --build-tags=gms_pure_go ./..."
	if skipLint {
		return CheckResult{
			Name:    "Lint passes",
			Passed:  false,
			Skipped: true,
			Warning: true,
			Output:  "lint check explicitly skipped by --skip-lint",
			Command: command,
		}
	}

	// Check if golangci-lint is available
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		return CheckResult{
			Name:    "Lint passes",
			Passed:  false,
			Output:  "golangci-lint not found in PATH (install it or rerun with --skip-lint)",
			Command: command,
		}
	}

	cmd := exec.Command("golangci-lint", "run", "--build-tags=gms_pure_go", "./...")
	output, err := cmd.CombinedOutput()

	return CheckResult{
		Name:    "Lint passes",
		Passed:  err == nil,
		Output:  string(output),
		Command: command,
	}
}

// runFmtCheck runs gofmt -l and fails if any files need formatting.
func runFmtCheck() CheckResult {
	command := "gofmt -l ."

	// Check if gofmt is available
	if _, err := exec.LookPath("gofmt"); err != nil {
		return CheckResult{
			Name:    "Formatting",
			Passed:  false,
			Output:  "gofmt not found in PATH (install Go toolchain)",
			Command: command,
		}
	}

	cmd := exec.Command("gofmt", "-l", ".")
	output, err := cmd.CombinedOutput()

	if err != nil {
		return CheckResult{
			Name:    "Formatting",
			Passed:  false,
			Output:  string(output),
			Command: command,
		}
	}

	unformatted := strings.TrimSpace(string(output))
	if unformatted != "" {
		return CheckResult{
			Name:    "Formatting",
			Passed:  false,
			Output:  fmt.Sprintf("Unformatted files:\n%s\nRun: gofmt -w .", unformatted),
			Command: command,
		}
	}

	return CheckResult{
		Name:    "Formatting",
		Passed:  true,
		Command: command,
	}
}

// runBeadsPollutionCheck detects .beads/issues.jsonl modifications vs merge base.
func runBeadsPollutionCheck() CheckResult {
	command := "git diff -- .beads/issues.jsonl"

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return CheckResult{
			Name:    "No beads pollution",
			Passed:  true,
			Command: command,
		}
	}

	// git diff requires a path relative to the worktree root.
	// If beadsDir points outside the worktree (shared .beads in a
	// worktree setup), convert to a relative path. When the path is
	// outside the worktree, the pollution check is skipped since git
	// cannot diff paths outside the working tree.
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if filepath.IsAbs(issuesPath) {
		repoRoot := git.GetRepoRoot()
		if repoRoot == "" {
			return CheckResult{
				Name:    "No beads pollution",
				Passed:  true,
				Command: command,
			}
		}
		rel, err := filepath.Rel(repoRoot, issuesPath)
		if err != nil || isPathOutsideRepo(rel) {
			return CheckResult{
				Name:    "No beads pollution",
				Passed:  true,
				Command: command,
				Output:  "Skipped: .beads is outside working tree (worktree setup)",
			}
		}
		issuesPath = rel
	}

	branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	branchOut, err := branchCmd.Output()
	if err != nil {
		return CheckResult{
			Name:    "No beads pollution",
			Passed:  false,
			Skipped: true,
			Output:  fmt.Sprintf("Cannot determine branch: %v", err),
			Command: command,
		}
	}
	branch := strings.TrimSpace(string(branchOut))

	var diffOutput []byte
	if branch != "main" && branch != "HEAD" {
		cmd := exec.Command("git", "diff", "origin/main...HEAD", "--", issuesPath)
		diffOutput, _ = cmd.Output()
	} else {
		cmd := exec.Command("git", "diff", "HEAD", "--", issuesPath)
		out1, _ := cmd.Output()
		cmd2 := exec.Command("git", "diff", "--cached", "--", issuesPath)
		out2, _ := cmd2.Output()
		diffOutput = append(out1, out2...)
	}

	if len(strings.TrimSpace(string(diffOutput))) > 0 {
		return CheckResult{
			Name:    "No beads pollution",
			Passed:  false,
			Output:  ".beads/issues.jsonl has been modified — revert changes before pushing",
			Command: command,
		}
	}

	return CheckResult{
		Name:    "No beads pollution",
		Passed:  true,
		Command: command,
	}
}

// isPathOutsideRepo checks if a relative path (from filepath.Rel) points
// outside the base directory by inspecting the first path segment.
func isPathOutsideRepo(rel string) bool {
	if rel == "" {
		return false
	}
	first := rel
	if i := strings.IndexAny(rel, "/\\"); i > 0 {
		first = rel[:i]
	}
	return first == ".."
}

// runNixHashCheck checks if go.sum has uncommitted changes that may require vendorHash update.
func runNixHashCheck() CheckResult {
	command := "git diff HEAD -- go.sum"

	// Check for unstaged changes to go.sum
	cmd := exec.Command("git", "diff", "--name-only", "HEAD", "--", "go.sum")
	output, _ := cmd.Output()

	// Check for staged changes to go.sum
	stagedCmd := exec.Command("git", "diff", "--name-only", "--cached", "--", "go.sum")
	stagedOutput, _ := stagedCmd.Output()

	hasChanges := len(strings.TrimSpace(string(output))) > 0 || len(strings.TrimSpace(string(stagedOutput))) > 0

	if hasChanges {
		return CheckResult{
			Name:    "Nix hash current",
			Passed:  false,
			Warning: true,
			Output:  "go.sum has uncommitted changes - vendorHash in default.nix may need updating",
			Command: command,
		}
	}

	return CheckResult{
		Name:    "Nix hash current",
		Passed:  true,
		Output:  "",
		Command: command,
	}
}

// runVersionSyncCheck checks that all version files are in sync.
// Prefers scripts/check-versions.sh (matches CI) with fallback to inline logic.
func runVersionSyncCheck() CheckResult {
	command := "scripts/check-versions.sh"

	// Try using the script (matches CI's check-version-consistency job)
	if _, err := os.Stat("scripts/check-versions.sh"); err == nil {
		cmd := exec.Command("bash", "scripts/check-versions.sh")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return CheckResult{
				Name:    "Version sync",
				Passed:  false,
				Output:  string(output),
				Command: command,
			}
		}
		return CheckResult{
			Name:    "Version sync",
			Passed:  true,
			Output:  string(output),
			Command: command,
		}
	}

	// Fallback: inline comparison of version.go and default.nix
	command = "Compare cmd/bd/version.go and default.nix"

	// Read version.go
	versionGoContent, err := os.ReadFile("cmd/bd/version.go")
	if err != nil {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Skipped: true,
			Output:  fmt.Sprintf("Cannot read cmd/bd/version.go: %v", err),
			Command: command,
		}
	}

	// Extract version from version.go
	versionGoRe := regexp.MustCompile(`Version\s*=\s*"([^"]+)"`)
	versionGoMatch := versionGoRe.FindSubmatch(versionGoContent)
	if versionGoMatch == nil {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Skipped: true,
			Output:  "Cannot parse version from version.go",
			Command: command,
		}
	}
	goVersion := string(versionGoMatch[1])

	// Read default.nix
	nixContent, err := os.ReadFile("default.nix")
	if err != nil {
		return CheckResult{
			Name:    "Version sync",
			Passed:  true,
			Skipped: true,
			Output:  "default.nix not found (skipping nix version check)",
			Command: command,
		}
	}

	// Extract version from default.nix
	nixRe := regexp.MustCompile(`version\s*=\s*"([^"]+)"`)
	nixMatch := nixRe.FindSubmatch(nixContent)
	if nixMatch == nil {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Skipped: true,
			Output:  "Cannot parse version from default.nix",
			Command: command,
		}
	}
	nixVersion := string(nixMatch[1])

	if goVersion != nixVersion {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Output:  fmt.Sprintf("Version mismatch: version.go=%s, default.nix=%s", goVersion, nixVersion),
			Command: command,
		}
	}

	return CheckResult{
		Name:    "Version sync",
		Passed:  true,
		Output:  fmt.Sprintf("Versions match: %s", goVersion),
		Command: command,
	}
}

// runAgentDocDivergenceCheck flags drift between AGENTS.md and CLAUDE.md
// user-authored regions so the inconsistency is caught pre-PR rather than in
// review.
func runAgentDocDivergenceCheck() CheckResult {
	command := "bd doctor (Agent Doc Divergence)"

	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		repoRoot = "."
	}
	check := doctor.CheckAgentDocDivergence(repoRoot)
	if check.Status == doctor.StatusOK {
		return CheckResult{
			Name:    "AGENTS.md/CLAUDE.md in sync",
			Passed:  true,
			Command: command,
		}
	}
	output := check.Message
	if check.Detail != "" {
		output += "\n" + check.Detail
	}
	if check.Fix != "" {
		output += "\n" + check.Fix
	}
	return CheckResult{
		Name:    "AGENTS.md/CLAUDE.md in sync",
		Passed:  false,
		Warning: true,
		Output:  output,
		Command: command,
	}
}

// truncateOutput truncates output to maxLen characters, adding ellipsis if truncated.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:maxLen]) + "\n... (truncated)"
}

// fixResult reports the outcome of one auto-fix operation.
type fixResult struct {
	Name    string `json:"name"`
	Fixed   bool   `json:"fixed"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
}

// runFixes executes auto-fix operations for fixable preflight checks.
func runFixes(jsonOutput bool) error {
	var results []fixResult
	hasError := false

	nixFixed, nixOld, nixNew, nixErr := fixNixHash()
	nr := fixResult{Name: "Nix vendorHash"}
	if nixErr != nil {
		nr.Error = nixErr.Error()
		hasError = true
	} else if nixFixed {
		nr.Fixed = true
		nr.Detail = fmt.Sprintf("%s → %s", nixOld, nixNew)
	} else {
		nr.Skipped = true
	}
	results = append(results, nr)

	versionFixed, versionOld, versionNew, versionErr := fixVersionSync()
	vr := fixResult{Name: "Version sync"}
	if versionErr != nil {
		vr.Error = versionErr.Error()
		hasError = true
	} else if versionFixed {
		vr.Fixed = true
		vr.Detail = fmt.Sprintf("version.go: %s → %s", versionOld, versionNew)
	} else {
		vr.Skipped = true
	}
	results = append(results, vr)

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding fix results: %v\n", err)
		}
		if hasError {
			return SilentExit()
		}
		return nil
	}

	for _, r := range results {
		switch {
		case r.Error != "":
			fmt.Printf("✗ %s\n  %s\n\n", r.Name, r.Error)
		case r.Fixed:
			fmt.Printf("✓ %s\n", r.Name)
			if r.Detail != "" {
				fmt.Printf("  %s\n", r.Detail)
			}
			fmt.Println()
		default:
			fmt.Printf("- %s (nothing to fix)\n\n", r.Name)
		}
	}

	if hasError {
		return SilentExit()
	}
	return nil
}

// fixNixHash computes and updates the vendorHash in default.nix.
// It uses a sentinel hash to trigger nix to report the correct hash.
// Returns (fixed, oldHash, newHash, err).
func fixNixHash() (bool, string, string, error) {
	// Check if go.sum has uncommitted changes (same heuristic as runNixHashCheck)
	cmd1 := exec.Command("git", "diff", "--name-only", "HEAD", "--", "go.sum")
	out1, _ := cmd1.Output()
	cmd2 := exec.Command("git", "diff", "--name-only", "--cached", "--", "go.sum")
	out2, _ := cmd2.Output()
	if len(strings.TrimSpace(string(out1))) == 0 && len(strings.TrimSpace(string(out2))) == 0 {
		return false, "", "", nil
	}

	if _, err := exec.LookPath("nix"); err != nil {
		return false, "", "", fmt.Errorf(
			"nix not found in PATH\n  Manual fix:\n" +
				"    1. Edit default.nix: set vendorHash = \"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n" +
				"    2. Run: nix build .#default\n" +
				"    3. Copy the 'got:' hash from the error into default.nix",
		)
	}

	nixPath := "default.nix"
	nixInfo, err := os.Stat(nixPath)
	if err != nil {
		return false, "", "", fmt.Errorf("cannot stat default.nix: %v", err)
	}
	nixPerm := nixInfo.Mode().Perm()

	content, err := os.ReadFile(nixPath)
	if err != nil {
		return false, "", "", fmt.Errorf("cannot read default.nix: %v", err)
	}

	re := regexp.MustCompile(`(vendorHash\s*=\s*)"([^"]+)"`)
	loc := re.FindSubmatchIndex(content)
	if loc == nil {
		return false, "", "", fmt.Errorf("vendorHash not found in default.nix")
	}
	oldHash := string(content[loc[4]:loc[5]])

	const sentinel = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	probed := append(append([]byte{}, content[:loc[4]]...), append([]byte(sentinel), content[loc[5]:]...)...)
	if err := os.WriteFile(nixPath, probed, nixPerm); err != nil {
		return false, "", "", fmt.Errorf("cannot write default.nix: %v", err)
	}

	restored := false
	defer func() {
		if !restored {
			_ = os.WriteFile(nixPath, content, nixPerm)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	nixCmd := exec.CommandContext(ctx, "nix", "build", ".#default", "--no-link")
	nixOut, _ := nixCmd.CombinedOutput()

	// Nix prints the correct hash in lines like "got:    sha256-..."
	hashRe := regexp.MustCompile(`got:\s+(sha256-[A-Za-z0-9+/]+=)`)
	m := hashRe.FindSubmatch(nixOut)
	if m == nil {
		// Fallback: pick any sha256-... that isn't our sentinel
		altRe := regexp.MustCompile(`sha256-([A-Za-z0-9+/]+=)`)
		for _, am := range altRe.FindAllSubmatch(nixOut, -1) {
			h := "sha256-" + string(am[1])
			if h != sentinel {
				m = [][]byte{nil, []byte(h)}
				break
			}
		}
	}
	if m == nil {
		return false, oldHash, "", fmt.Errorf("could not parse correct hash from nix output:\n%s", string(nixOut))
	}
	newHash := string(m[1])

	if newHash == oldHash {
		_ = os.WriteFile(nixPath, content, nixPerm)
		restored = true
		return false, oldHash, newHash, nil
	}

	updated := append(append([]byte{}, content[:loc[4]]...), append([]byte(newHash), content[loc[5]:]...)...)
	if err := os.WriteFile(nixPath, updated, nixPerm); err != nil {
		return false, oldHash, newHash, fmt.Errorf("cannot update default.nix: %v", err)
	}
	restored = true
	return true, oldHash, newHash, nil
}

// fixVersionSync updates cmd/bd/version.go to match the version in default.nix.
// default.nix is the source of truth. Returns (fixed, oldVersion, newVersion, err).
func fixVersionSync() (bool, string, string, error) {
	vgoPath := "cmd/bd/version.go"
	vgoInfo, err := os.Stat(vgoPath)
	if err != nil {
		return false, "", "", fmt.Errorf("cannot read cmd/bd/version.go: %v", err)
	}
	vgoPerm := vgoInfo.Mode().Perm()

	vgoContent, err := os.ReadFile(vgoPath)
	if err != nil {
		return false, "", "", fmt.Errorf("cannot read cmd/bd/version.go: %v", err)
	}

	vgoRe := regexp.MustCompile(`(Version\s*=\s*)"([^"]+)"`)
	vgoLoc := vgoRe.FindSubmatchIndex(vgoContent)
	if vgoLoc == nil {
		return false, "", "", fmt.Errorf("cannot parse Version from cmd/bd/version.go")
	}
	oldVersion := string(vgoContent[vgoLoc[4]:vgoLoc[5]])

	nixContent, err := os.ReadFile("default.nix")
	if err != nil {
		// No default.nix — nothing to sync against
		return false, "", "", nil
	}

	nixRe := regexp.MustCompile(`version\s*=\s*"([^"]+)"`)
	nixM := nixRe.FindSubmatch(nixContent)
	if nixM == nil {
		return false, "", "", fmt.Errorf("cannot parse version from default.nix")
	}
	newVersion := string(nixM[1])

	if oldVersion == newVersion {
		return false, oldVersion, newVersion, nil
	}

	updated := append(append([]byte{}, vgoContent[:vgoLoc[4]]...), append([]byte(newVersion), vgoContent[vgoLoc[5]:]...)...)
	if err := os.WriteFile(vgoPath, updated, vgoPerm); err != nil {
		return false, oldVersion, newVersion, fmt.Errorf("cannot update cmd/bd/version.go: %v", err)
	}
	return true, oldVersion, newVersion, nil
}
