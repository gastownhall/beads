//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/workapi"
)

// THE NAME IS LOAD-BEARING for the same reason TestEmbeddedVersionedHistorySwitchRoundTrip's is:
// .github/scripts/embedded-test-shard.sh selects the tests the embedded job runs by
// `grep -rh '^func TestEmbedded' cmd/bd/*_test.go`, so a test that keeps this prefix runs in
// the one job that sets BEADS_TEST_EMBEDDED_DOLT=1.
//
// TestEmbeddedVersionedHistorySwitchRefusesUnversionableMetadata drives the REAL
// `bd config set versioned-history.enabled true` against a real store that already holds rows
// a version could not be recorded for.
//
// With history on, recording a version runs in the same transaction as the write, so a write
// that introduces a number outside the I-JSON exact-integer range already fails atomically.
// What that cannot help is a row that holds such a number BEFORE the switch is turned on:
// written while history was off, or through a path that does not mint (bd sql, a pull from a
// clone that had history off). Once history is on, every later write to that row fails if the
// row participates in history. The switch therefore checks first, with the same function the
// mint runs, and refuses to turn history on while any issue the mint would version holds a
// value it would refuse.
//
// A row participates when it was created while history was on. A row created while history was
// off is a legacy row, and design §16.2b's write fence skips a write to it before the mint looks
// at it, so a value it holds blocks no write to it. Each offender below therefore has a legacy
// twin holding the same value in the same status, and none of them is part of a proto, whose
// values a pour copies (TestEmbeddedVersionedHistorySwitchChecksTheLegacyProtosAPourCopies).
//
// What this pins, end to end and in a database of its own:
//   - the refusal exits non-zero and prints the count, the ids and the remedy,
//   - it names every participating issue the mint would version, whatever its status or type: a
//     closed issue, a pinned one and a gate are all hidden from a default `bd list`, so a scan that
//     read only what `bd list` shows would clear the switch over them,
//   - it names only those: the legacy twins are skipped by the write fence, and an ephemeral row
//     and a no-history row are never versioned, so none of them is listed or counted,
//   - it writes NOTHING: the setting is still off afterwards,
//   - turning history OFF never scans,
//   - once the participating rows are fixed with the command the refusal names, the switch turns
//     on, although every legacy twin still holds its value, and with history on a write to each
//     legacy twin still succeeds.
func TestEmbeddedVersionedHistorySwitchRefusesUnversionableMetadata(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	// The environment can only turn recording on and would make a passing run prove nothing
	// about the store's own setting, so clear it. No t.Parallel: t.Setenv is incompatible.
	t.Setenv("BD_VERSIONED_HISTORY_ENABLED", "")

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "vsc")

	const timestampInNanoseconds = `{"ts":1727000000000000000}`
	const largeInteger = `{"n":9007199254740993}`

	// participating creates the row while history is on, so it participates, and then, with
	// history off, writes the value a version could not record, which nothing refuses.
	participating := func(title, value string, args ...string) string {
		t.Helper()
		id := createWithHistoryOn(t, bd, dir, append([]string{title, "--metadata", `{"ok":1}`}, args...)...)
		bdRunOK(t, bd, dir, "update", id, "--metadata", value)
		return id
	}

	clean := createWithHistoryOn(t, bd, dir, "clean", "--metadata", `{"ok":1,"fraction":0.1}`)
	first := participating("holds a nanosecond timestamp", timestampInNanoseconds)
	second := participating("holds an integer past 2^53", largeInteger)
	// Never versioned, whatever plane they were created on: created while history is on, and
	// holding the value from the start.
	wisp := createWithHistoryOn(t, bd, dir, "ephemeral, never versioned", "--ephemeral", "--metadata", timestampInNanoseconds)
	noHistory := createWithHistoryOn(t, bd, dir, "no-history, never versioned", "--no-history", "--metadata", timestampInNanoseconds)

	// Three more the mint versions that a default `bd list` does not show. The scan has to ask for
	// every status and every type to see them.
	closed := participating("closed, still versioned", timestampInNanoseconds)
	bdRunOK(t, bd, dir, "close", closed)
	pinned := participating("pinned, still versioned", largeInteger)
	bdRunOK(t, bd, dir, "update", pinned, "--status", "pinned")
	gate := participating("a gate, still versioned", timestampInNanoseconds, "--type", "gate")

	// The legacy twins, written while history is off: the write fence skips every write to them.
	legacyOpen := bdCreateSilent(t, bd, dir, "legacy, holds a nanosecond timestamp", "--metadata", timestampInNanoseconds)
	legacyClosed := bdCreateSilent(t, bd, dir, "legacy, closed", "--metadata", timestampInNanoseconds)
	bdRunOK(t, bd, dir, "close", legacyClosed)
	legacyPinned := bdCreateSilent(t, bd, dir, "legacy, pinned", "--metadata", largeInteger)
	bdRunOK(t, bd, dir, "update", legacyPinned, "--status", "pinned")
	legacyGate := bdCreateSilent(t, bd, dir, "legacy, a gate", "--type", "gate", "--metadata", timestampInNanoseconds)
	legacy := map[string]string{
		"the legacy open issue":   legacyOpen,
		"the legacy closed issue": legacyClosed,
		"the legacy pinned issue": legacyPinned,
		"the legacy gate":         legacyGate,
	}

	out, code := bdRunFailCode(t, bd, dir, "config", "set", "versioned-history.enabled", "true")
	if code == 0 {
		t.Fatalf("enabling versioned history over unrecordable participating rows exited 0; out=%s", out)
	}
	offenders := map[string]string{
		"the open issue holding a timestamp":     first,
		"the open issue holding a large integer": second,
		"the closed issue":                       closed,
		"the pinned issue":                       pinned,
		"the gate":                               gate,
	}
	for name, id := range offenders {
		if !strings.Contains(out, id) {
			t.Errorf("the refusal does not name %s (%s), which participates and holds a value a version could not record:\n%s", name, id, out)
		}
	}
	for name, id := range map[string]string{"the clean issue": clean, "the ephemeral issue": wisp, "the no-history issue": noHistory} {
		if strings.Contains(out, id) {
			t.Errorf("the refusal names %s (%s), which the mint would never version:\n%s", name, id, out)
		}
	}
	for name, id := range legacy {
		if strings.Contains(out, id) {
			t.Errorf("the refusal names %s (%s), a legacy row the write fence skips whatever it holds:\n%s", name, id, out)
		}
	}
	if want := fmt.Sprintf("%d issues", len(offenders)); !strings.Contains(out, want) {
		t.Errorf("the refusal does not count the participating offenders alone (%d):\n%s", len(offenders), out)
	}
	for _, want := range []string{"bd update", "--metadata", "I-JSON", "participates in history"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not contain %q, so it does not say what is wrong or how to fix it:\n%s", want, out)
		}
	}

	// It refuses WITHOUT writing: the setting is still off.
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); strings.HasPrefix(got, "true") {
		t.Fatalf("the refused `config set ... true` wrote the setting anyway: config get says %q", got)
	}

	// Turning it off never scans, so it works with offenders present and is how a
	// store that is somehow already on gets out from under a poisoned row.
	bdConfig(t, bd, dir, "set", "versioned-history.enabled", "false")

	// The remedy the refusal names, one update per participating row, while history is off. The
	// legacy twins are left as they are.
	bdRunOK(t, bd, dir, "update", first, "--metadata", `{"ts":"1727000000000000000"}`)
	bdRunOK(t, bd, dir, "update", second, "--unset-metadata", "n")
	bdRunOK(t, bd, dir, "update", closed, "--metadata", `{"ts":"1727000000000000000"}`)
	bdRunOK(t, bd, dir, "update", pinned, "--unset-metadata", "n")
	bdRunOK(t, bd, dir, "update", gate, "--metadata", `{"ts":"1727000000000000000"}`)

	bdConfig(t, bd, dir, "set", "versioned-history.enabled", "true")
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); !strings.HasPrefix(got, "true") {
		t.Fatalf("after fixing every participating row the switch did not turn on: config get says %q", got)
	}
	// History is on now, through the store's own row, and the legacy twins still hold their values:
	// the write fence skips them, so writes to them still succeed.
	for name, id := range legacy {
		cmd := exec.Command(bd, "update", id, "--notes", "touched with history on")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("with history on, a write to %s (%s) failed; the write fence should have skipped it: %v\n%s", name, id, err, out)
		}
	}
}

