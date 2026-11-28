//go:build !windows

package testutil

import (
	"strings"
	"testing"
)

// TestCheckRyukDisabled_EnabledIsSilent covers be-ovg86 exit_contract bullet
// 2: a run with Ryuk enabled must be unchanged and silent.
func TestCheckRyukDisabled_EnabledIsSilent(t *testing.T) {
	if err := checkRyukDisabled("/home/whoever", false, false); err != nil {
		t.Fatalf("checkRyukDisabled(disabled=false) = %v, want nil", err)
	}
}

// TestCheckRyukDisabled_DisabledFailsLoudly covers be-ovg86 exit_contract
// bullet 1: a run with Ryuk disabled must produce an unmissable signal naming
// the resolved $HOME plus both override sources.
func TestCheckRyukDisabled_DisabledFailsLoudly(t *testing.T) {
	const home = "/fake/home/for/test"
	err := checkRyukDisabled(home, true, false)
	if err == nil {
		t.Fatal("checkRyukDisabled(disabled=true) = nil, want an unmissable error")
	}
	msg := err.Error()
	for _, want := range []string{
		home,
		".testcontainers.properties",
		"TESTCONTAINERS_RYUK_DISABLED",
		"BEADS_ALLOW_UNREAPED_TESTCONTAINERS",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("checkRyukDisabled error %q does not mention %q", msg, want)
		}
	}
}

// TestCheckRyukDisabled_AllowUnreapedOptOut covers the explicit opt-out named
// in be-ovg86 exit_contract bullet 1.
func TestCheckRyukDisabled_AllowUnreapedOptOut(t *testing.T) {
	if err := checkRyukDisabled("/fake/home", true, true); err != nil {
		t.Fatalf("checkRyukDisabled(disabled=true, allowUnreaped=true) = %v, want nil (explicit opt-out honored)", err)
	}
}

// TestResolvedHome_ReadsHomeEnv guards the load-bearing detail from be-ovg86:
// the message must name the ACTUAL resolved $HOME, not a guess. Ryuk's own
// config (testcontainers.ReadConfig) is cached process-wide behind an
// unexported sync.Once with no exported reset, so it is not safe to probe
// with varying env in a table of subtests (see ryuk.go); resolvedHome is
// deliberately a separate, uncached function so this part IS safe to test.
func TestResolvedHome_ReadsHomeEnv(t *testing.T) {
	t.Setenv("HOME", "/fake/home/for/resolved-home-test")
	if got := resolvedHome(); got != "/fake/home/for/resolved-home-test" {
		t.Fatalf("resolvedHome() = %q, want the $HOME override", got)
	}
}
