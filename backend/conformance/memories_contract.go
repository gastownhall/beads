package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/memoryops"
)

// This file holds the contract every implementation of memoryops.Memories must
// satisfy. Each case asserts what memoryops/memories.go PROMISES rather than
// what any one backend happens to do today; a backend that disagrees is parked
// at its own wiring site with skipKnownDivergence so the case still runs on the
// ones that agree.
//
// THE VOTE COUNT. Three wirings — the server-backed store, the embedded store
// and the unit-of-work provider — and TWO independent bodies between them: the
// two stores share the InTx functions in internal/storage/memoryops, and the
// unit of work composes domain.ConfigUseCase instead. So this is two readings
// plus an engine check, never "three backends agree". The refusals are a third
// shared thing: they all come from internal/memoryapi, so below that validator
// what these cases test is the EXECUTION half.
//
// THE SEEDING DISCIPLINE IS THE POINT OF THIS FILE, more than the case list is.
// The config table holds FOUR classes of row at once and this plane owns
// exactly one of them:
//
//	settings rows        issue_prefix, custom.*
//	generic kv rows      kv.* written by `bd kv set`
//	memory rows          kv.memory.*
//	the trap class       kv.memory.<a settings name> — a memory that SHADOWS a
//	                     setting, which is a memory, while the setting is not
//
// Every READ case runs against a table seeded with all four, and every WRITE
// case asserts the other three survived, through RAW ROWS rather than through
// the role. A suite that seeded only memory rows would be green over every bug
// this plane can actually have: a body filtering on "kv." instead of
// "kv.memory.", a prefix trim of the wrong length, a LIKE-shaped delete. Each
// of those produces a plausible answer, which is why reading it back through
// the role is exactly the check that passes on a corrupted table. The Deleter
// suite went 59 cases green over a live-row corruption for want of one seeded
// cross-plane row.
//
// KEYS ARE NAMESPACED WITH THE FIXTURE PREFIX wherever they can be, because
// config keys are global to a workspace and these cases share one. The two that
// cannot be are issue_prefix and its shadow: the whole point of the trap class
// is that the shadowed name is a real settings key.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN: the one-transaction promise. It
// is structural, not black-box-observable — a single-threaded case cannot
// falsify it and a concurrency case would be flaky at three engines. It is
// pinned instead by the SHAPE of the bodies (there is no two-call composition
// to regress to without deleting the …InTx functions) and by review. If a cheap
// deterministic probe ever emerges — a transaction-counting seam on the kit —
// add a case for it; do not fake one with sleeps.

