package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/remedy"
)

// captureLegacyUpgradeWarnings redirects guard warnings for the duration of a
// test and returns the buffer they land in.
func captureLegacyUpgradeWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := legacyUpgradeWarnWriter
	legacyUpgradeWarnWriter = buf
	t.Cleanup(func() { legacyUpgradeWarnWriter = previous })
	return buf
}

func TestClassifyVersionWitness(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    witnessEra
	}{
		// The exact witness that refused five production workspaces: release
		// tooling stamped a Go pseudo-version into main.Version and the guard
		// counted four dot-separated fields instead of three.
		{name: "go pseudo-version", version: "v1.1.1-0.20260805093327-bf97b73749ac", want: witnessEraCurrent},
		{name: "plain release", version: "1.1.0", want: witnessEraCurrent},
		{name: "v-prefixed release", version: "v1.2.0", want: witnessEraCurrent},
		{name: "release candidate", version: "1.1.0-rc.1", want: witnessEraCurrent},
		{name: "v-prefixed release candidate", version: "v1.2.0-rc.1", want: witnessEraCurrent},
		{name: "build metadata", version: "1.2.0+build.5", want: witnessEraCurrent},
		{name: "prerelease and build metadata", version: "v2.0.0-beta.1+darwin.arm64", want: witnessEraCurrent},
		{name: "major only", version: "v3", want: witnessEraCurrent},
		{name: "surrounding whitespace", version: "  1.1.0\n", want: witnessEraCurrent},
		// A Homebrew --HEAD build stamps its own shape, not semver (GH#5603).
		{name: "brew head stamp", version: "HEAD-f925f3f", want: witnessEraCurrent},
		{name: "bare brew head stamp", version: "HEAD", want: witnessEraCurrent},
		{name: "brew head stamp with revision", version: "HEAD-f925f3f_1", want: witnessEraCurrent},
		{name: "brew head stamp with newline", version: "HEAD-f925f3f\n", want: witnessEraCurrent},

		{name: "historical server release", version: "0.62.0", want: witnessEraLegacy},
		{name: "historical v-prefixed release", version: "v0.9.1", want: witnessEraLegacy},
		{name: "historical pseudo-version", version: "v0.62.0-0.20250101000000-abcdefabcdef", want: witnessEraLegacy},
		{name: "historical release candidate", version: "0.62.0-rc.2", want: witnessEraLegacy},
		{name: "historical four-component build", version: "0.62.0.1", want: witnessEraLegacy},

		{name: "empty", version: "", want: witnessEraUnknown},
		{name: "whitespace only", version: "   ", want: witnessEraUnknown},
		{name: "genuine garbage", version: "not-a-version", want: witnessEraUnknown},
		{name: "truncated binary noise", version: "\x00\x01\x02", want: witnessEraUnknown},
		{name: "brew head lookalike dirty", version: "HEAD-f925f3f-dirty", want: witnessEraUnknown},
		{name: "brew head lookalike non-hex", version: "HEAD-zzzzzzz", want: witnessEraUnknown},
		{name: "brew head lookalike lowercase", version: "head", want: witnessEraUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyVersionWitness(tt.version); got != tt.want {
				t.Fatalf("classifyVersionWitness(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestLegacyServerVersionSpansHistoricalServerEra(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "0.55.0", want: true},
		{version: "0.62.21", want: true},
		{version: "v0.62.0-0.20250101000000-abcdefabcdef", want: true},
		{version: "0.54.9", want: false},
		{version: "0.63.0", want: false},
		{version: "1.1.0", want: false},
		{version: "v1.1.1-0.20260805093327-bf97b73749ac", want: false},
		{version: "not-a-version", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if got := legacyServerVersion(tt.version); got != tt.want {
				t.Fatalf("legacyServerVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

// writeSelectedServerWorkspace lays down the ambiguous shape the guard has to
// classify: a selected Dolt server workspace that still owns a populated local
// Dolt root.
func writeSelectedServerWorkspace(t *testing.T, version string) string {
	t.Helper()
	beadsDir := t.TempDir()
	metadata := []byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"selected_server_db"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), version); err != nil {
			t.Fatal(err)
		}
	}
	populateLegacyDoltRoot(t, beadsDir)
	return beadsDir
}

func TestLegacyUpgradeGuardAdmitsNonReleaseVersionWitnesses(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	versions := []string{
		"v1.1.1-0.20260805093327-bf97b73749ac",
		"1.1.0",
		"v1.2.0",
		"1.1.0-rc.1",
		"1.2.0+build.5",
	}

	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeSelectedServerWorkspace(t, version)

			if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
			}
			if warnings.Len() != 0 {
				t.Fatalf("guard warned about a readable witness: %q", warnings.String())
			}
		})
	}
}

