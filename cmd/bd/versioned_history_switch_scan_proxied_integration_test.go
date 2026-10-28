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

// proxiedCreateWithHistoryOn is createWithHistoryOn on the proxied route: the row participates in
// history from its first write because the process records versions as its environment says so.
func proxiedCreateWithHistoryOn(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, dir, []string{vhEnvVar + "=1"}, append([]string{"create", "--silent"}, args...)...)
	if err != nil {
		t.Fatalf("bd create --silent %s with history on failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return strings.TrimSpace(stdout)
}

// TestProxiedServerVersionedHistorySwitchRefusesAnUnrecordableGateTimeout is the proxied-server twin
// of TestEmbeddedVersionedHistorySwitchRefusesAnUnrecordableGateTimeout: the same scan, read through
// the proxied route's own reader instead of the store the command opened, and its participation
// read through the provider's unit of work instead of the store's storage.HistoryParticipation.
//
// That is the point of running it. The scan reads a lite projection, and each route builds its own,
// so a route whose projection left out a gate's timeout would clear the switch over a participating
// gate the mint then refuses every write to. A timeout past 2^53-1 ns (about 104.25 days) must be
// refused here exactly as on the direct route. And each route reads participation its own way, so a
// route that lost it would refuse the switch over legacy rows the mint never versions.
//
// What this pins, against a real server:
//   - rows written while history was off are legacy: design §16.2b's write fence makes the mint skip
//     every write to them, so with history on through the environment no write to one is refused,
//     and the scan names none of them, a gate past the range and a metadata number included,
//   - a gate that participates and holds a timeout past the range (set with bd sql, a writer that
//     does not record) is named, with the field ("timeout"), and a participating gate in range is not,
//   - it writes NOTHING: the setting is still off afterwards,
//   - the scan and the mint AGREE on every row: the issues the scan names are exactly the ones whose
//     next write is refused with history on through the environment,
//   - the remedies it names work on this route, `bd sql` for the timeout included (it is not
//     available on an embedded store), and the switch then turns on with the legacy rows untouched.
func TestProxiedServerVersionedHistorySwitchRefusesAnUnrecordableGateTimeout(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "vsx")

	blocked := bdProxiedCreate(t, bd, p.dir, "blocked by the gates")
	clean := bdProxiedCreate(t, bd, p.dir, "clean")
	createGate := func(timeout string, env []string) string {
		t.Helper()
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, env, "gate", "create", "--type=timer", "--blocks", blocked.ID, "--timeout", timeout, "--json")
		if err != nil {
			t.Fatalf("bd gate create --timeout %s failed: %v\nstdout:\n%s\nstderr:\n%s", timeout, err, stdout, stderr)
		}
		return gateIDFromCreateJSON(t, stdout)
	}
	// History is off, so nothing refuses a gate whose timeout a version could not record. These rows
	// are legacy.
	legacyOverlong := createGate("2600h", nil)
	legacyHoldsNumber := bdProxiedCreate(t, bd, p.dir, "legacy, holds a nanosecond timestamp", "--metadata", `{"ts":1727000000000000000}`)

	// Rows that participate: created while history is on (in range), then given a value a version could
	// not record by writers that do not record: bd sql for the gate's timeout, and bd update with history
	// off for the metadata.
	historyOn := []string{vhEnvVar + "=1"}
	overlong := createGate("1h", historyOn)
	inRange := createGate("2500h", historyOn)
	holdsNumber := proxiedCreateWithHistoryOn(t, bd, p.dir, "participates, then holds a nanosecond timestamp", "--metadata", `{"ts":1}`)
	bdProxiedSQL(t, bd, p.dir, fmt.Sprintf("UPDATE issues SET timeout_ns = 9360000000000000 WHERE id = '%s'", overlong))
	bdProxiedUpdate(t, bd, p.dir, holdsNumber, "--metadata", `{"ts":1727000000000000000}`)

	out := bdProxiedConfigFail(t, bd, p.dir, "set", versionedHistorySettingKey, "true")
	for name, id := range map[string]string{"the participating gate past the range": overlong, "the participating issue holding a number in metadata": holdsNumber} {
		if !strings.Contains(out, id) {
			t.Errorf("the refusal does not name %s (%s), which holds a value a version could not record:\n%s", name, id, out)
		}
	}
	for name, id := range map[string]string{
		"the gate in range":                         inRange,
		"the issue the gates block":                 blocked.ID,
		"the clean issue":                           clean.ID,
		"the legacy gate past the range":            legacyOverlong,
		"the legacy issue holding a metadata value": legacyHoldsNumber.ID,
	} {
		if strings.Contains(out, id) {
			t.Errorf("the refusal names %s (%s), whose writes the mint never refuses:\n%s", name, id, out)
		}
	}
	for _, want := range []string{"2 issues", `"timeout"`, `"metadata"`, "I-JSON", "participates in history", "bd delete <id> --force", "only previews", "unblocks anything it was blocking", "bd sql", "--unset-metadata"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not contain %q, so it does not say what is wrong or how to fix it:\n%s", want, out)
		}
	}
	if got := strings.TrimSpace(bdProxiedConfig(t, bd, p.dir, "get", versionedHistorySettingKey)); strings.HasPrefix(got, "true") {
		t.Fatalf("the refused `config set ... true` wrote the setting anyway: config get says %q", got)
	}

	// Ask the mint, through a process that records because its environment says so (a plane the scan
	// does not guard), what a write to each row does, and compare it with what the scan named. They must
	// be the same issues: the participating offenders, and no legacy row whatever it holds.
	named := idsNamedIn(out, "vsx")
	refused := map[string]bool{}
	for _, id := range []string{blocked.ID, clean.ID, legacyOverlong, legacyHoldsNumber.ID, inRange, overlong, holdsNumber} {
		if proxiedWriteRefusedWithHistoryOn(t, bd, p.dir, id) {
			refused[id] = true
		}
	}
	want := strings.Join(sortedIDSet(map[string]bool{overlong: true, holdsNumber: true}), ",")
	if got := strings.Join(sortedIDSet(named), ","); got != want {
		t.Errorf("the scan named %s, want exactly the participating offenders %s", got, want)
	}
	if got := strings.Join(sortedIDSet(refused), ","); got != want {
		t.Errorf("the mint refused a write to %s, want exactly the participating offenders %s", got, want)
	}

	// The remedies the refusal names, then the switch turns on, with the legacy rows still holding
	// their values.
	bdProxiedSQL(t, bd, p.dir, fmt.Sprintf("UPDATE issues SET timeout_ns = 7200000000000 WHERE id = '%s'", overlong))
	bdProxiedUpdate(t, bd, p.dir, holdsNumber, "--unset-metadata", "ts")
	bdProxiedConfig(t, bd, p.dir, "set", versionedHistorySettingKey, "true")
	if got := strings.TrimSpace(bdProxiedConfig(t, bd, p.dir, "get", versionedHistorySettingKey)); !strings.HasPrefix(got, "true") {
		t.Fatalf("after applying the remedies the switch did not turn on: config get says %q", got)
	}
}

