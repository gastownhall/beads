package main

import "testing"

// GH#7597: ship runs through the proxied unit of work, so its capability row
// is honored instead of refused before the provider opens.
func TestShipProxyRowHonored(t *testing.T) {
	cmd := transformTestCommand("ship", "force", "dry-run")
	row, ok := capabilityRowFor(cmd)
	if !ok || row.Path != "ship" || row.Rule.Outcome != ProxyOutcomeHonored {
		t.Fatalf("ship row = %#v (found %t), want an honored row", row, ok)
	}
	if err := validateProxyRegistryBeforeProvider(cmd, ProxyTopologyManagedLocal); err != nil {
		t.Fatalf("ship refused before the provider: %v", err)
	}
}
