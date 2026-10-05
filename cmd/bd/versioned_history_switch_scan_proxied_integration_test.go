//go:build cgo

package main

import (
	"fmt"
	"strings"
	"testing"
)

// THE NAME IS LOAD-BEARING: .github/scripts/proxied-test-shard.sh finds the tests the proxied-server
// lane runs by the name prefix (TestProxiedServer, TestServerMode) across cmd/bd/*_test.go, and that
// lane is the only one that enables the proxied-server tests. A test that dropped the prefix would
// skip everywhere else and never run.

// proxiedWriteRefusedWithHistoryOn is writeRefusedWithHistoryOn on the proxied route: it asks
// whether an ordinary write to the issue is refused when the process records versions because its
// environment says so, a plane the pre-enable scan does not guard.
func proxiedWriteRefusedWithHistoryOn(t *testing.T, bd, dir, id string) bool {
	t.Helper()
	_, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, dir, []string{vhEnvVar + "=1"}, "update", id, "--notes", "touched with history on")
	if err == nil {
		return false
	}
	if !strings.Contains(stderr, "I-JSON") {
		t.Fatalf("bd update %s failed, but not with the mint's refusal of a number outside the I-JSON range:\n%s", id, stderr)
	}
	return true
}

// TestProxiedServerVersionedHistorySwitchRefusesAnUnrecordableGateTimeout is the proxied-server twin
// of TestEmbeddedVersionedHistorySwitchRefusesAnUnrecordableGateTimeout: the same scan, read through
// the proxied route's own reader instead of the store the command opened.
//
// That is the point of running it. The scan reads a lite projection, and each route builds its own,
// so a route whose projection left out a gate's timeout would clear the switch over a gate the mint
// then refuses every write to. A timeout past 2^53-1 ns (about 104.25 days) must be refused here
// exactly as on the direct route.
//
// What this pins, against a real server:
//   - the refusal names the gate past the range and the field ("timeout"), and not the gate in range,
//   - it writes NOTHING: the setting is still off afterwards,
//   - the scan and the mint AGREE: the issues whose next write is refused with history on through the
//     environment are exactly the issues the scan named,
//   - the remedies it names work on this route, `bd sql` for the timeout included (it is not
//     available on an embedded store), and the switch then turns on.
func TestProxiedServerVersionedHistorySwitchRefusesAnUnrecordableGateTimeout(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "vsx")

	blocked := bdProxiedCreate(t, bd, p.dir, "blocked by two gates")
	clean := bdProxiedCreate(t, bd, p.dir, "clean")
	createGate := func(timeout string) string {
		t.Helper()
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "create", "--type=timer", "--blocks", blocked.ID, "--timeout", timeout, "--json")
		if err != nil {
			t.Fatalf("bd gate create --timeout %s failed: %v\nstdout:\n%s\nstderr:\n%s", timeout, err, stdout, stderr)
		}
		return gateIDFromCreateJSON(t, stdout)
	}
	// History is off, so nothing refuses a gate whose timeout a version could not record.
	overlong := createGate("2600h")
	inRange := createGate("2500h")
	holdsNumber := bdProxiedCreate(t, bd, p.dir, "holds a nanosecond timestamp", "--metadata", `{"ts":1727000000000000000}`)

	out := bdProxiedConfigFail(t, bd, p.dir, "set", versionedHistorySettingKey, "true")
	for name, id := range map[string]string{"the gate past the range": overlong, "the issue holding a number in metadata": holdsNumber.ID} {
		if !strings.Contains(out, id) {
			t.Errorf("the refusal does not name %s (%s), which holds a value a version could not record:\n%s", name, id, out)
		}
	}
	for name, id := range map[string]string{"the gate in range": inRange, "the issue the gates block": blocked.ID, "the clean issue": clean.ID} {
		if strings.Contains(out, id) {
			t.Errorf("the refusal names %s (%s), which the mint would record:\n%s", name, id, out)
		}
	}
	for _, want := range []string{"2 issues", `"timeout"`, `"metadata"`, "I-JSON", "bd delete <id> --force", "only previews", "unblocks anything it was blocking", "bd sql", "--unset-metadata"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not contain %q, so it does not say what is wrong or how to fix it:\n%s", want, out)
		}
	}
	if got := strings.TrimSpace(bdProxiedConfig(t, bd, p.dir, "get", versionedHistorySettingKey)); strings.HasPrefix(got, "true") {
		t.Fatalf("the refused `config set ... true` wrote the setting anyway: config get says %q", got)
	}

	// The scan and the mint must agree on which issues are the problem.
	named := idsNamedIn(out, "vsx")
	refused := map[string]bool{}
	for _, id := range []string{blocked.ID, clean.ID, overlong, inRange, holdsNumber.ID} {
		if proxiedWriteRefusedWithHistoryOn(t, bd, p.dir, id) {
			refused[id] = true
		}
	}
	if got, want := strings.Join(sortedIDSet(named), ","), strings.Join(sortedIDSet(refused), ","); got != want {
		t.Errorf("the scan named %s but the mint refused a write to %s: they must be the same issues", got, want)
	}
	if want := strings.Join(sortedIDSet(map[string]bool{overlong: true, holdsNumber.ID: true}), ","); strings.Join(sortedIDSet(refused), ",") != want {
		t.Errorf("the mint refused a write to %v, want exactly %s", sortedIDSet(refused), want)
	}

	// The remedies the refusal names, then the switch turns on.
	bdProxiedSQL(t, bd, p.dir, fmt.Sprintf("UPDATE issues SET timeout_ns = 7200000000000 WHERE id = '%s'", overlong))
	bdProxiedUpdate(t, bd, p.dir, holdsNumber.ID, "--unset-metadata", "ts")
	bdProxiedConfig(t, bd, p.dir, "set", versionedHistorySettingKey, "true")
	if got := strings.TrimSpace(bdProxiedConfig(t, bd, p.dir, "get", versionedHistorySettingKey)); !strings.HasPrefix(got, "true") {
		t.Fatalf("after applying the remedies the switch did not turn on: config get says %q", got)
	}
}