func TestLegacyUpgradeGuardStillRefusesPreOneWorkspaces(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	versions := []string{
		"0.9.1",
		"v0.49.6",
		"0.55.0",
		"0.62.21",
		"v0.62.0-0.20250101000000-abcdefabcdef",
		"0.62.0.1",
	}

	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			captureLegacyUpgradeWarnings(t)
			beadsDir := writeSelectedServerWorkspace(t, version)

			if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
			}
		})
	}
}

func TestLegacyUpgradeGuardRefusesWorkspaceWithoutAnyWitness(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	captureLegacyUpgradeWarnings(t)
	beadsDir := writeSelectedServerWorkspace(t, "")

	if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
		t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
	}
}

// TestLegacyUpgradeGuardWarnsInsteadOfRefusingPresentButBlankWitness pins F1:
// a witness file that exists but is blank or whitespace-only — the shape an
// interrupted or disk-full best-effort writeLocalVersion leaves behind — must
// be classified as an unknown-era (unreadable) witness and warned-and-opened,
// not collapsed into the "genuinely missing" path and hard-refused. A truly
// absent witness must still refuse so the pre-1.0 safety invariant does not
// relax.
func TestLegacyUpgradeGuardWarnsInsteadOfRefusingPresentButBlankWitness(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	blankWitnesses := []struct {
		name    string
		content []byte
	}{
		{name: "zero byte", content: []byte("")},
		{name: "newline only", content: []byte("\n")},
		{name: "spaces and tabs", content: []byte("  \t\n")},
	}

	for _, tc := range blankWitnesses {
		t.Run(tc.name, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeSelectedServerWorkspace(t, "")
			// writeSelectedServerWorkspace skips the witness for an empty
			// version, leaving a genuinely-missing witness; overwrite it with a
			// present-but-blank file to exercise the failed-write shape.
			if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), tc.content, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil for a present-but-blank witness", err)
			}
			if warnings.Len() == 0 {
				t.Fatal("guard admitted a present-but-blank witness without warning")
			}
			if !strings.Contains(warnings.String(), localVersionFile) {
				t.Fatalf("guard warning = %q, want it to name the witness path", warnings.String())
			}
		})
	}

	t.Run("genuinely missing witness still refuses", func(t *testing.T) {
		captureLegacyUpgradeWarnings(t)
		// No .local_version is written at all: absence must keep refusing so the
		// pre-1.0 guard invariant does not relax alongside the blank-witness fix.
		beadsDir := writeSelectedServerWorkspace(t, "")

		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal for a missing witness", err)
		}
	})
}