// TestEmbeddedVersionedHistorySwitchScansEveryRowNotOnePage pins that the scan reads the whole
// store. A list read that sets no limit returns one page (workapi.DefaultListLimit rows), so a
// scan that stopped asking for everything would clear the switch over any offender past the first
// page and count only that page's worth in its refusal. The participation read that narrows the
// offenders to the rows that participate is asked about every offender too, so the store also
// holds as many legacy offenders, and the count must be the participating ones exactly.
//
// The rows are imported in one process each, and poisoned in one: creating a page and a half of
// issues one command at a time costs the better part of a minute in this job.
func TestEmbeddedVersionedHistorySwitchScansEveryRowNotOnePage(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Setenv("BD_VERSIONED_HISTORY_ENABLED", "")

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "vsp")

	// More rows than one page, and more than the refusal lists, so both counts are exercised.
	n := workapi.DefaultListLimit + 10
	var participatingRows, legacyRows strings.Builder
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("vsp-p%03d", i)
		ids = append(ids, id)
		fmt.Fprintf(&participatingRows, `{"id":%q,"title":"offender %03d","metadata":{"ts":1}}`+"\n", id, i)
		fmt.Fprintf(&legacyRows, `{"id":"vsp-l%03d","title":"legacy %03d","metadata":{"ts":1727000000000000000}}`+"\n", i, i)
	}
	importFile := func(name, rows string) string {
		file := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(file, []byte(rows), 0o600); err != nil {
			t.Fatalf("writing the rows to import: %v", err)
		}
		return file
	}
	// Imported while history is on, so each row participates from its first write; then, with
	// history off, given a value a version could not record.
	cmd := exec.Command(bd, "import", importFile("participating.jsonl", participatingRows.String()))
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), vhEnvVar+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd import with history on failed: %v\n%s", err, out)
	}
	bdRunOK(t, bd, dir, append(append([]string{"update"}, ids...), "--metadata", `{"ts":1727000000000000000}`)...)
	bdRunOK(t, bd, dir, "import", importFile("legacy.jsonl", legacyRows.String()))

	out, _ := bdRunFailCode(t, bd, dir, "config", "set", "versioned-history.enabled", "true")
	if want := fmt.Sprintf("%d issues", n); !strings.Contains(out, want) {
		t.Errorf("the refusal does not count the %d participating offenders exactly (a scan that read one page would say %d, one that kept the legacy rows %d):\n%s", n, workapi.DefaultListLimit, 2*n, out)
	}
	if want := fmt.Sprintf("... and %d more", n-versionedHistoryRefusalListLimit); !strings.Contains(out, want) {
		t.Errorf("the refusal does not say %q after the first %d ids:\n%s", want, versionedHistoryRefusalListLimit, out)
	}
	if strings.Contains(out, "vsp-l") {
		t.Errorf("the refusal names a legacy row, which the write fence skips whatever it holds:\n%s", out)
	}
}