// MemoriesFixture supplies adapter-specific storage access for the memory
// assertions. Every field is named and typed exactly like the per-backend
// roleFixtureKit hook it is filled from.
type MemoriesFixture struct {
	// IssuePrefix namespaces the keys each assertion writes, so several of them
	// can share one database.
	IssuePrefix string
	Memories    memoryops.Memories
	// SetConfig writes one workspace config key OUT OF BAND, past the role, by
	// its FULL storage key. It is how these cases seed the three classes this
	// plane does not own, and how they create the one row no front door can: a
	// memory stored as the empty string.
	SetConfig func(context.Context, string, string) error
	// QueryScalar runs a single-row query and scans it, and RETURNS the error
	// rather than failing the test. It is how the cases read RAW ROWS — the
	// only way to tell "the answer looks right" from "the table is right".
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// memoriesNeighbors are the rows a memory operation must not touch: one
// settings row, one generic kv row, one ordinary memory and the shadow memory
// whose user key is a settings name.
type memoriesNeighbors struct {
	settingKey, settingValue string
	genericKey, genericValue string
	memoryKey, memoryValue   string
	shadowKey, shadowValue   string
}

// seedMemoriesAllFourClasses writes one row of every class the config table
// holds and returns them, so a case can assert afterwards that the three it
// does not own are byte-identical.
//
// It is called by EVERY case, read and write alike. That is deliberate
// repetition: a helper that only some cases called would leave the others
// running against whichever rows the previous subtest happened to leave.
func seedMemoriesAllFourClasses(t *testing.T, ctx context.Context, fixture MemoriesFixture) memoriesNeighbors {
	t.Helper()
	n := memoriesNeighbors{
		settingKey:   "custom." + fixture.IssuePrefix + "-setting",
		settingValue: "a settings row, not a memory",
		genericKey:   "kv." + fixture.IssuePrefix + "-generic",
		genericValue: "a bd kv row, not a memory",
		memoryKey:    "kv.memory." + fixture.IssuePrefix + "-neighbor",
		memoryValue:  "a memory that no case under test names",
		// The trap: a memory CALLED issue_prefix. Its storage key differs from
		// the workspace's own issue_prefix row by the prefix alone, so a
		// de-prefixing bug re-keys the workspace's identity.
		shadowKey:   "kv.memory.issue_prefix",
		shadowValue: "the prefix rename runbook lives in engdocs",
	}
	for _, row := range [][2]string{
		{"issue_prefix", fixture.IssuePrefix},
		{n.settingKey, n.settingValue},
		{n.genericKey, n.genericValue},
		{n.memoryKey, n.memoryValue},
		{n.shadowKey, n.shadowValue},
	} {
		if err := fixture.SetConfig(ctx, row[0], row[1]); err != nil {
			t.Fatalf("seed %q out of band: %v", row[0], err)
		}
	}
	return n
}

// assertMemoriesNeighborsSurvived is the other half of the discipline: after a
// write, the three classes this plane does not own are still exactly what the
// seed left, read as RAW ROWS.
func assertMemoriesNeighborsSurvived(t *testing.T, ctx context.Context, fixture MemoriesFixture, n memoriesNeighbors) {
	t.Helper()
	assertMemoriesRawValue(t, ctx, fixture, "issue_prefix", fixture.IssuePrefix)
	assertMemoriesRawValue(t, ctx, fixture, n.settingKey, n.settingValue)
	assertMemoriesRawValue(t, ctx, fixture, n.genericKey, n.genericValue)
	assertMemoriesRawValue(t, ctx, fixture, n.memoryKey, n.memoryValue)
	assertMemoriesRawValue(t, ctx, fixture, n.shadowKey, n.shadowValue)
}

// RunMemoriesListOfAnEmptyPlaneAnswersAnEmptyMap pins memoryops.ListResult's
// "empty map, never nil" on the one state that can produce a nil: a workspace
// with no memory rows at all.
//
// IT MUST RUN FIRST in each wiring, before any case seeds. All three backends
// build a fresh workspace per suite, so "first" is enough; the case says so
// loudly rather than weakening itself, because the property it pins is only
// reachable from an untouched plane.
func RunMemoriesListOfAnEmptyPlaneAnswersAnEmptyMap(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	result, err := fixture.Memories.List(ctx, memoryops.ListRequest{})
	if err != nil {
		t.Fatalf("List on an empty plane: %v", err)
	}
	if result.Memories == nil {
		t.Fatal("List returned a nil map; the contract promises an empty one")
	}
	if len(result.Memories) != 0 {
		t.Fatalf("List on what should be an untouched plane returned %v.\n"+
			"This case pins the nil-versus-empty answer and can only see it before "+
			"anything is stored: run it FIRST in this wiring's subtest order.", result.Memories)
	}
}

// RunMemoriesRememberStoresContentVerbatim pins memoryops.RememberRequest's
// "stored VERBATIM": newlines, surrounding space and unicode all survive.
//
// The raw-row assertion is the load-bearing half. Reading the value back
// through Recall would pass on a body that truncated for display and on a body
// that stored under the wrong key, because it would make the same two mistakes
// in reverse. This asserts the row is at EXACTLY kv.memory.<key> holding
// EXACTLY those bytes.
func RunMemoriesRememberStoresContentVerbatim(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)
	key := fixture.IssuePrefix + "-verbatim"
	const content = "  first line\nsecond line — with an em dash  "

	result := rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: key, Content: content})
	if result.Key != key {
		t.Fatalf("Remember result key = %q, want %q", result.Key, key)
	}
	if result.Value != content {
		t.Fatalf("Remember result value = %q, want the content verbatim %q", result.Value, content)
	}
	assertMemoriesRawValue(t, ctx, fixture, "kv.memory."+key, content)
	assertMemoriesRecall(t, ctx, fixture, key, content, true)
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
}

// RunMemoriesRememberDerivesTheKeyWhenAbsent pins that a request with no key is
// the NORMAL case and that the role derives it — memoryops.RememberRequest.Key.
//
// The expected key is SPELLED OUT here rather than recomputed with DeriveKey,
// so this case is a second, independent statement of the derivation and not a
// tautology. The byte-level golden table lives in internal/memoryapi's unit
// tests; this one pins that the role reaches it at all, and that the memory is
// recallable under what the result reported.
func RunMemoriesRememberDerivesTheKeyWhenAbsent(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)
	content := fixture.IssuePrefix + " Derives The Key, From Content!"
	want := strings.ToLower(fixture.IssuePrefix) + "-derives-the-key-from-content"

	result := rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Content: content})
	if result.Key != want {
		t.Fatalf("Remember(%q) derived key %q, want %q", content, result.Key, want)
	}
	assertMemoriesRawValue(t, ctx, fixture, "kv.memory."+want, content)
	assertMemoriesRecall(t, ctx, fixture, want, content, true)
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
}

