package docsync

import "testing"

const (
	unknownDoltModeHeading   = "### Unrecognized `dolt_mode`"
	legacyProxiedRootHeading = "### Older bd fails with `proxieddb: not a directory`"
)

// TestDoltModeAndLegacyRootUpgradeGuidance guards the two GH#5104 refusals
// users meet after mixing bd versions: an unrecognized metadata dolt_mode and
// the legacy proxied-server root marker. The upgrade guide explains each in
// its own section, the troubleshooting page routes both symptoms there, and
// the changelog records both without promising protection the marker does
// not give.
func TestDoltModeAndLegacyRootUpgradeGuidance(t *testing.T) {
	upgrading := readRepoDoc(t, "docs", "getting-started", "upgrading.md")

	t.Run("unknown mode", func(t *testing.T) {
		section, ok := docSubtree(upgrading, unknownDoltModeHeading)
		if !ok {
			t.Fatalf("docs/getting-started/upgrading.md missing the %q section", unknownDoltModeHeading)
		}
		where := "upgrading.md " + unknownDoltModeHeading
		requirePhrases(t, where, section,
			"is not recognized or supported by this bd build",
			".beads/metadata.json",
			`"embedded", "server" and "proxied-server"`,
			"no storage database was opened or modified",
			"upgrade every bd that opens this workspace",
			"restore dolt_mode to the mode this workspace was initialized with",
			"bd doctor",
			"not a root-layout problem",
		)
		forbidPhrases(t, where, section, "proxieddb: not a directory")
	})

	t.Run("legacy proxied root", func(t *testing.T) {
		section, ok := docSubtree(upgrading, legacyProxiedRootHeading)
		if !ok {
			t.Fatalf("docs/getting-started/upgrading.md missing the %q section", legacyProxiedRootHeading)
		}
		where := "upgrading.md " + legacyProxiedRootHeading
		requirePhrases(t, where, section,
			".beads/dolt",
			".beads/proxieddb",
			"too old for this workspace",
			"upgrade every bd that opens it",
			"nothing already exists at .beads/proxieddb",
			"a line of its own",
			"proxieddb/ alone is not enough",
			"git check-ignore",
			"bd doctor --fix",
			"external server",
			"an existing .beads/proxieddb directory is not protected or repaired",
			"before proxied-server mode existed",
			"never deletes a proxieddb directory",
			"not an unrecognized dolt_mode",
		)
		forbidPhrases(t, where, section, "rm -rf", "or newer", "or later")
	})

	t.Run("troubleshooting routes both symptoms", func(t *testing.T) {
		troubleshooting := readRepoDoc(t, "docs", "reference", "troubleshooting.md")
		for heading, phrases := range map[string][]string{
			unknownDoltModeHeading:   {"is not recognized or supported by this bd build", "/getting-started/upgrading"},
			legacyProxiedRootHeading: {"too old for this workspace", "/getting-started/upgrading"},
		} {
			section, ok := docSubtree(troubleshooting, heading)
			if !ok {
				t.Errorf("docs/reference/troubleshooting.md missing the %q section", heading)
				continue
			}
			requirePhrases(t, "troubleshooting.md "+heading, section, phrases...)
		}
	})

	t.Run("changelog", func(t *testing.T) {
		changelog := readRepoDoc(t, "CHANGELOG.md")
		unreleased, ok := docSubtree(changelog, "## [Unreleased]")
		if !ok {
			t.Fatal("CHANGELOG.md has no [Unreleased] section")
		}
		requirePhrases(t, "CHANGELOG.md [Unreleased]", unreleased,
			"unrecognized dolt_mode",
			"proxieddb: not a directory",
			"existing .beads/proxieddb directories are not protected or repaired",
		)
	})
}
