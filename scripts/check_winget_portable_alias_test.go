package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The guard's own vocabulary is shared between mikefarah yq v4 and python-yq,
// so either flavour exercises these cases. Deliberately a plain skip rather
// than requireHostTool: //scripts:scripts_test is tagged host-tools, and that
// tag's declared inventory (git, python3, awk, sed, find, grep, timeout) does
// not include yq, so failing under Bazel would red the shared target on hosts
// that keep the promise the tag actually makes.
func skipWithoutYQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skipf("yq not available: %v", err)
	}
}

const (
	wingetAliasManifest = `PackageIdentifier: Example.Beads
PackageVersion: 1.2.3
InstallerType: zip
NestedInstallerType: portable
NestedInstallerFiles:
  - RelativeFilePath: bd.exe
    PortableCommandAlias: bd
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/beads_1.2.3_windows_amd64.zip
    InstallerSha256: 12B1D37344D3B1543301E21A2B9ED3AB6AE009F0418441F3DE5F762B40769A6B
ManifestType: installer
ManifestVersion: 1.12.0
`

	// No NestedInstallerFiles at all, which is the shape of every non-nested
	// installer type. PortableCommandAlias is meaningless here.
	wingetMsiManifest = `PackageIdentifier: Example.Beads
PackageVersion: 1.2.3
InstallerType: msi
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/beads_1.2.3_windows_amd64.msi
    InstallerSha256: 12B1D37344D3B1543301E21A2B9ED3AB6AE009F0418441F3DE5F762B40769A6B
ManifestType: installer
ManifestVersion: 1.12.0
`
)

// runWingetAliasGuard runs check-winget-portable-alias.sh against a throwaway
// tree holding the given files. The script resolves the repository from its own
// location, so it is copied in beside a synthetic winget/ directory. The real
// in-tree manifests are covered by the check-build-tags job, which runs this
// same script against the actual checkout.
func runWingetAliasGuard(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("guard is a Bash boundary")
	}
	skipWithoutYQ(t)
	script, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "check-winget-portable-alias.sh"))
	if err != nil {
		t.Fatalf("read check-winget-portable-alias.sh: %v", err)
	}
	dir := t.TempDir()
	all := map[string]string{"scripts/check-winget-portable-alias.sh": string(script)}
	for rel, content := range files {
		all[rel] = content
	}
	for rel, content := range all {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(dir, "scripts", "check-winget-portable-alias.sh"))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestWingetAliasGuardAcceptsPortableManifestWithAlias(t *testing.T) {
	out, err := runWingetAliasGuard(t, map[string]string{
		"winget/Example.Beads.installer.yaml": wingetAliasManifest,
	})
	if err != nil {
		t.Fatalf("a portable manifest with PortableCommandAlias should pass: %v\n%s", err, out)
	}
	if !strings.Contains(out, "OK:") {
		t.Errorf("expected an OK line, got:\n%s", out)
	}
}

func TestWingetAliasGuardRejectsMissingAlias(t *testing.T) {
	out, err := runWingetAliasGuard(t, map[string]string{
		"winget/Example.Beads.installer.yaml": strings.ReplaceAll(
			wingetAliasManifest, "    PortableCommandAlias: bd\n", ""),
	})
	if err == nil {
		t.Fatalf("a portable manifest without PortableCommandAlias was accepted:\n%s", out)
	}
	if !strings.Contains(out, "missing valid PortableCommandAlias") {
		t.Errorf("expected the alias diagnostic, got:\n%s", out)
	}
}

// The alias must be exactly `bd`; that is the name winget links onto PATH.
func TestWingetAliasGuardRejectsWrongAlias(t *testing.T) {
	out, err := runWingetAliasGuard(t, map[string]string{
		"winget/Example.Beads.installer.yaml": strings.ReplaceAll(
			wingetAliasManifest, "PortableCommandAlias: bd", "PortableCommandAlias: beads"),
	})
	if err == nil {
		t.Fatalf("a manifest aliasing something other than bd was accepted:\n%s", out)
	}
	if !strings.Contains(out, "missing valid PortableCommandAlias") {
		t.Errorf("expected the alias diagnostic, got:\n%s", out)
	}
}

func TestWingetAliasGuardRejectsEmptyWingetDir(t *testing.T) {
	out, err := runWingetAliasGuard(t, nil)
	if err == nil {
		t.Fatalf("an empty winget/ directory was accepted:\n%s", out)
	}
	if !strings.Contains(out, "no winget/*.installer.yaml files found") {
		t.Errorf("expected the empty-set diagnostic, got:\n%s", out)
	}
}