// RunMemoriesRememberWithExplicitKeyStoresVerbatim pins the other half of
// RememberRequest.Key: an explicit key is used with NO normalization, so a
// memory stays recallable under the exact bytes the caller used.
//
// The key carries a space, a dot and a non-ASCII character on purpose: every
// one of them is something a slugifier would eat, and `bd remember --key` has
// always accepted all three.
func RunMemoriesRememberWithExplicitKeyStoresVerbatim(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)
	key := fixture.IssuePrefix + "-Has Spaces.✓"
	const content = "an explicit key is not a slug"

	result := rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: key, Content: content})
	if result.Key != key {
		t.Fatalf("Remember result key = %q, want the explicit key verbatim %q", result.Key, key)
	}
	assertMemoriesRawValue(t, ctx, fixture, "kv.memory."+key, content)
	assertMemoriesRecall(t, ctx, fixture, key, content, true)
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
}

// RunMemoriesRememberReplacesAndReportsIt pins
// memoryops.RememberResult.Replaced, including the leg that makes it different
// from RecallResult.Found.
//
// Three legs: a first write reports false, a second reports true and REPLACES
// rather than appends, and a write over an out-of-band EMPTY row reports true
// even though a Recall of that key would have reported Found false. The third
// is the divergence the leaf doc states — Replaced is about the ROW — and it is
// the one a body computing `previous != ""` gets wrong.
func RunMemoriesRememberReplacesAndReportsIt(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)
	key := fixture.IssuePrefix + "-replaced"

	first := rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: key, Content: "first"})
	if first.Replaced {
		t.Fatalf("Remember of a key nothing stored reported Replaced true")
	}
	second := rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: key, Content: "second"})
	if !second.Replaced {
		t.Fatalf("Remember over an existing memory reported Replaced false")
	}
	assertMemoriesRawValue(t, ctx, fixture, "kv.memory."+key, "second")

	// The row exists and holds "". No front door can create it; an out-of-band
	// write and a cross-clone merge both can.
	emptied := fixture.IssuePrefix + "-replaced-empty"
	if err := fixture.SetConfig(ctx, "kv.memory."+emptied, ""); err != nil {
		t.Fatalf("seed an empty memory row out of band: %v", err)
	}
	assertMemoriesRecall(t, ctx, fixture, emptied, "", false)
	overEmpty := rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: emptied, Content: "now it has content"})
	if !overEmpty.Replaced {
		t.Fatalf("Remember over a row stored EMPTY reported Replaced false: Replaced is about the row, " +
			"not the value — see memoryops.RememberResult.Replaced")
	}
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
}

// RunMemoriesRememberRefusesEmptyContent pins the first refusal as the SENTINEL
// rather than the message text — a wrapper that reformatted the prose would
// still satisfy every caller that classifies, and a message assertion would
// fail on it — and pins that NOTHING lands when it fires.
func RunMemoriesRememberRefusesEmptyContent(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)
	key := fixture.IssuePrefix + "-refused-empty"

	for _, blank := range []string{"", "   "} {
		if _, err := fixture.Memories.Remember(ctx, memoryops.RememberRequest{Key: key, Content: blank}); !errors.Is(err, memoryops.ErrValidation) {
			t.Fatalf("Remember(content %q) error = %v, want ErrValidation", blank, err)
		}
		assertMemoriesRawAbsent(t, ctx, fixture, "kv.memory."+key)
	}
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
}

