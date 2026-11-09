package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// pinGuardVersion pins main.Version — the identity the guard compares a
// witness against — for the duration of a test and restores it on cleanup,
// the way version_tracking_test.go pins it for trackBdVersion. Tests that
// mutate it must not run in parallel with anything reading Version.
func pinGuardVersion(t *testing.T, version string) {
	t.Helper()
	previous := Version
	Version = version
	t.Cleanup(func() { Version = previous })
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

		{name: "historical server release", version: "0.62.0", want: witnessEraLegacy},
		{name: "historical v-prefixed release", version: "v0.9.1", want: witnessEraLegacy},
		{name: "historical pseudo-version", version: "v0.62.0-0.20250101000000-abcdefabcdef", want: witnessEraLegacy},
		{name: "historical release candidate", version: "0.62.0-rc.2", want: witnessEraLegacy},
		{name: "historical four-component build", version: "0.62.0.1", want: witnessEraLegacy},

		{name: "empty", version: "", want: witnessEraUnknown},
		{name: "whitespace only", version: "   ", want: witnessEraUnknown},
		{name: "genuine garbage", version: "not-a-version", want: witnessEraUnknown},
		{name: "truncated binary noise", version: "\x00\x01\x02", want: witnessEraUnknown},

		// A Homebrew --HEAD stamp names no release era: homebrew-core shipped
		// HEAD builds while its stable formula was still pre-1.0, so the shape
		// can name an old build as easily as a current one. The era stays
		// unknown here; admission trusts such a witness only when it exactly
		// matches the running binary's own stamp.
		{name: "brew HEAD stamp", version: "HEAD-f925f3f", want: witnessEraUnknown},
		{name: "brew HEAD stamp with revision", version: "HEAD-f925f3f_1", want: witnessEraUnknown},
		{name: "bare brew HEAD", version: "HEAD", want: witnessEraUnknown},
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
// next command reads it cleanly. That includes a Homebrew --HEAD build
// (GH#5603): the first command warns over another build's stamp, persists this
// build's stamp, and the next command admits silently. The write is the
// command's to make — preview, read-only and frozen commands leave the witness
// alone — so the silence below is conditional on persistence succeeding, not
// guaranteed by admission alone.
func TestLegacyUpgradeGuardWarningIsSelfHealing(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)

	t.Run("garbage witness heals to the running release", func(t *testing.T) {
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
	})

	t.Run("another build's HEAD stamp heals to this build's stamp", func(t *testing.T) {
		pinGuardVersion(t, "HEAD-f925f3f")
		warnings := captureLegacyUpgradeWarnings(t)
		beadsDir := writeSelectedServerWorkspace(t, "HEAD-0123abc")

		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			t.Fatalf("first guardLegacyUpgradeWorkspace() = %v, want nil", err)
		}
		if warnings.Len() == 0 {
			t.Fatal("guard admitted another build's HEAD stamp without warning")
		}

		// trackBdVersion persists this build's stamp once the admitted command
		// runs; model that write and check it succeeds.
		if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), Version); err != nil {
			t.Fatalf("writeLocalVersion() = %v, want nil", err)
		}

		warnings.Reset()
		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			t.Fatalf("second guardLegacyUpgradeWorkspace() = %v, want nil", err)
		}
		if warnings.Len() != 0 {
			t.Fatalf("guard warned again after the witness healed to this build's stamp: %q", warnings.String())
		}
	})
}

// TestLegacyUpgradeGuardAdmitsBrewHeadStamp covers the reporter's scenario in
// GH#5603 under a release binary: Homebrew stamps HEAD-<shortsha> into
// main.Version for --HEAD installs, bd writes it verbatim, and the workspace
// must still open. The stamp names no era and a release build never shares it,
// so it rides the warn-and-open arm exactly like any other unreadable
// witness; only a build whose own Version is that stamp trusts it (see
// TestLegacyUpgradeGuardTrustsThisBuildsBrewHeadStamp).
func TestLegacyUpgradeGuardAdmitsBrewHeadStamp(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	pinGuardVersion(t, "1.3.4")
	for _, stamp := range []string{"HEAD-f925f3f", "HEAD", "HEAD-f925f3f_1"} {
		t.Run(stamp, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeSelectedServerWorkspace(t, stamp)

			if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
			}
			if warnings.Len() == 0 {
				t.Fatalf("guard admitted witness %q without the unreadable-witness warning", stamp)
			}
		})
	}
}

// TestLegacyUpgradeGuardTrustsThisBuildsBrewHeadStamp pins the residual
// GH#5603 defect: a Homebrew --HEAD build rewrites its own commit-bearing
// stamp into .local_version identically on every run, so the warn-and-open arm
// repeated its warning for a workspace the running binary demonstrably owns —
// and version tracking cannot heal it, because the witness already equals
// Version. A witness exactly equal to the running binary's own HEAD-<sha>
// stamp (including its Homebrew revision suffix) is the same current-era
// evidence a semver witness is, so every invocation admits silently. No other
// shape acquires the exemption: another build's stamp, a different revision,
// and the identity-free shapes stay on the warn-and-open arm.
func TestLegacyUpgradeGuardTrustsThisBuildsBrewHeadStamp(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	pinGuardVersion(t, "HEAD-f925f3f")

	t.Run("own stamp admits silently on every invocation", func(t *testing.T) {
		for _, stamp := range []string{"HEAD-f925f3f", "HEAD-f925f3f_1"} {
			t.Run(stamp, func(t *testing.T) {
				// Homebrew injects the keg's whole version — revision suffix
				// included — into main.Version, so a matching witness carries
				// the same suffix.
				pinGuardVersion(t, stamp)
				warnings := captureLegacyUpgradeWarnings(t)
				beadsDir := writeSelectedServerWorkspace(t, stamp)

				for invocation := 1; invocation <= 2; invocation++ {
					if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
						t.Fatalf("guardLegacyUpgradeWorkspace() invocation %d = %v, want nil", invocation, err)
					}
					if warnings.Len() != 0 {
						t.Fatalf("guard invocation %d warned about this build's own stamp: %q", invocation, warnings.String())
					}
				}
			})
		}
	})

	t.Run("no other shape acquires the exemption", func(t *testing.T) {
		for _, stamp := range []string{
			"HEAD-0123abc",   // another build's commit
			"HEAD-f925f3f_2", // a different Homebrew revision
			"HEAD",           // identity-free: any head build can share it
			"HEAD_1",         // identity-free revision of bare HEAD
			"HEAD-abc",       // tail too short for a git abbreviation
			"HEAD-f925f3f_x", // malformed revision
			"not-a-version",
		} {
			t.Run(stamp, func(t *testing.T) {
				warnings := captureLegacyUpgradeWarnings(t)
				beadsDir := writeSelectedServerWorkspace(t, stamp)

				if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
					t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
				}
				if warnings.Len() == 0 {
					t.Fatalf("guard admitted witness %q without the unreadable-witness warning", stamp)
				}
			})
		}
	})
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