// TestEmbeddedVersionedHistorySetManyRefusesUnversionableMetadata drives the REAL
// `bd config set-many versioned-history.enabled=true` against a store that holds a participating
// row a version could not be recorded for. set-many writes the same setting as `bd config set`
// through a validation loop of its own, and it once turned history on over such a row with no
// check at all: the next write to that row then failed.
//
// What this pins, end to end and in a database of its own:
//   - the batch is refused: it exits non-zero and the refusal names the issue and the remedy,
//   - it names only the row that participates, not a legacy row holding the same value,
//   - it writes NOTHING, neither the setting nor the other pair in the same batch, because the
//     check runs before any pair is written,
//   - a batch that turns history OFF never scans,
//   - once the participating row is fixed, the same batch turns history on and writes its other
//     pair, with the legacy row still holding its value.
func TestEmbeddedVersionedHistorySetManyRefusesUnversionableMetadata(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Setenv("BD_VERSIONED_HISTORY_ENABLED", "")

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "vsm")

	offender := createWithHistoryOn(t, bd, dir, "holds a nanosecond timestamp", "--metadata", `{"ts":1}`)
	bdRunOK(t, bd, dir, "update", offender, "--metadata", `{"ts":1727000000000000000}`)
	legacy := bdCreateSilent(t, bd, dir, "legacy, holds a nanosecond timestamp", "--metadata", `{"ts":1727000000000000000}`)

	// The other pair comes first, so a check made after the writes would already have stored it.
	out, _ := bdRunFailCode(t, bd, dir, "config", "set-many", "vsm.marker=1", "versioned-history.enabled=true")
	if !strings.Contains(out, offender) {
		t.Errorf("the refusal does not name %s, which participates and holds a value a version could not record:\n%s", offender, out)
	}
	if strings.Contains(out, legacy) {
		t.Errorf("the refusal names %s, a legacy row the write fence skips whatever it holds:\n%s", legacy, out)
	}
	for _, want := range []string{"1 issue", "bd update", "--metadata", "I-JSON"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not contain %q, so it does not say what is wrong or how to fix it:\n%s", want, out)
		}
	}

	// It refuses WITHOUT writing: the setting is still off and the other pair was not stored.
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); strings.HasPrefix(got, "true") {
		t.Fatalf("the refused `config set-many ... versioned-history.enabled=true` wrote the setting anyway: config get says %q", got)
	}
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "vsm.marker")); got == "1" {
		t.Fatalf("the refused batch stored its other pair anyway: config get vsm.marker says %q", got)
	}

	// Turning it off never scans, so a batch that does so works with the offender present.
	bdConfig(t, bd, dir, "set-many", "versioned-history.enabled=false")

	// The remedy the refusal names, then the same batch that was refused goes through.
	bdRunOK(t, bd, dir, "update", offender, "--unset-metadata", "ts")
	bdConfig(t, bd, dir, "set-many", "vsm.marker=1", "versioned-history.enabled=true")
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); !strings.HasPrefix(got, "true") {
		t.Fatalf("after fixing the row the batch did not turn the switch on: config get says %q", got)
	}
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "vsm.marker")); got != "1" {
		t.Fatalf("after fixing the row the batch did not store its other pair: config get vsm.marker says %q", got)
	}
}

// gateIDFromCreateJSON reads the id out of `bd gate create --json` output, which may carry
// warnings ahead of the object.
func gateIDFromCreateJSON(t *testing.T, out string) string {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in `bd gate create --json` output:\n%s", out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&created); err != nil || created.ID == "" {
		t.Fatalf("parsing `bd gate create --json` output: %v\n%s", err, out)
	}
	return created.ID
}

// createTimerGate creates a timer gate that blocks one issue, through the real command, so what
// reaches the store is what a person can write.
func createTimerGate(t *testing.T, bd, dir, blocks, timeout string) string {
	t.Helper()
	return gateIDFromCreateJSON(t, bdRunOK(t, bd, dir, "gate", "create", "--type=timer", "--blocks", blocks, "--timeout", timeout, "--json"))
}