// An unparseable manifest must be reported as a parse failure rather than
// reported as a missing alias or skipped as a non-portable installer.
func TestWingetAliasGuardRejectsUnparseableManifest(t *testing.T) {
	out, err := runWingetAliasGuard(t, map[string]string{
		"winget/Example.Beads.installer.yaml": "PackageIdentifier: Example.Beads\n\tInstallerSha256: [unclosed\n",
	})
	if err == nil {
		t.Fatalf("an unparseable manifest was accepted:\n%s", out)
	}
	if !strings.Contains(out, "is not parseable YAML") {
		t.Errorf("expected the parse diagnostic, got:\n%s", out)
	}
	if strings.Contains(out, "SKIP:") {
		t.Errorf("an unparseable manifest must not be skipped as non-portable:\n%s", out)
	}
	// yq's own stderr has to survive to the log, otherwise a parse error is
	// indistinguishable from a missing alias. Matched loosely because the
	// wording differs between yq flavours.
	if !strings.Contains(strings.ToLower(out), "error") {
		t.Errorf("expected yq's own diagnostic to reach the log, got:\n%s", out)
	}
}

// A missing yq must be named, not reported as every manifest missing its alias.
func TestWingetAliasGuardNamesMissingYQ(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard is a Bash boundary")
	}
	script, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "check-winget-portable-alias.sh"))
	if err != nil {
		t.Fatalf("read check-winget-portable-alias.sh: %v", err)
	}
	dir := t.TempDir()
	for rel, content := range map[string]string{
		"scripts/check-winget-portable-alias.sh": string(script),
		"winget/Example.Beads.installer.yaml":    wingetAliasManifest,
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	emptyBin := filepath.Join(dir, "emptybin")
	if err := os.MkdirAll(emptyBin, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", emptyBin, err)
	}
	cmd := exec.Command("bash", filepath.Join(dir, "scripts", "check-winget-portable-alias.sh"))
	cmd.Dir = dir
	// The tool check is bash builtins only, so it still reports with no PATH.
	cmd.Env = append(os.Environ(), "PATH="+emptyBin)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("guard passed with no yq on PATH:\n%s", out)
	}
	if !strings.Contains(string(out), "yq is required") {
		t.Errorf("expected the missing-yq diagnostic, got:\n%s", out)
	}
	if strings.Contains(string(out), "PortableCommandAlias") {
		t.Errorf("a missing tool must not be reported as a missing alias:\n%s", out)
	}
}

// PortableCommandAlias is not a field non-nested installers have, so the guard
// must classify them out instead of hard-failing a required job.
func TestWingetAliasGuardSkipsNonPortableInstaller(t *testing.T) {
	out, err := runWingetAliasGuard(t, map[string]string{
		"winget/Example.Beads.installer.yaml": wingetMsiManifest,
	})
	if err != nil {
		t.Fatalf("a non-portable manifest should not fail the guard: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SKIP:") {
		t.Errorf("expected a SKIP line, got:\n%s", out)
	}
}

// Classifying non-portable manifests out must not weaken the guard for the
// portable ones sharing the directory.
func TestWingetAliasGuardStillFailsPortableSiblingOfSkippedManifest(t *testing.T) {
	out, err := runWingetAliasGuard(t, map[string]string{
		"winget/Example.Msi.installer.yaml": wingetMsiManifest,
		"winget/Example.Beads.installer.yaml": strings.ReplaceAll(
			wingetAliasManifest, "    PortableCommandAlias: bd\n", ""),
	})
	if err == nil {
		t.Fatalf("a violating portable manifest was masked by a skipped sibling:\n%s", out)
	}
	if !strings.Contains(out, "SKIP:") || !strings.Contains(out, "missing valid PortableCommandAlias") {
		t.Errorf("expected both a SKIP and the alias diagnostic, got:\n%s", out)
	}
}

// The all-zero hash scripts/update-winget.sh substitutes for a missing
// windows_arm64 checksum is surfaced, but advisory: the legacy SteveYegge
// manifest already carries one, and this guard is not the gate for that.
func TestWingetAliasGuardWarnsButDoesNotFailOnPlaceholderSha256(t *testing.T) {
	arm64Entry := `  - Architecture: arm64
    InstallerUrl: https://example.invalid/beads_1.2.3_windows_arm64.zip
    InstallerSha256: "` + strings.Repeat("0", 64) + `"
ManifestType: installer`
	manifest := strings.ReplaceAll(wingetAliasManifest, "ManifestType: installer", arm64Entry)
	if !strings.Contains(manifest, "arm64") {
		t.Fatalf("fixture did not gain an arm64 installer entry:\n%s", manifest)
	}
	out, err := runWingetAliasGuard(t, map[string]string{
		"winget/Example.Beads.installer.yaml": manifest,
	})
	if err != nil {
		t.Fatalf("the placeholder tripwire must stay advisory: %v\n%s", err, out)
	}
	if !strings.Contains(out, "placeholder all-zero InstallerSha256") {
		t.Errorf("expected the placeholder warning, got:\n%s", out)
	}
}