// RunMemoriesRememberRefusesAWhitespaceOnlyKey pins the rule Remember shares
// with Recall and Forget, over the quadrant that had no case.
//
// Recall and Forget refuse a key that is empty after trimming — cases 8 and 13
// pin that. Remember used to accept any non-empty string, so `--key "   "`
// minted a row no memory operation could ever name again: enumerated by List
// forever, unrecallable, unforgettable, reachable only through
// `bd config unset` on the raw storage key. The write door accepted what every
// read door refused, and the HTTP surface inherited the split exactly — POST
// accepted the key that GET and DELETE answered 400 for.
//
// The surviving half is asserted too, because the rule is that a key must NAME
// something and not that it must be tidy: a key with surrounding space is
// stored and recalled byte for byte.
func RunMemoriesRememberRefusesAWhitespaceOnlyKey(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)

	for _, key := range []string{" ", "   ", "\t"} {
		_, err := fixture.Memories.Remember(ctx, memoryops.RememberRequest{Key: key, Content: "content"})
		if !errors.Is(err, memoryops.ErrValidation) {
			t.Fatalf("Remember(key %q) error = %v, want ErrValidation: a key no read can name must not be writable", key, err)
		}
		assertMemoriesRawAbsent(t, ctx, fixture, "kv.memory."+key)
	}

	// Surrounding space survives: this is a trim REFUSAL, not a trim.
	spaced := " " + fixture.IssuePrefix + "-spaced "
	if _, err := fixture.Memories.Remember(ctx, memoryops.RememberRequest{Key: spaced, Content: "kept"}); err != nil {
		t.Fatalf("Remember(key %q) error = %v, want it stored verbatim", spaced, err)
	}
	got, err := fixture.Memories.Recall(ctx, memoryops.RecallRequest{Key: spaced})
	if err != nil {
		t.Fatalf("Recall(%q) error = %v", spaced, err)
	}
	if !got.Found || got.Value != "kept" {
		t.Errorf("Recall(%q) = %+v, want the value under the exact bytes written", spaced, got)
	}
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
}

// RunMemoriesRememberRefusesAnUnderivableKey pins the second refusal: content
// with nothing to derive from, and no key to fall back on.
//
// The raw assertion is on the EMPTY user key, because that is what a body which
// skipped the check would have written: a row at "kv.memory." holding the
// content, which nothing can ever name again.
func RunMemoriesRememberRefusesAnUnderivableKey(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)

	if _, err := fixture.Memories.Remember(ctx, memoryops.RememberRequest{Content: "!!!"}); !errors.Is(err, memoryops.ErrValidation) {
		t.Fatalf("Remember(content %q, no key) error = %v, want ErrValidation", "!!!", err)
	}
	assertMemoriesRawAbsent(t, ctx, fixture, "kv.memory.")
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
}

// RunMemoriesRecallAnswersTheStoredValue pins that Recall reads THIS plane and
// only this plane, against a table holding all four classes.
//
// The shadow leg is the sharp one: `bd recall issue_prefix` answers the MEMORY
// called issue_prefix, not the workspace's issue prefix. A body that trimmed
// the wrong number of bytes, or looked the user key up unprefixed, would serve
// the workspace's identity as a user's note.
func RunMemoriesRecallAnswersTheStoredValue(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)

	assertMemoriesRecall(t, ctx, fixture, fixture.IssuePrefix+"-neighbor", neighbors.memoryValue, true)
	assertMemoriesRecall(t, ctx, fixture, "issue_prefix", neighbors.shadowValue, true)

	// The three classes this plane does not own are not reachable through it,
	// under either the bare name or the stored one.
	for _, absent := range []string{
		neighbors.settingKey,
		neighbors.genericKey,
		fixture.IssuePrefix + "-generic",
		"kv.memory." + fixture.IssuePrefix + "-neighbor",
	} {
		assertMemoriesRecall(t, ctx, fixture, absent, "", false)
	}
}

// RunMemoriesRecallReportsAMissAsNotFoundNotAnError pins the decision recorded
// in memoryops/errors.go: there is no ErrNotFound on this role, because the
// seam beneath it cannot see the difference the error would claim.
func RunMemoriesRecallReportsAMissAsNotFoundNotAnError(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	seedMemoriesAllFourClasses(t, ctx, fixture)
	key := fixture.IssuePrefix + "-never-remembered"

	result, err := fixture.Memories.Recall(ctx, memoryops.RecallRequest{Key: key})
	if err != nil {
		t.Fatalf("Recall of a key nothing stored = %v, want a nil error with Found false", err)
	}
	if result.Found || result.Value != "" {
		t.Fatalf("Recall of a key nothing stored = %+v, want Found false and an empty value", result)
	}
	if result.Key != key {
		t.Fatalf("Recall result key = %q, want %q", result.Key, key)
	}

	// The empty key is a refusal rather than a miss: there is no row a caller
	// could mean by it.
	for _, blank := range []string{"", "   "} {
		if _, err := fixture.Memories.Recall(ctx, memoryops.RecallRequest{Key: blank}); !errors.Is(err, memoryops.ErrValidation) {
			t.Fatalf("Recall(%q) error = %v, want ErrValidation", blank, err)
		}
	}
}

