package docsync

import (
	"strings"
	"testing"
)

// TestServerIdentityUpgradeGuidance guards the documentation of the Dolt
// server identity check (GH#4637) for an operator whose existing managed
// server was started by a bd from before the check: the configuration
// reference and the troubleshooting entry explain the refusal, how to see
// what holds the port, the stop/start that pairs bd's own server, both
// spellings of the setting, which endpoints are covered and which paths still
// bypass the check, and never advise deleting or reinitializing a database.
func TestServerIdentityUpgradeGuidance(t *testing.T) {
	configuration := readRepoDoc(t, "docs", "reference", "configuration.md")
	troubleshooting := readRepoDoc(t, "docs", "reference", "troubleshooting.md")

	const configHeading = "## Dolt Server Identity"
	config, ok := docSubtree(configuration, configHeading)
	if !ok {
		t.Fatalf("docs/reference/configuration.md missing the %q section", configHeading)
	}
	requirePhrases(t, "configuration.md "+configHeading, config,
		".bd-server-uuid",
		"refuses, before it lists, creates, reads or writes any database there",
		"dolt.verify-server-identity: false",
		"BEADS_DOLT_VERIFY_SERVER_IDENTITY=false",
		"a value bd cannot read leaves it on",
		"the shared server",
		"a project's own server",
		"external server mode",
		"bd init --server-port",
		"unix sockets, TLS connections, proxied servers and gateways",
		"bd dolt stop && bd dolt start",
		"WSL1",
		"bd doctor --fix",
		"bd bootstrap",
		"the working-set flush bd runs before it stops a server",
	)
	for _, row := range []string{"| `dolt.verify-server-identity` |", "| `BEADS_DOLT_VERIFY_SERVER_IDENTITY` |"} {
		if !strings.Contains(configuration, row) {
			t.Errorf("configuration.md has no %s table row", row)
		}
	}

	const troubleHeading = "### Refusing the Dolt server: its identity cannot be verified"
	trouble, ok := docSubtree(troubleshooting, troubleHeading)
	if !ok {
		t.Fatalf("docs/reference/troubleshooting.md missing the %q section", troubleHeading)
	}
	requirePhrases(t, "troubleshooting.md "+troubleHeading, trouble,
		"it is not the server bd launched for this data directory",
		"lsof -i :3308",
		"netstat -aon | findstr :3308",
		"bd dolt stop && bd dolt start",
		"BEADS_DOLT_VERIFY_SERVER_IDENTITY=false",
		"dolt.verify-server-identity: false",
		"did not record its server identity",
		"bd doctor --fix",
		"bd bootstrap",
		"do not delete or reinitialize anything",
	)

	for where, section := range map[string]string{"configuration.md": config, "troubleshooting.md": trouble} {
		forbidPhrases(t, where+" server identity guidance", section,
			"--reinit", "--force", "rm -rf", "DROP DATABASE", "delete the database")
	}
}