// TestLegacyUpgradeVersionWitnessPresence pins the reader's present/absent
// signal that guardLegacyUpgradeWorkspace relies on to tell a present-but-blank
// witness (warn+open) apart from a missing one (refuse). Oversized and missing
// witnesses must both report absent so the pre-1.0 refusal cannot relax.
func TestLegacyUpgradeVersionWitnessPresence(t *testing.T) {
	t.Run("present but blank reports present with empty version", func(t *testing.T) {
		for _, content := range [][]byte{[]byte(""), []byte("\n"), []byte("  \t")} {
			beadsDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), content, 0o600); err != nil {
				t.Fatal(err)
			}
			version, present := legacyUpgradeVersionWitness(beadsDir)
			if !present || version != "" {
				t.Fatalf("legacyUpgradeVersionWitness() = (%q, %v), want (%q, true) for %q", version, present, "", content)
			}
		}
	})

	t.Run("missing witness reports absent", func(t *testing.T) {
		beadsDir := t.TempDir()
		if version, present := legacyUpgradeVersionWitness(beadsDir); present || version != "" {
			t.Fatalf("legacyUpgradeVersionWitness() = (%q, %v), want (%q, false) for a missing witness", version, present, "")
		}
	})

	t.Run("oversized witness reports absent", func(t *testing.T) {
		beadsDir := t.TempDir()
		oversized := bytes.Repeat([]byte("a"), 65)
		if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), oversized, 0o600); err != nil {
			t.Fatal(err)
		}
		if version, present := legacyUpgradeVersionWitness(beadsDir); present || version != "" {
			t.Fatalf("legacyUpgradeVersionWitness() = (%q, %v), want (%q, false) for an oversized witness", version, present, "")
		}
	})
}

func TestLegacyUpgradeGuardWarnsInsteadOfRefusingUnreadableWitness(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	warnings := captureLegacyUpgradeWarnings(t)
	beadsDir := writeSelectedServerWorkspace(t, "not-a-version")

	if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
		t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
	}
	warned := warnings.String()
	if !strings.Contains(warned, "not-a-version") || !strings.Contains(warned, localVersionFile) {
		t.Fatalf("guard warning = %q, want it to name the unreadable witness and its path", warned)
	}
}

// TestLegacyUpgradeGuardWarningIsSelfHealing models the PersistentPreRunE
// order — guard, then trackBdVersion — to show the warning is one-shot: the
// admitted command rewrites the witness with this binary's own version, so the
// next command reads it cleanly. A Homebrew --HEAD stamp, which bd rewrites
// identically every run, is read as current and never warns (GH#5603).
func TestLegacyUpgradeGuardWarningIsSelfHealing(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	warnings := captureLegacyUpgradeWarnings(t)
	beadsDir := writeSelectedServerWorkspace(t, "not-a-version")

	if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
		t.Fatalf("first guardLegacyUpgradeWorkspace() = %v, want nil", err)
	}
	if warnings.Len() == 0 {
		t.Fatal("guard admitted an unreadable witness without warning")
	}

	// trackBdVersion rewrites the witness whenever it differs from Version.
	if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), Version); err != nil {
		t.Fatal(err)
	}

	warnings.Reset()
	if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
		t.Fatalf("second guardLegacyUpgradeWorkspace() = %v, want nil", err)
	}
	if warnings.Len() != 0 {
		t.Fatalf("guard warned again after the witness healed: %q", warnings.String())
	}
}

// TestLegacyUpgradeGuardAdmitsBrewHeadStamp covers the reporter's scenario in
// GH#5603: Homebrew stamps HEAD-<shortsha> into main.Version for --HEAD
// installs, bd writes it verbatim, and the workspace must open without the
// unreadable-witness warning. bd rewrites the same stamp on every run, so a
// warning here would repeat on every command.
func TestLegacyUpgradeGuardAdmitsBrewHeadStamp(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	for _, stamp := range []string{"HEAD-f925f3f", "HEAD", "HEAD-f925f3f_1"} {
		t.Run(stamp, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeSelectedServerWorkspace(t, stamp)

			if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
			}
			if warnings.Len() != 0 {
				t.Fatalf("guard warned about a brew --HEAD stamp: %q", warnings.String())
			}
		})
	}
}