// RunMemoriesRecallConflatesStoredEmptyWithAbsent pins
// memoryops.RecallResult.Found's central claim, and the one asymmetry that lets
// a caller see past it: List enumerates the key, because the ROW exists.
//
// That is the same same-answer-different-row shape the settings contract pins
// for `bd config get`, and stating it is what keeps a backend from "fixing" it
// on one leg and making the answer depend on the route.
func RunMemoriesRecallConflatesStoredEmptyWithAbsent(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	seedMemoriesAllFourClasses(t, ctx, fixture)
	emptied := fixture.IssuePrefix + "-stored-empty"
	never := fixture.IssuePrefix + "-never-stored"

	if err := fixture.SetConfig(ctx, "kv.memory."+emptied, ""); err != nil {
		t.Fatalf("seed an empty memory row out of band: %v", err)
	}
	assertMemoriesRecall(t, ctx, fixture, emptied, "", false)
	assertMemoriesRecall(t, ctx, fixture, never, "", false)

	plane := listMemories(t, ctx, fixture, "")
	if value, ok := plane[emptied]; !ok || value != "" {
		t.Fatalf("List[%q] = %q (present=%v), want an empty value that IS enumerated: "+
			"the row exists even though Recall denies it", emptied, value, ok)
	}
	if _, ok := plane[never]; ok {
		t.Fatalf("List carries %q, which nothing stored", never)
	}
}

// RunMemoriesForgetRemovesExactlyTheNamedRow IS THE QUADRANT CASE, and the
// reason this contract has a seeding discipline instead of a case list.
//
// It seeds a memory whose key is a PREFIX of another memory's key, a generic kv
// row whose storage key is what the memory's would be one prefix up, and a
// settings row — then forgets the first and asserts, through raw rows, that the
// other three are still there.
//
// Each of the three bugs it catches produces a plausible answer through the
// role: a LIKE-shaped delete takes the adjacent memory with it, a delete
// composed from the short prefix takes the `bd kv` row instead, and an
// unanchored match takes the settings row. This is the Deleter bug family, and
// the analog of it went 59 green cases undetected.
func RunMemoriesForgetRemovesExactlyTheNamedRow(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)

	target := fixture.IssuePrefix + "-a"
	adjacent := fixture.IssuePrefix + "-a-b"
	generic := "kv." + target // what the memory's key would be one prefix up

	for _, row := range [][2]string{
		{"kv.memory." + target, "the memory being forgotten"},
		{"kv.memory." + adjacent, "the memory that must survive"},
		{generic, "a bd kv value that must survive"},
	} {
		if err := fixture.SetConfig(ctx, row[0], row[1]); err != nil {
			t.Fatalf("seed %q out of band: %v", row[0], err)
		}
	}

	result := forgetMemory(t, ctx, fixture, target)
	if !result.Found || result.Value != "the memory being forgotten" {
		t.Fatalf("Forget(%q) = %+v, want Found true with the stored value", target, result)
	}

	assertMemoriesRawAbsent(t, ctx, fixture, "kv.memory."+target)
	assertMemoriesRawValue(t, ctx, fixture, "kv.memory."+adjacent, "the memory that must survive")
	assertMemoriesRawValue(t, ctx, fixture, generic, "a bd kv value that must survive")
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)

	// And through the role, because a delete that also broke the read would
	// otherwise look consistent.
	assertMemoriesRecall(t, ctx, fixture, target, "", false)
	assertMemoriesRecall(t, ctx, fixture, adjacent, "the memory that must survive", true)
}

// RunMemoriesForgetNeverTouchesTheSettingsPlane is foldable into the case above
// and kept separate because its failure mode deserves its own name: a
// de-prefixing bug here does not lose a note, it RE-KEYS THE WORKSPACE. Every
// bead created before the write and every bead created after it would disagree
// about their own namespace, with nothing to reconcile them.
func RunMemoriesForgetNeverTouchesTheSettingsPlane(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)

	result := forgetMemory(t, ctx, fixture, "issue_prefix")
	if !result.Found || result.Value != neighbors.shadowValue {
		t.Fatalf("Forget(%q) = %+v, want the SHADOW memory's value %q", "issue_prefix", result, neighbors.shadowValue)
	}

	assertMemoriesRawAbsent(t, ctx, fixture, "kv.memory.issue_prefix")
	assertMemoriesRawValue(t, ctx, fixture, "issue_prefix", fixture.IssuePrefix)
	assertMemoriesRawValue(t, ctx, fixture, neighbors.settingKey, neighbors.settingValue)
	assertMemoriesRawValue(t, ctx, fixture, neighbors.genericKey, neighbors.genericValue)
}