// createTimerGateWithHistoryOn is createTimerGate in a process that records versions because its
// ENVIRONMENT says so, so the gate participates in history from its first write.
func createTimerGateWithHistoryOn(t *testing.T, bd, dir, blocks, timeout string) string {
	t.Helper()
	cmd := exec.Command(bd, "gate", "create", "--type=timer", "--blocks", blocks, "--timeout", timeout, "--json")
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), vhEnvVar+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd gate create --timeout %s with history on failed: %v\n%s", timeout, err, out)
	}
	return gateIDFromCreateJSON(t, string(out))
}

// setTimeoutBehindBD sets a row's timeout_ns with SQL straight into the embedded database, then
// commits it: the write of a writer that does not record versions (bd sql, an older bd), which is
// the only way a gate that participates in history comes to hold a timeout a version could not
// record. bd update cannot change a gate's timeout, and with history on a write that introduces such
// a timeout is refused. No bd process may be running against the store.
func setTimeoutBehindBD(t *testing.T, beadsDir, database, id string, nanoseconds int64) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	res, err := db.ExecContext(t.Context(), "UPDATE issues SET timeout_ns = ? WHERE id = ?", nanoseconds, id)
	if err != nil {
		t.Fatalf("set the timeout of %s: %v", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("set the timeout of %s: %d rows affected (err %v), want 1", id, n, err)
	}
	if _, err := db.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', 'test: a writer that does not record sets a timeout')"); err != nil {
		t.Fatalf("commit the timeout of %s: %v", id, err)
	}
}

// idsNamedIn returns the distinct issue ids with the given prefix that a command's output names.
func idsNamedIn(out, prefix string) map[string]bool {
	ids := map[string]bool{}
	for _, id := range regexp.MustCompile(regexp.QuoteMeta(prefix)+`-[0-9a-z]+`).FindAllString(out, -1) {
		ids[id] = true
	}
	return ids
}

func sortedIDSet(set map[string]bool) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// writeRefusedWithHistoryOn reports whether an ordinary write to the issue is refused when the
// process records versions because its ENVIRONMENT says so. That plane does not pass through the
// pre-enable scan, which makes it the way to ask the mint itself what it would refuse.
func writeRefusedWithHistoryOn(t *testing.T, bd, dir, id string) bool {
	t.Helper()
	cmd := exec.Command(bd, "update", id, "--notes", "touched with history on")
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), vhEnvVar+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("bd update %s failed without an exit code: %v\n%s", id, err, out)
	}
	if !strings.Contains(string(out), "I-JSON") {
		t.Fatalf("bd update %s failed, but not with the mint's refusal of a number outside the I-JSON range:\n%s", id, out)
	}
	return true
}