// TestLegacyUpgradeGuardBrewHeadStampMatchesReleaseWitness is the identity
// invariant of the reclassification: in server and embedded mode alike, the
// guard's outcome for a brew --HEAD stamp (error and warning bytes) equals its
// outcome for a 1.x release witness, and the guard leaves the witness file's
// hash unchanged.
func TestLegacyUpgradeGuardBrewHeadStampMatchesReleaseWitness(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	type outcome struct{ err, warning string }
	guard := func(t *testing.T, write func(*testing.T, string) string, witness string) outcome {
		t.Helper()
		warnings := captureLegacyUpgradeWarnings(t)
		beadsDir := write(t, witness)
		path := filepath.Join(beadsDir, localVersionFile)
		hashBefore := witnessFileHash(t, path)
		err := guardLegacyUpgradeWorkspace(beadsDir)
		if hashAfter := witnessFileHash(t, path); hashAfter != hashBefore {
			t.Fatalf("guard rewrote the %q witness: hash %s, want %s", witness, hashAfter, hashBefore)
		}
		// Each run has its own temp dir; compare outcomes with it masked.
		got := outcome{warning: strings.ReplaceAll(warnings.String(), beadsDir, "<beads>")}
		if err != nil {
			got.err = strings.ReplaceAll(err.Error(), beadsDir, "<beads>")
		}
		return got
	}
	modes := []struct {
		name  string
		write func(*testing.T, string) string
	}{
		{name: "server", write: writeSelectedServerWorkspace},
		{name: "embedded", write: func(t *testing.T, witness string) string {
			return writeDoltRootWorkspace(t, `{"backend":"dolt"}`, "", witness)
		}},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			release := guard(t, mode.write, "1.1.0")
			if mode.name == "server" && release != (outcome{}) {
				t.Fatalf("release witness outcome = %+v, want admitted without a warning", release)
			}
			// Identity: the stamp is indistinguishable from a release witness.
			if head := guard(t, mode.write, "HEAD-f925f3f"); head != release {
				t.Fatalf("brew --HEAD outcome = %+v, want the release witness outcome %+v", head, release)
			}
		})
	}
}

// witnessFileHash returns the sha256 of the version witness at path.
func witnessFileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// TestLegacyUpgradeGuardBrewHeadLookalikeStillWarns keeps the recognizer
// bounded: a stamp brew never emits is still an unreadable witness, and the
// server-mode warning for it stays byte-for-byte unchanged from main.
func TestLegacyUpgradeGuardBrewHeadLookalikeStillWarns(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	for _, stamp := range []string{"HEAD-f925f3f-dirty", "HEAD-zzzzzzz", "head", "not-a-version"} {
		t.Run(stamp, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeSelectedServerWorkspace(t, stamp)

			if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
			}
			// Spelled out, not rebuilt from the warning helper, so any change to
			// the unaffected path's output fails here.
			want := "Warning: unreadable version witness " + strconv.Quote(stamp) + " in " +
				remedy.PrintedValue(filepath.Join(beadsDir, localVersionFile)) +
				"; assuming a current workspace. Run 'bd doctor' to reset version tracking.\n"
			if got := warnings.String(); got != want {
				t.Fatalf("guard warning changed:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestVersionWitnessRoundTrip pins the contract the guard depends on: every
// version string bd can write, bd can read back and recognize as current.
func TestVersionWitnessRoundTrip(t *testing.T) {
	versions := []string{
		Version,
		"1.1.0",
		"v1.2.0",
		"1.1.0-rc.1",
		"v1.2.0-rc.1",
		"1.2.0+build.5",
		"v1.1.1-0.20260805093327-bf97b73749ac",
		"v2.0.0-beta.1+darwin.arm64",
		"HEAD-f925f3f", // a Homebrew --HEAD build (GH#5603)
	}

	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			beadsDir := t.TempDir()
			path := filepath.Join(beadsDir, localVersionFile)
			if err := writeLocalVersion(path, version); err != nil {
				t.Fatal(err)
			}

			if got := readLocalVersion(path); got != version {
				t.Fatalf("readLocalVersion() = %q, want %q", got, version)
			}
			witness, ok := legacyUpgradeVersionWitness(beadsDir)
			if !ok {
				t.Fatalf("legacyUpgradeVersionWitness() did not see the witness bd just wrote")
			}
			if witness != version {
				t.Fatalf("legacyUpgradeVersionWitness() = %q, want %q", witness, version)
			}
			if got := classifyVersionWitness(witness); got != witnessEraCurrent {
				t.Fatalf("classifyVersionWitness(%q) = %v, want %v", witness, got, witnessEraCurrent)
			}
		})
	}
}