// RunMemoriesForgetReportsTheForgottenValue pins memoryops.ForgetResult.Value:
// what `bd forget` prints is the content of the row it removed, read in the
// same transaction as the delete.
func RunMemoriesForgetReportsTheForgottenValue(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)
	key := fixture.IssuePrefix + "-reported"
	const content = "the value the caller is about to be shown"

	rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: key, Content: content})
	result := forgetMemory(t, ctx, fixture, key)
	if result.Key != key {
		t.Fatalf("Forget result key = %q, want %q", result.Key, key)
	}
	if !result.Found || result.Value != content {
		t.Fatalf("Forget(%q) = %+v, want Found true with value %q", key, result, content)
	}
	// The row really went, and nothing beside it did. This is the only
	// forget-after-Remember sequence in the suite — every other forget case
	// removes an out-of-band seed — so it is the one place a delete whose
	// storage key came from the PRECEDING write could do collateral damage.
	// Without these two lines the next case's seed rewrites all four neighbor
	// rows and repairs the damage before anything observes it.
	assertMemoriesRawAbsent(t, ctx, fixture, "kv.memory."+key)
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)
	assertMemoriesRecall(t, ctx, fixture, key, "", false)
}

// RunMemoriesForgetOfAnAbsentKeyIsNotFoundAndDeletesNothing pins that a miss is
// a RESULT and not an error, that it changes no row anywhere in the table, that
// the second call — the one a retrying caller actually makes — answers the same
// way, and that the SECOND kind of miss behaves like the first: a memory stored
// as the empty string is Found false, and its row is left standing.
//
// That last leg is memoryops.ForgetResult.Found's parenthesis, and it keeps
// Forget's answer and Recall's answer the same statement. A body that deleted
// on "the row is there" rather than on "Recall would find it" would report
// nothing removed while removing something.
//
// The whole-table row count is the assertion that matters: "the key is still
// absent" would pass on a delete that swept a neighboring plane.
func RunMemoriesForgetOfAnAbsentKeyIsNotFoundAndDeletesNothing(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)
	key := fixture.IssuePrefix + "-never-stored-forget"
	emptied := fixture.IssuePrefix + "-empty-forget"
	if err := fixture.SetConfig(ctx, "kv.memory."+emptied, ""); err != nil {
		t.Fatalf("seed an empty memory row out of band: %v", err)
	}

	var before int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM config", nil, &before); err != nil {
		t.Fatalf("count config rows before: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		for _, miss := range []string{key, emptied} {
			result, err := fixture.Memories.Forget(ctx, memoryops.ForgetRequest{Key: miss})
			if err != nil {
				t.Fatalf("Forget(%q) attempt %d = %v, want success with Found false", miss, attempt, err)
			}
			if result.Found || result.Value != "" {
				t.Fatalf("Forget(%q) attempt %d = %+v, want Found false and no value", miss, attempt, result)
			}
		}
	}

	var after int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM config", nil, &after); err != nil {
		t.Fatalf("count config rows after: %v", err)
	}
	if after != before {
		t.Fatalf("config rows went %d -> %d across four forgets that found nothing, want no change", before, after)
	}
	// The stored-empty row is specifically still there: Found false said nothing
	// was removed, and that has to be true of the row as well as of the answer.
	assertMemoriesRawValue(t, ctx, fixture, "kv.memory."+emptied, "")
	assertMemoriesNeighborsSurvived(t, ctx, fixture, neighbors)

	// The empty key is a refusal, not a miss.
	for _, blank := range []string{"", "   "} {
		if _, err := fixture.Memories.Forget(ctx, memoryops.ForgetRequest{Key: blank}); !errors.Is(err, memoryops.ErrValidation) {
			t.Fatalf("Forget(%q) error = %v, want ErrValidation", blank, err)
		}
	}
}