// createWithHistoryOn creates an issue in a process that records versions because its ENVIRONMENT
// says so, which makes the row participate in history from its first write. A row created while
// history is off does not participate: design §16.2b's write fence skips an update-shaped write to it.
func createWithHistoryOn(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"create", "--silent"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), vhEnvVar+"=1")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("bd create --silent %s with history on failed: %v\n%s", strings.Join(args, " "), err, exitErr.Stderr)
		}
		t.Fatalf("bd create --silent %s with history on failed: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// TestEmbeddedVersionedHistorySwitchRefusesAnUnrecordableGateTimeout drives the REAL
// `bd config set versioned-history.enabled true` against a store that holds gates whose timeout a
// version could not record.
//
// A gate's timeout is a time.Duration, which a version holds as a count of nanoseconds, so one past
// 2^53-1 ns (about 104.25 days) is a number outside the I-JSON exact-integer range. Nothing stops
// `bd gate create --timeout 2600h` while history is off, and once history is on every write to a
// participating gate, a close included, is refused. A scan that read only metadata cleared the
// switch over it.
//
// What this pins, end to end and in a database of its own:
//   - gates written while history was off are legacy rows: design §16.2b's write fence makes the
//     mint skip every write to them, so with history on through the environment (a plane the scan
//     does not guard) no write to one is refused, and the scan does not name them,
//   - a gate that participates and holds such a timeout (set behind bd's back, the way bd sql does)
//     is named, with the field the number was found in ("timeout"), and a gate in range is not,
//   - it offers the remedy for the field that is wrong: removing the gate, not a metadata edit, until
//     an issue with a number in its metadata is there too,
//   - it writes NOTHING: the setting is still off afterwards,
//   - the scan and the mint AGREE on every row: the issues the scan names are exactly the ones whose
//     next write is refused,
//   - once the remedies are applied, the switch turns on, with the legacy gates still past the range.
func TestEmbeddedVersionedHistorySwitchRefusesAnUnrecordableGateTimeout(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("embedded dolt integration tests are not enabled")
	}
	// The environment can only turn recording on and would make a passing run prove nothing
	// about the store's own setting, so clear it. No t.Parallel: t.Setenv is incompatible.
	t.Setenv(vhEnvVar, "")

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "vsg")

	blocked := bdCreateSilent(t, bd, dir, "blocked by the gates")
	clean := bdCreateSilent(t, bd, dir, "clean")
	// History is off, so nothing refuses a gate whose timeout a version could not record. These
	// gates are legacy rows.
	legacyOverlong := createTimerGate(t, bd, dir, blocked, "2600h")
	legacyInRange := createTimerGate(t, bd, dir, blocked, "2500h")
	legacyClosedOverlong := createTimerGate(t, bd, dir, blocked, "8760h")
	bdRunOK(t, bd, dir, "gate", "resolve", legacyClosedOverlong)

	// The scan has nothing to refuse: every offender is legacy. Turn the switch on and off again to
	// show it; turning it off never scans.
	bdConfig(t, bd, dir, "set", "versioned-history.enabled", "true")
	bdConfig(t, bd, dir, "set", "versioned-history.enabled", "false")

	// Gates that participate: created in range while history is on, then given a timeout past the
	// range by a writer that does not record, one of them closed first.
	overlong := createTimerGateWithHistoryOn(t, bd, dir, blocked, "1h")
	inRange := createTimerGateWithHistoryOn(t, bd, dir, blocked, "2500h")
	closedOverlong := createTimerGateWithHistoryOn(t, bd, dir, blocked, "1h")
	bdRunOK(t, bd, dir, "gate", "resolve", closedOverlong)
	setTimeoutBehindBD(t, beadsDir, "vsg", overlong, (2600 * time.Hour).Nanoseconds())
	setTimeoutBehindBD(t, beadsDir, "vsg", closedOverlong, (8760 * time.Hour).Nanoseconds())

	out, code := bdRunFailCode(t, bd, dir, "config", "set", "versioned-history.enabled", "true")
	if code == 0 {
		t.Fatalf("enabling versioned history over a participating gate whose timeout is past the range exited 0; out=%s", out)
	}
	for name, id := range map[string]string{"the open gate past the range": overlong, "the closed gate past the range": closedOverlong} {
		if !strings.Contains(out, id) {
			t.Errorf("the refusal does not name %s (%s), whose timeout a version could not record:\n%s", name, id, out)
		}
	}
	for name, id := range map[string]string{
		"the gate in range":                     inRange,
		"the issue the gates block":             blocked,
		"the clean issue":                       clean,
		"the legacy gate past the range":        legacyOverlong,
		"the legacy gate in range":              legacyInRange,
		"the legacy closed gate past the range": legacyClosedOverlong,
	} {
		if strings.Contains(out, id) {
			t.Errorf("the refusal names %s (%s), whose writes the mint never refuses:\n%s", name, id, out)
		}
	}
	for _, want := range []string{"2 issues", `"timeout"`, "I-JSON", "bd delete <id> --force", "only previews", "unblocks anything it was blocking"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not contain %q, so it does not say what is wrong or how to fix it:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--unset-metadata") {
		t.Errorf("the refusal offers a metadata edit although nothing holds a number in its metadata:\n%s", out)
	}
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); strings.HasPrefix(got, "true") {
		t.Fatalf("the refused `config set ... true` wrote the setting anyway: config get says %q", got)
	}

	// Add the other kind of offender, a participating issue with a number in its metadata, beside a
	// legacy one. The refusal now has two fields to fix and offers a remedy for each.
	holdsNumber := createWithHistoryOn(t, bd, dir, "participates, then holds a nanosecond timestamp", "--metadata", `{"ts":1}`)
	bdRunOK(t, bd, dir, "update", holdsNumber, "--metadata", `{"ts":1727000000000000000}`)
	legacyHoldsNumber := bdCreateSilent(t, bd, dir, "legacy, holds a nanosecond timestamp", "--metadata", `{"ts":1727000000000000000}`)
	out, _ = bdRunFailCode(t, bd, dir, "config", "set", "versioned-history.enabled", "true")
	for _, want := range []string{"3 issues", `"timeout"`, `"metadata"`, "bd delete <id> --force", "only previews", "unblocks anything it was blocking", "--unset-metadata"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal over a gate and a metadata number does not contain %q:\n%s", want, out)
		}
	}

	// Ask the mint, through a process that records because its environment says so (a plane the scan
	// does not guard), what a write to each row does, and compare it with what the scan named. They
	// must be the same issues: the participating offenders, and no legacy row whatever it holds.
	named := idsNamedIn(out, "vsg")
	refused := map[string]bool{}
	for _, id := range []string{blocked, clean, legacyOverlong, legacyInRange, legacyClosedOverlong, legacyHoldsNumber, inRange, overlong, closedOverlong, holdsNumber} {
		if writeRefusedWithHistoryOn(t, bd, dir, id) {
			refused[id] = true
		}
	}
	want := strings.Join(sortedIDSet(map[string]bool{overlong: true, closedOverlong: true, holdsNumber: true}), ",")
	if got := strings.Join(sortedIDSet(named), ","); got != want {
		t.Errorf("the scan named %s, want exactly the participating offenders %s", got, want)
	}
	if got := strings.Join(sortedIDSet(refused), ","); got != want {
		t.Errorf("the mint refused a write to %s, want exactly the participating offenders %s", got, want)
	}

	// The remedies the refusal names, then the switch turns on although the legacy gates and the
	// legacy issue still hold their values.
	bdRunOK(t, bd, dir, "delete", overlong, closedOverlong, "--force")
	bdRunOK(t, bd, dir, "update", holdsNumber, "--unset-metadata", "ts")
	bdConfig(t, bd, dir, "set", "versioned-history.enabled", "true")
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); !strings.HasPrefix(got, "true") {
		t.Fatalf("after applying the remedies the switch did not turn on: config get says %q", got)
	}
}