// TestProxiedServerVersionedHistorySwitchChecksTheLegacyProtosAPourCopies is the proxied-server
// twin of TestEmbeddedVersionedHistorySwitchChecksTheLegacyProtosAPourCopies. The walk that finds a
// proto's issues is the same code on both routes, but what it walks is what the route's own reader
// lists: each row's template flag, its parent and its parent-child dependencies, and the issues
// labeled template. A route whose listing lost the template flag would clear the switch over a
// template whose every persistent pour the mint then refuses, and one that lost the rest would drop
// the warning. bd cook does not run on this route, so the template here is made one with bd sql.
//
// What this pins, against a real server:
//   - it refuses over a legacy template that holds a value a version could not record, marked as
//     one, and writes NOTHING,
//   - it warns of, and does not refuse over, the other legacy issues of a proto: in a proto
//     assembled by hand, a step that inherited the template label through --parent, one created
//     with --no-inherit-labels and one attached with a parent-child dependency, each under the root,
//   - it names no legacy issue that is part of no proto,
//   - the scan and the mint AGREE: with history on through the environment, a pour of the proto is
//     refused,
//   - the template remedy it names, bd delete, works on this route, and the switch then turns on,
//     still warning of the hand-assembled steps; with history on those stay writable, and once they
//     are fixed with bd update the proto pours, and the legacy issue still stays writable.
func TestProxiedServerVersionedHistorySwitchChecksTheLegacyProtosAPourCopies(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "vsy")

	const timestampInNanoseconds = `{"ts":1727000000000000000}`
	// History is off, so every row below is legacy.
	hand := bdProxiedCreate(t, bd, p.dir, "hand-assembled proto", "--type", "epic", "--labels", "template")
	labeledStep := bdProxiedCreate(t, bd, p.dir, "inherits the template label", "--parent", hand.ID, "--metadata", timestampInNanoseconds)
	unlabeledStep := bdProxiedCreate(t, bd, p.dir, "opts out of the label", "--parent", hand.ID, "--no-inherit-labels", "--metadata", timestampInNanoseconds)
	attached := bdProxiedCreate(t, bd, p.dir, "attached afterwards", "--metadata", timestampInNanoseconds)
	if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", attached.ID, hand.ID, "--type", "parent-child"); err != nil {
		t.Fatalf("bd dep add %s %s --type parent-child failed: %v\n%s", attached.ID, hand.ID, err, out)
	}
	template := bdProxiedCreate(t, bd, p.dir, "a template", "--metadata", timestampInNanoseconds)
	bdProxiedSQL(t, bd, p.dir, fmt.Sprintf("UPDATE issues SET is_template = 1 WHERE id = '%s'", template.ID))
	legacy := bdProxiedCreate(t, bd, p.dir, "legacy, part of no proto", "--metadata", timestampInNanoseconds)

	steps := []string{labeledStep.ID, unlabeledStep.ID, attached.ID}
	checkWarning := func(out string) {
		t.Helper()
		for _, id := range steps {
			if !warnedUnder(out, id, hand.ID) {
				t.Errorf("the switch does not warn of %s under the proto rooted at %s, whose value a pour of it copies:\n%s", id, hand.ID, out)
			}
		}
		if !strings.Contains(out, fmt.Sprintf("Warning: %d legacy issues of a proto", len(steps))) {
			t.Errorf("the switch's warning does not count the hand-assembled steps:\n%s", out)
		}
	}

	out := bdProxiedConfigFail(t, bd, p.dir, "set", versionedHistorySettingKey, "true")
	for _, want := range []string{"1 issue holds", "  " + template.ID + "  (template) ", "bd mol pour and bd mol bond copy its value", "bd delete <id> --force"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not contain %q:\n%s", want, out)
		}
	}
	checkWarning(out)
	for name, id := range map[string]string{"the proto's root": hand.ID, "the legacy issue in no proto": legacy.ID} {
		if strings.Contains(out, "  "+id+"  ") {
			t.Errorf("the switch names %s (%s), which holds no such value or is part of no proto:\n%s", name, id, out)
		}
	}
	if got := strings.TrimSpace(bdProxiedConfig(t, bd, p.dir, "get", versionedHistorySettingKey)); strings.HasPrefix(got, "true") {
		t.Fatalf("the refused `config set ... true` wrote the setting anyway: config get says %q", got)
	}

	// Ask the mint, through a process that records because its environment says so, what a pour of the
	// proto does.
	if _, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{vhEnvVar + "=1"}, "mol", "pour", hand.ID); err == nil || !strings.Contains(stderr, "I-JSON") {
		t.Errorf("with history on, a pour of %s was not refused by the mint (err %v), although the switch warned of its steps:\n%s", hand.ID, err, stderr)
	}

	// The template remedy the refusal names clears the switch, which still warns of the steps.
	if out, err := bdProxiedRun(t, bd, p.dir, "delete", template.ID, "--force"); err != nil {
		t.Fatalf("bd delete %s --force, the template remedy the refusal names, failed: %v\n%s", template.ID, err, out)
	}
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "config", "set", versionedHistorySettingKey, "true")
	if err != nil {
		t.Fatalf("after removing the template the switch was refused: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := strings.TrimSpace(bdProxiedConfig(t, bd, p.dir, "get", versionedHistorySettingKey)); !strings.HasPrefix(got, "true") {
		t.Fatalf("after removing the template the switch did not turn on: config get says %q", got)
	}
	checkWarning(stderr)

	// History is on: the steps the warning named are fixed with bd update, which the write fence lets
	// through, and the proto then pours.
	for _, id := range steps {
		bdProxiedUpdate(t, bd, p.dir, id, "--unset-metadata", "ts")
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "mol", "pour", hand.ID); err != nil {
		t.Errorf("with history on and the proto fixed, bd mol pour %s failed: %v\n%s", hand.ID, err, out)
	}
	bdProxiedUpdate(t, bd, p.dir, legacy.ID, "--notes", "touched with history on")
}