// RunMemoriesListReturnsOnlyTheMemoryPlane pins memoryops.ListResult.Memories
// against a table holding all four classes.
//
// It asserts in both directions, and the second one is what the case is for:
// the seeded memories are present under their STRIPPED keys, and no answer key
// belongs to a plane this role does not own. A body filtering on "kv." answers
// with keys beginning "memory."; a trim of the wrong length answers with the
// same; no filter at all answers with the workspace's settings. All three look
// like memories to a caller.
func RunMemoriesListReturnsOnlyTheMemoryPlane(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	neighbors := seedMemoriesAllFourClasses(t, ctx, fixture)

	plane := listMemories(t, ctx, fixture, "")

	if got := plane[fixture.IssuePrefix+"-neighbor"]; got != neighbors.memoryValue {
		t.Fatalf("List[%q] = %q, want %q — the key must arrive with kv.memory. stripped",
			fixture.IssuePrefix+"-neighbor", got, neighbors.memoryValue)
	}
	// The trap: the answer DOES carry issue_prefix, and it carries the memory's
	// value rather than the workspace's prefix.
	if got := plane["issue_prefix"]; got != neighbors.shadowValue {
		t.Fatalf("List[%q] = %q, want the shadow MEMORY's value %q, not the workspace's prefix %q",
			"issue_prefix", got, neighbors.shadowValue, fixture.IssuePrefix)
	}

	for _, foreign := range []string{
		neighbors.settingKey,                          // the settings row, unfiltered
		neighbors.genericKey,                          // the generic kv row, unfiltered
		fixture.IssuePrefix + "-generic",              // the generic kv row, "kv." trimmed
		"memory." + fixture.IssuePrefix + "-neighbor", // a memory with only "kv." trimmed
	} {
		if value, ok := plane[foreign]; ok {
			t.Fatalf("List carries %q = %q, which is not a memory: the answer is the kv.memory. plane only",
				foreign, value)
		}
	}
	for key := range plane {
		if strings.HasPrefix(key, "kv.") || strings.HasPrefix(key, "memory.") || strings.HasPrefix(key, "custom.") {
			t.Fatalf("List carries %q: the keys are USER keys, so a storage prefix in one means the "+
				"answer was narrowed or trimmed at the wrong boundary", key)
		}
	}
}

// RunMemoriesListSearchMatchesTheUserKeyNotTheStorageKey pins WHICH STRING the
// search folds over, which no other case can see.
//
// ListRequest.Search promises a match against the USER key. Every term the
// case below uses is a substring of the user key exactly when it is a substring
// of the storage key, so a body that filtered BEFORE stripping the prefix —
// one transposed line — passes all of them. A term that appears only in the
// storage form separates the two: "kv." matches every row before the strip and
// none after it.
//
// The failure it catches is route-dependent answers: `bd memories memory` would
// return the ENTIRE plane on the leg that folded over storage keys, because
// every storage key contains "kv.memory.", while the other leg returned genuine
// matches. That is the wrong-boundary bug family this file's own header names.
func RunMemoriesListSearchMatchesTheUserKeyNotTheStorageKey(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	seedMemoriesAllFourClasses(t, ctx, fixture)
	rememberMemory(t, ctx, fixture, memoryops.RememberRequest{
		Key: fixture.IssuePrefix + "-plain", Content: "nothing prefix-shaped in here",
	})

	for _, term := range []string{"kv.", "kv.memory.", "memory."} {
		if got := listMemories(t, ctx, fixture, term); len(got) != 0 {
			t.Errorf("List(search %q) matched %d memories, want 0: the fold is over the USER key, "+
				"and %q appears only in the storage form", term, len(got), term)
		}
	}
}

// RunMemoriesListSearchMatchesKeyOrValueCaseInsensitively pins
// memoryops.ListRequest.Search's "the folding is THIS ROLE'S": the raw term the
// user typed goes in, and matching is case-insensitive on both sides.
//
// The shipped `bd memories` lowercased the term at the front door, so a second
// door passing it through raw would have reported an empty plane. This case
// passes an UPPERCASE term at a lowercase value and vice versa, which is the
// pair that fails if either fold is missing.
func RunMemoriesListSearchMatchesKeyOrValueCaseInsensitively(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	seedMemoriesAllFourClasses(t, ctx, fixture)
	byKey := fixture.IssuePrefix + "-searchable-KEY"
	byValue := fixture.IssuePrefix + "-searched-by-value"

	rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: byKey, Content: "content with nothing to match on"})
	rememberMemory(t, ctx, fixture, memoryops.RememberRequest{Key: byValue, Content: "this one mentions PHANTOMS"})

	// A lowercase term against an uppercase key, and an uppercase term against
	// a value whose match is uppercase too — both sides folded.
	if got := listMemories(t, ctx, fixture, "searchable-key"); got[byKey] == "" {
		t.Fatalf("List(search=%q) = %v, want the memory whose KEY matches case-insensitively", "searchable-key", got)
	}
	if got := listMemories(t, ctx, fixture, "phantoms"); got[byValue] == "" {
		t.Fatalf("List(search=%q) = %v, want the memory whose VALUE matches case-insensitively", "phantoms", got)
	}
	if got := listMemories(t, ctx, fixture, "SEARCHED-BY-VALUE"); got[byValue] == "" {
		t.Fatalf("List(search=%q) = %v, want the memory whose key matches the folded term", "SEARCHED-BY-VALUE", got)
	}

	// A miss is an empty map, never nil and never an error.
	miss := listMemories(t, ctx, fixture, fixture.IssuePrefix+"-matches-nothing-at-all")
	if len(miss) != 0 {
		t.Fatalf("List(search that matches nothing) = %v, want an empty map", miss)
	}
}