// TestEmbeddedConfigSetOtherKeysSkipTheVersionedHistoryScan pins that the pre-enable scan belongs
// to versioned-history.enabled alone. In a store that holds a row the scan refuses over, `bd config
// set` and `bd config set-many` of any other key -- given the value "true", which is what turns
// the switch on -- exit 0, store the value, print no refusal and leave the switch off. The switch
// itself is still refused afterwards, which proves the store held a row the scan would have named.
//
// TestEmbeddedVersionedHistorySwitchRefusesUnversionableMetadata pins that turning the switch OFF
// never scans; this pins the other half of "any other key ... passes without reading a row"
// (checkVersionedHistoryCanBeEnabled's doc comment) through the real commands.
func TestEmbeddedConfigSetOtherKeysSkipTheVersionedHistoryScan(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	// The offender below can only be written while nothing records versions. No t.Parallel:
	// t.Setenv is incompatible.
	t.Setenv(vhEnvVar, "")

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "vso")

	// It participates, so the scan refuses over it: created while history is on, then given a value
	// a version could not record while history is off.
	offender := createWithHistoryOn(t, bd, dir, "holds an integer past 2^53", "--metadata", `{"n":1}`)
	bdRunOK(t, bd, dir, "update", offender, "--metadata", `{"n":9007199254740993}`)

	for _, args := range [][]string{
		{"config", "set", "custom.vso-marker", "true"},
		{"config", "set-many", "custom.vso-batch=true"},
	} {
		stdout, stderr, exit := bdRunStreams(t, bd, dir, args...)
		if exit != 0 {
			t.Fatalf("bd %s exited %d in a store holding %s; the scan must not run for this key\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), exit, offender, stdout, stderr)
		}
		if out := stdout + stderr; strings.Contains(out, "cannot turn versioned history on") || strings.Contains(out, offender) {
			t.Errorf("bd %s printed the versioned-history refusal for a key that is not the switch:\n%s", strings.Join(args, " "), out)
		}
	}
	for key, want := range map[string]string{"custom.vso-marker": "true", "custom.vso-batch": "true"} {
		if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", key)); got != want {
			t.Errorf("config get %s = %q, want %q", key, got, want)
		}
	}
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); strings.HasPrefix(got, "true") {
		t.Fatalf("setting other keys turned versioned history on: config get says %q", got)
	}

	// The control: the scan does see the offender when the switch itself is set.
	out, _ := bdRunFailCode(t, bd, dir, "config", "set", "versioned-history.enabled", "true")
	if !strings.Contains(out, offender) {
		t.Fatalf("the refused switch does not name %s, so the pin above proves nothing:\n%s", offender, out)
	}
}

// molRefusedWithHistoryOn reports whether `bd mol <args>` (a pour or a bond) is refused when the
// process records versions because its ENVIRONMENT says so, a plane the pre-enable scan does not
// guard. A pour, or a bond of a template, copies the proto's values into the issues it creates, and
// each of them is versioned, so this asks the mint itself whether the proto holds a value it would
// refuse.
func molRefusedWithHistoryOn(t *testing.T, bd, dir string, args ...string) bool {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"mol"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), vhEnvVar+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("bd mol %s failed without an exit code: %v\n%s", strings.Join(args, " "), err, out)
	}
	if !strings.Contains(string(out), "I-JSON") {
		t.Fatalf("bd mol %s failed, but not with the mint's refusal of a number outside the I-JSON range:\n%s", strings.Join(args, " "), out)
	}
	return true
}

// pourJSON pours proto with history off and returns what --json reports.
func pourJSON(t *testing.T, bd, dir, proto string) pourJSONResult {
	t.Helper()
	out := bdRunOK(t, bd, dir, "mol", "pour", proto, "--json")
	var result pourJSONResult
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &result); err != nil {
		t.Fatalf("parse bd mol pour %s --json: %v\n%s", proto, err, out)
	}
	return result
}

// warnedUnder reports whether out lists id in the switch's warning as part of the proto rooted at
// root.
func warnedUnder(out, id, root string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  "+id+"  ") && strings.HasSuffix(line, "(proto "+root+")") {
			return true
		}
	}
	return false
}

// TestEmbeddedVersionedHistorySwitchChecksTheLegacyProtosAPourCopies drives the REAL
// `bd config set versioned-history.enabled true` against a store whose protos, and molecules
// poured from them, were written while history was off and hold values a version could not record.
//
// Every issue here is legacy, so the write fence skips every write to it. But `bd mol pour` and
// `bd mol bond` copy a proto's metadata and gate timeouts into every issue they create, and with
// history on each new issue is versioned, so every persistent pour of such a proto is refused. A
// template (what bd cook --persist writes) cannot be fixed with bd update, the in-place fix, and the
// pour's refusal names no fix, so the switch refuses over it and names the fixes that work (bd cook
// --persist --force, bd delete). Any other issue of a proto -- under a template or an issue labeled
// template, which the root of a molecule poured from a stored proto is too, since pour copies the
// proto's labels -- can be fixed in place with history on, so the switch only warns about it, naming
// the proto's root. A template whose label was removed is still a proto's root: pour refuses it, but
// bd mol bond treats any template as a proto and copies everything under it.
//
// What this pins, end to end and in a database of its own:
//   - it refuses over the templates bd cook --persist wrote, for a step's metadata and a gate's
//     timeout, marks them, tells how to fix one, and writes NOTHING,
//   - it warns of, and does not refuse over, the other legacy issues of a proto: in a proto
//     assembled by hand, a step that inherited the template label through --parent, one created
//     with --no-inherit-labels and one attached with a parent-child dependency; and the steps of
//     molecules poured from the cooked proto before the switch, each under its poured root; and an
//     issue attached under a cooked template whose template label was removed,
//   - it names no legacy issue that is part of no proto, a gate past the range included,
//   - the scan and the mint AGREE: with history on through the environment, a pour of each proto
//     it named is refused, a poured molecule's root included, and so is a bond of the template
//     without the label,
//   - the refusal's template remedy clears the switch, although the poured molecules still hold the
//     proto's old values: the switch turns on, still warning of them,
//   - with history on, the cooked proto pours, the hand-assembled steps and the poured gates stay
//     writable (bd update, and bd delete for a gate, the fixes the warning names), and once the
//     steps are fixed with bd update the hand-assembled proto pours too, and once the issue under
//     the template without the label is fixed, that template bonds.
func TestEmbeddedVersionedHistorySwitchChecksTheLegacyProtosAPourCopies(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	// The protos below can only be written while nothing records versions. No t.Parallel:
	// t.Setenv is incompatible.
	t.Setenv(vhEnvVar, "")

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "vsq")

	const timestampInNanoseconds = `{"ts":1727000000000000000}`

	// The formula lives outside every formula search path, so `bd mol pour quarterly` pours the proto
	// bd cook --persist wrote into the store, not the file.
	formula := filepath.Join(t.TempDir(), "quarterly.formula.toml")
	writeFormula := func(stamp, timeout string) {
		t.Helper()
		body := fmt.Sprintf(`formula = "quarterly"
description = "a quarterly review"
version = 1
type = "workflow"

[[steps]]
id = "prep"
title = "Prepare"
[steps.metadata]
ts = %s

[[steps]]
id = "review"
title = "Review"
depends_on = ["prep"]

[steps.gate]
type = "timer"
await_id = "quarter"
timeout = %q
`, stamp, timeout)
		if err := os.WriteFile(formula, []byte(body), 0o600); err != nil {
			t.Fatalf("writing the formula: %v", err)
		}
	}
	writeFormula("1727000000000000000", "2600h")
	bdRunOK(t, bd, dir, "cook", formula, "--persist")
	const cooked, cookedStep, cookedGate = "quarterly", "quarterly.prep", "quarterly.gate-review"

	// Two molecules poured from the stored proto while history is off: each carries the proto's
	// values, and its root carries the proto's template label.
	type pouredMolecule struct{ root, step, gate string }
	var poured []pouredMolecule
	for range 2 {
		result := pourJSON(t, bd, dir, cooked)
		m := pouredMolecule{root: result.NewEpicID, step: result.IDMapping[cookedStep], gate: result.IDMapping[cookedGate]}
		if m.root == "" || m.step == "" || m.gate == "" {
			t.Fatalf("bd mol pour %s --json did not map the proto's root, step and gate: %+v", cooked, result)
		}
		poured = append(poured, m)
	}

	// A proto assembled by hand. Its root is labeled template, and its steps reach it each way pour
	// follows: one inherits the label through --parent, one opts out of the label, and one is attached
	// afterwards with a parent-child dependency.
	hand := bdCreateSilent(t, bd, dir, "hand-assembled proto", "--type", "epic", "--labels", "template")
	labeledStep := bdCreateSilent(t, bd, dir, "inherits the template label", "--parent", hand, "--metadata", timestampInNanoseconds)
	unlabeledStep := bdCreateSilent(t, bd, dir, "opts out of the label", "--parent", hand, "--no-inherit-labels", "--metadata", timestampInNanoseconds)
	attached := bdCreateSilent(t, bd, dir, "attached afterwards", "--metadata", timestampInNanoseconds)
	bdRunOK(t, bd, dir, "dep", "add", attached, hand, "--type", "parent-child")

	// A cooked template whose template label was removed afterwards, with an issue attached under it.
	// Pour refuses it ("not a proto"), but bond treats any template as a proto and copies the whole
	// subgraph under it, the attached issue included.
	bareFormula := filepath.Join(t.TempDir(), "bare.formula.toml")
	if err := os.WriteFile(bareFormula, []byte(`formula = "bare"
description = "a template that loses its label"
version = 1
type = "workflow"

[[steps]]
id = "only"
title = "Only"
`), 0o600); err != nil {
		t.Fatalf("writing the bare formula: %v", err)
	}
	bdRunOK(t, bd, dir, "cook", bareFormula, "--persist")
	const bare = "bare"
	underBare := bdCreateSilent(t, bd, dir, "attached under a template without the label", "--metadata", timestampInNanoseconds)
	bdRunOK(t, bd, dir, "dep", "add", underBare, bare, "--type", "parent-child")
	bdRunOK(t, bd, dir, "label", "remove", bare, "template")
	bondTarget := bdCreateSilent(t, bd, dir, "a molecule to bond onto", "--type", "epic")

	// Legacy issues that are part of no proto: no write to them is refused, and no write copies them.
	legacy := bdCreateSilent(t, bd, dir, "legacy, part of no proto", "--metadata", timestampInNanoseconds)
	legacyGate := createTimerGate(t, bd, dir, legacy, "2600h")

	warned := map[string]string{
		labeledStep:   hand,
		unlabeledStep: hand,
		attached:      hand,
		underBare:     bare,
	}
	for _, m := range poured {
		warned[m.step] = m.root
		warned[m.gate] = m.root
	}
	checkWarning := func(out string) {
		t.Helper()
		for id, root := range warned {
			if !warnedUnder(out, id, root) {
				t.Errorf("the switch does not warn of %s under the proto rooted at %s, whose value a pour of it (or a bond, for a template root) copies:\n%s", id, root, out)
			}
		}
		for _, want := range []string{fmt.Sprintf("Warning: %d legacy issues of a proto", len(warned)), "bd label remove <root> template"} {
			if !strings.Contains(out, want) {
				t.Errorf("the switch's warning does not contain %q:\n%s", want, out)
			}
		}
	}

	out, code := bdRunFailCode(t, bd, dir, "config", "set", "versioned-history.enabled", "true")
	if code == 0 {
		t.Fatalf("enabling versioned history over templates a pour would copy exited 0; out=%s", out)
	}
	for _, want := range []string{
		"2 issues hold",
		"  " + cookedStep + "  (template) ",
		"  " + cookedGate + "  (template) ",
		"bd mol pour and bd mol bond copy its value",
		"bd cook <formula> --persist --force",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not contain %q, so it does not say what is wrong or how to fix it:\n%s", want, out)
		}
	}
	checkWarning(out)
	for name, id := range map[string]string{
		"the cooked proto's root":         cooked,
		"the hand-assembled proto's root": hand,
		"the template without the label":  bare,
		"the bond target":                 bondTarget,
		"the legacy issue in no proto":    legacy,
		"the legacy gate in no proto":     legacyGate,
	} {
		if strings.Contains(out, "  "+id+"  ") {
			t.Errorf("the switch names %s (%s), which holds no such value or is part of no proto:\n%s", name, id, out)
		}
	}
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); strings.HasPrefix(got, "true") {
		t.Fatalf("the refused `config set ... true` wrote the setting anyway: config get says %q", got)
	}

	// Ask the mint, through a process that records because its environment says so, what a pour of
	// each proto does: it is refused, which is what the switch named the proto's issues for.
	for _, proto := range []string{cooked, hand, poured[0].root} {
		if !molRefusedWithHistoryOn(t, bd, dir, "pour", proto) {
			t.Errorf("with history on, a pour of %s succeeded although the switch named its issues", proto)
		}
	}
	if !molRefusedWithHistoryOn(t, bd, dir, "bond", bare, bondTarget) {
		t.Errorf("with history on, a bond of the template %s without the label succeeded although the switch named %s under it", bare, underBare)
	}

	// The refusal's template remedy: a template is read-only, so the formula is fixed and cooked again.
	// That alone clears the switch, although the poured molecules still hold the old values.
	if out, _ := bdRunFailCode(t, bd, dir, "update", cookedStep, "--unset-metadata", "ts"); !strings.Contains(out, "read-only") {
		t.Errorf("bd update of the template %s did not refuse as read-only, so the refusal's template remedy is wrong:\n%s", cookedStep, out)
	}
	writeFormula(`"1727000000000000000"`, "2000h")
	bdRunOK(t, bd, dir, "cook", formula, "--persist", "--force")

	out = bdRunOK(t, bd, dir, "config", "set", "versioned-history.enabled", "true")
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", "versioned-history.enabled")); !strings.HasPrefix(got, "true") {
		t.Fatalf("after cooking the fixed formula again the switch did not turn on: config get says %q", got)
	}
	checkWarning(out)

	// History is on now, through the store's own row. The cooked proto pours; the legacy issues the
	// warning named stay writable, and a poured gate can be removed; once the hand-assembled steps are fixed
	// with bd update, that proto pours too; and the legacy issues in no proto stay writable.
	bdRunOK(t, bd, dir, "mol", "pour", cooked)
	bdRunOK(t, bd, dir, "update", poured[0].gate, "--notes", "touched with history on")
	bdRunOK(t, bd, dir, "delete", poured[1].gate, "--force")
	for _, id := range []string{labeledStep, unlabeledStep, attached} {
		bdRunOK(t, bd, dir, "update", id, "--unset-metadata", "ts")
	}
	bdRunOK(t, bd, dir, "mol", "pour", hand)
	bdRunOK(t, bd, dir, "update", underBare, "--unset-metadata", "ts")
	bdRunOK(t, bd, dir, "mol", "bond", bare, bondTarget)
	bdRunOK(t, bd, dir, "update", legacy, "--notes", "touched with history on")
}