// RunMemoriesARefusedWriteRecordsNoHistory pins the other half of "and NOTHING
// is written": a refusal does not reach storage at all, so it leaves no history
// entry behind either.
//
// The delta is taken around the refusals rather than read off the top of the
// log, because two commits made inside one second tie on date.
func RunMemoriesARefusedWriteRecordsNoHistory(t *testing.T, ctx context.Context, fixture MemoriesFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the no-write half of a refusal is unobservable here")
	}
	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}

	for _, req := range []memoryops.RememberRequest{
		{Key: fixture.IssuePrefix + "-refused-history", Content: ""},
		{Key: fixture.IssuePrefix + "-refused-history", Content: "   "},
		{Content: "!!!"},
	} {
		if _, err := fixture.Memories.Remember(ctx, req); !errors.Is(err, memoryops.ErrValidation) {
			t.Fatalf("Remember(%+v) error = %v, want ErrValidation", req, err)
		}
	}
	if _, err := fixture.Memories.Forget(ctx, memoryops.ForgetRequest{Key: ""}); !errors.Is(err, memoryops.ErrValidation) {
		t.Fatalf("Forget(\"\") error = %v, want ErrValidation", err)
	}

	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Fatalf("history entries went %d -> %d across four refused writes, want no change", before, after)
	}
}

func rememberMemory(t *testing.T, ctx context.Context, fixture MemoriesFixture, req memoryops.RememberRequest) memoryops.RememberResult {
	t.Helper()
	result, err := fixture.Memories.Remember(ctx, req)
	if err != nil {
		t.Fatalf("Remember(key=%q, content=%q): %v", req.Key, req.Content, err)
	}
	return result
}

func forgetMemory(t *testing.T, ctx context.Context, fixture MemoriesFixture, key string) memoryops.ForgetResult {
	t.Helper()
	result, err := fixture.Memories.Forget(ctx, memoryops.ForgetRequest{Key: key})
	if err != nil {
		t.Fatalf("Forget(%q): %v", key, err)
	}
	return result
}

func listMemories(t *testing.T, ctx context.Context, fixture MemoriesFixture, search string) map[string]string {
	t.Helper()
	result, err := fixture.Memories.List(ctx, memoryops.ListRequest{Search: search})
	if err != nil {
		t.Fatalf("List(search=%q): %v", search, err)
	}
	if result.Memories == nil {
		t.Fatalf("List(search=%q) returned a nil map; the contract promises an empty one", search)
	}
	return result.Memories
}

func assertMemoriesRecall(t *testing.T, ctx context.Context, fixture MemoriesFixture, key, wantValue string, wantFound bool) {
	t.Helper()
	result, err := fixture.Memories.Recall(ctx, memoryops.RecallRequest{Key: key})
	if err != nil {
		t.Fatalf("Recall(%q): %v", key, err)
	}
	if result.Key != key {
		t.Fatalf("Recall(%q) echoed key %q", key, result.Key)
	}
	if result.Value != wantValue || result.Found != wantFound {
		t.Fatalf("Recall(%q) = value %q found %v, want value %q found %v", key, result.Value, result.Found, wantValue, wantFound)
	}
}

// assertMemoriesRawValue reads ONE config row by its full storage key. It is
// how every write case checks the three planes it does not own, and how the
// encoding itself is pinned: the role gives no way to see whether a memory
// landed at kv.memory.<key> or somewhere adjacent that reads back the same.
func assertMemoriesRawValue(t *testing.T, ctx context.Context, fixture MemoriesFixture, storageKey, want string) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, "SELECT value FROM config WHERE `key` = ?", []any{storageKey}, &got); err != nil {
		t.Fatalf("read raw config row %q: %v", storageKey, err)
	}
	if got != want {
		t.Fatalf("raw config row %q = %q, want %q", storageKey, got, want)
	}
}

// assertMemoriesRawAbsent counts rather than selects, because a backend's
// missing-row error is its own business and a case that asserted on one would
// pass for the wrong reason on the others.
func assertMemoriesRawAbsent(t *testing.T, ctx context.Context, fixture MemoriesFixture, storageKey string) {
	t.Helper()
	var rows int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM config WHERE `key` = ?", []any{storageKey}, &rows); err != nil {
		t.Fatalf("count raw config rows for %q: %v", storageKey, err)
	}
	if rows != 0 {
		t.Fatalf("raw config row %q exists (%d rows), want none", storageKey, rows)
	}
}
